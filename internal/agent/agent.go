// Package agent implements the LLM agent loop with tool-use support.
// It sends messages to an OpenAI-compatible API, handles tool calls,
// executes them, and feeds results back to the LLM.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"smithly.dev/internal/db"
	"smithly.dev/internal/sandbox"
	"smithly.dev/internal/skills"
	"smithly.dev/internal/tools"
	"smithly.dev/internal/workspace"
)

// ErrTokenLimitReached is returned when any cost spending window is exceeded.
var ErrTokenLimitReached = fmt.Errorf("agent paused: token usage limit reached")

// Agent represents a running agent with its workspace, memory, tools, and LLM connection.
type Agent struct {
	ID          string
	Model       string
	BaseURL     string
	APIKey      string
	MaxContext  int // max context window in tokens (0 = default 128k)
	CostWindows []*CostWindow
	Pricing     ModelPricing
	Workspace   *workspace.Workspace
	Store       db.Store
	Tools       *tools.Registry
	Skills      *skills.Registry
	Services    *Services
	CodeRunner  sandbox.Provider
	LLM         LLMClient
}

// New creates a new agent.
func New(id, model, provider, baseURL, apiKey string, ws *workspace.Workspace, store db.Store) *Agent {
	return NewWithClient(id, model, provider, baseURL, apiKey, ws, store, &http.Client{})
}

// NewWithClient creates a new agent with a custom HTTP client (for testing).
func NewWithClient(id, model, provider, baseURL, apiKey string, ws *workspace.Workspace, store db.Store, client *http.Client) *Agent {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Agent{
		ID:        id,
		Model:     model,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		Workspace: ws,
		Store:     store,
		Tools:     tools.NewRegistry(),
		Skills:    skills.NewRegistry(),
		LLM:       NewLLMClientForModel(provider, model, baseURL, apiKey, client),
	}
}

// Callbacks for the agent loop — the channel (CLI, web, etc.) provides these.
type Callbacks struct {
	// OnDelta is called for each streamed token of assistant text.
	OnDelta func(token string)

	// OnToolCall is called when the agent wants to use a tool.
	// Receives tool name and arguments. Used to display what's happening.
	OnToolCall func(name string, args string)

	// OnToolResult is called with the tool's output.
	OnToolResult func(name string, result string)

	// Approve is called when a tool needs user approval.
	// Returns true if the user approves.
	Approve tools.ApprovalFunc

	// OnPaused is called when the agent hits a token limit window.
	// Receives the window description and time until reset.
	OnPaused func(window string, remaining time.Duration)
}

// chatMessage is a message in the OpenAI chat format, extended for tool use.
type chatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"` // string or null
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatRequest is the OpenAI-compatible chat completion request.
type chatRequest struct {
	Model    string             `json:"model"`
	Messages []chatMessage      `json:"messages"`
	Tools    []tools.OpenAITool `json:"tools,omitempty"`
	Stream   bool               `json:"stream"`
}

// Paused returns true if any cost window is exceeded.
func (a *Agent) Paused() bool {
	return checkCostWindows(a.CostWindows) != nil
}

// Boot runs the BOOT.md content as the first message if it exists.
// Called once when the agent starts. Returns empty string if no boot content.
func (a *Agent) Boot(ctx context.Context, cb *Callbacks) (string, error) {
	if a.Workspace.Boot == "" {
		return "", nil
	}
	return a.Chat(ctx, a.Workspace.Boot, cb)
}

// Chat sends a user message, runs the agent loop (possibly multiple LLM round-trips
// if tool calls are involved), and returns the final text response.
func (a *Agent) Chat(ctx context.Context, userMessage string, cb *Callbacks) (string, error) {
	if w := checkCostWindows(a.CostWindows); w != nil {
		if cb != nil && cb.OnPaused != nil {
			cb.OnPaused(w.formatWindow(), w.remaining())
		}
		return "", ErrTokenLimitReached
	}
	if cb == nil {
		cb = &Callbacks{}
	}

	// Save user message
	if err := a.Store.AppendMessage(ctx, &db.Message{
		AgentID: a.ID,
		Role:    "user",
		Content: userMessage,
		Source:  "cli",
		Trust:   "trusted",
	}); err != nil {
		return "", fmt.Errorf("save user message: %w", err)
	}

	// Build message list: system prompt + skill summary + recent history
	systemPrompt := a.Workspace.SystemPrompt()
	if a.Skills != nil {
		if summary := a.Skills.Summary(); summary != "" {
			systemPrompt += "\n\n---\n\n" + summary
		}
	}
	if a.Services != nil {
		if section := a.Services.SystemPromptSection(); section != "" {
			systemPrompt += "\n\n---\n\n" + section
		}
	}
	messages := []chatMessage{
		{Role: "system", Content: systemPrompt},
	}

	// Get tool definitions (needed for context budget calculation)
	var toolDefs []tools.OpenAITool
	toolDefsTokens := 0
	if len(a.Tools.All()) > 0 {
		toolDefs = a.Tools.OpenAITools()
		// Rough estimate: ~100 tokens per tool definition
		toolDefsTokens = len(toolDefs) * 100
	}

	history, err := a.Store.GetMessages(ctx, a.ID, 200)
	if err != nil {
		return "", fmt.Errorf("load history: %w", err)
	}
	var historyMsgs []chatMessage
	for _, m := range history {
		historyMsgs = append(historyMsgs, chatMessage{Role: m.Role, Content: m.Content})
	}

	// Trim history to fit within context window
	historyMsgs = a.trimHistory(systemPrompt, historyMsgs, toolDefsTokens)
	messages = append(messages, historyMsgs...)

	// Agent loop — keep going until we get a text response (no more tool calls)
	const maxIterations = 20
	ld := newLoopDetector()
	for i := 0; i < maxIterations; i++ {
		response, err := a.LLM.SendChat(ctx, a.Model, messages, toolDefs, cb.OnDelta)
		if err != nil {
			return "", err
		}

		// Track cost across all spending windows
		if w := a.trackCost(response); w != nil {
			if cb.OnPaused != nil {
				cb.OnPaused(w.formatWindow(), w.remaining())
			}
			return "", ErrTokenLimitReached
		}

		// If the response has tool calls, execute them and loop
		if len(response.ToolCalls) > 0 {
			// Add assistant message with tool calls to history
			messages = append(messages, chatMessage{
				Role:      "assistant",
				ToolCalls: response.ToolCalls,
			})

			// Execute each tool call
			loopDetected := false
			for _, tc := range response.ToolCalls {
				if cb.OnToolCall != nil {
					cb.OnToolCall(tc.Function.Name, tc.Function.Arguments)
				}

				if ld.record(tc.Function.Name, tc.Function.Arguments) {
					loopDetected = true
				}

				result, err := a.Tools.Execute(ctx, tc.Function.Name, json.RawMessage(tc.Function.Arguments), cb.Approve)
				if err != nil {
					result = fmt.Sprintf("Error: %v", err)
				}

				if cb.OnToolResult != nil {
					cb.OnToolResult(tc.Function.Name, result)
				}

				// Audit the tool call
				a.Store.LogAudit(ctx, &db.AuditEntry{
					Actor:      "agent:" + a.ID,
					Action:     "tool_call",
					Target:     tc.Function.Name,
					Details:    tc.Function.Arguments,
					TrustLevel: "trusted",
				})

				// Add tool result to messages
				messages = append(messages, chatMessage{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID,
				})
			}

			// If loop detected, inject a nudge to break the cycle
			if loopDetected {
				a.Store.LogAudit(ctx, &db.AuditEntry{
					Actor:      "agent:" + a.ID,
					Action:     "loop_detected",
					Details:    "repeated tool calls detected, injecting correction",
					TrustLevel: "system",
				})
				messages = append(messages, chatMessage{
					Role:    "user",
					Content: "[system] You are repeating the same tool call. Stop and provide a final text response to the user. If you are stuck, explain what went wrong.",
				})
			}

			continue // Loop back to send tool results to LLM
		}

		// No tool calls — we have the final text response
		finalText := response.Content

		// Save assistant response
		if err := a.Store.AppendMessage(ctx, &db.Message{
			AgentID: a.ID,
			Role:    "assistant",
			Content: finalText,
			Source:  "llm",
			Trust:   "trusted",
		}); err != nil {
			return "", fmt.Errorf("save assistant message: %w", err)
		}

		a.Store.LogAudit(ctx, &db.AuditEntry{
			Actor:      "agent:" + a.ID,
			Action:     "llm_chat",
			TrustLevel: "trusted",
		})

		return finalText, nil
	}

	return "", fmt.Errorf("agent loop exceeded %d iterations", maxIterations)
}

// llmResponse is the parsed response from the LLM.
type llmResponse struct {
	Content      string
	ToolCalls    []toolCall
	PromptTokens int // from API usage field, 0 if not available
	OutputTokens int // from API usage field, 0 if not available
	CachedTokens int // cached input tokens (cheaper), 0 if not available
}

// trackCost records spending across all cost windows.
// Returns the first exceeded window, or nil.
func (a *Agent) trackCost(resp *llmResponse) *CostWindow {
	if len(a.CostWindows) == 0 {
		return nil
	}

	inputTokens := resp.PromptTokens
	outputTokens := resp.OutputTokens
	cachedTokens := resp.CachedTokens

	// If API didn't return usage, estimate from response content
	if inputTokens == 0 && outputTokens == 0 {
		outputTokens = estimateTokens(resp.Content)
		for _, tc := range resp.ToolCalls {
			outputTokens += estimateTokens(tc.Function.Arguments)
		}
	}

	// Subtract cached from input (they're counted separately)
	if cachedTokens > 0 && inputTokens >= cachedTokens {
		inputTokens -= cachedTokens
	}

	cost := calculateCost(a.Pricing, inputTokens, outputTokens, cachedTokens)
	return recordCostWindows(a.CostWindows, cost)
}

