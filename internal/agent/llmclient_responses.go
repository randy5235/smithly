package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"smithly.dev/internal/tools"
)

// ResponsesClient speaks the OpenAI Responses API format (/responses).
// Required for models like gpt-5.3-codex that only work with this API.
type ResponsesClient struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

// responsesRequest is the request body for the Responses API.
type responsesRequest struct {
	Model        string          `json:"model"`
	Instructions string          `json:"instructions,omitempty"`
	Input        json.RawMessage `json:"input"`
	Tools        json.RawMessage `json:"tools,omitempty"`
	Stream       bool            `json:"stream"`
}

// responsesInputMessage is a single item in the Responses API input array.
type responsesInputMessage struct {
	Role    string `json:"role,omitempty"`
	Type    string `json:"type,omitempty"`
	Content any    `json:"content,omitempty"`
	// For function_call items (assistant tool calls passed back as context)
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// For function_call_output items (tool results)
	Output string `json:"output,omitempty"`
}

func (c *ResponsesClient) SendChat(ctx context.Context, model string, messages []chatMessage, toolDefs []tools.OpenAITool, onDelta func(string)) (*llmResponse, error) {
	// Extract system prompt from messages, convert rest to Responses API input
	var instructions string
	var input []responsesInputMessage

	for _, m := range messages {
		switch m.Role {
		case "system":
			// System messages become the instructions field
			if s, ok := m.Content.(string); ok {
				if instructions != "" {
					instructions += "\n\n"
				}
				instructions += s
			}
		case "user":
			if s, ok := m.Content.(string); ok {
				input = append(input, responsesInputMessage{
					Role:    "user",
					Content: s,
				})
			}
		case "assistant":
			// Assistant messages with tool calls become function_call items
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					input = append(input, responsesInputMessage{
						Type:      "function_call",
						CallID:    tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					})
				}
			} else {
				if s, ok := m.Content.(string); ok && s != "" {
					input = append(input, responsesInputMessage{
						Role:    "assistant",
						Content: s,
					})
				}
			}
		case "tool":
			// Tool result messages become function_call_output items
			output := ""
			if s, ok := m.Content.(string); ok {
				output = s
			}
			input = append(input, responsesInputMessage{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: output,
			})
		}
	}

	// Convert tool definitions to Responses API flat format
	var toolsJSON json.RawMessage
	if len(toolDefs) > 0 {
		var responsesTools []tools.ResponsesTool
		for _, t := range toolDefs {
			responsesTools = append(responsesTools, tools.ResponsesTool{
				Type:        "function",
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}
		var err error
		toolsJSON, err = json.Marshal(responsesTools)
		if err != nil {
			return nil, fmt.Errorf("marshal tools: %w", err)
		}
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal input: %w", err)
	}

	reqBody := responsesRequest{
		Model:        model,
		Instructions: instructions,
		Input:        inputJSON,
		Tools:        toolsJSON,
		Stream:       onDelta != nil,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := c.BaseURL + "/responses"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llm returned %d: %s", resp.StatusCode, string(errBody))
	}

	if onDelta != nil {
		return c.readStream(resp.Body, onDelta)
	}
	return c.readFull(resp.Body)
}

func (c *ResponsesClient) readFull(body io.Reader) (*llmResponse, error) {
	var apiResp struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			// function_call fields
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode responses api: %w", err)
	}

	result := &llmResponse{
		PromptTokens: apiResp.Usage.InputTokens,
		OutputTokens: apiResp.Usage.OutputTokens,
	}

	for _, item := range apiResp.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "text" {
					result.Content += c.Text
				}
			}
		case "function_call":
			result.ToolCalls = append(result.ToolCalls, toolCall{
				ID:   item.CallID,
				Type: "function",
				Function: functionCall{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		}
	}

	return result, nil
}

func (c *ResponsesClient) readStream(body io.Reader, onDelta func(string)) (*llmResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var contentBuf strings.Builder
	// Track function calls by call_id
	type pendingCall struct {
		callID string
		name   string
		args   strings.Builder
	}
	callsByID := make(map[string]*pendingCall)
	var callOrder []string // preserve order

	var eventType string

	for scanner.Scan() {
		line := scanner.Text()

		// Parse SSE event type
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		switch eventType {
		case "response.output_text.delta":
			var delta struct {
				Delta string `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &delta); err != nil {
				continue
			}
			if delta.Delta != "" {
				contentBuf.WriteString(delta.Delta)
				onDelta(delta.Delta)
			}

		case "response.function_call_arguments.delta":
			var delta struct {
				Delta  string `json:"delta"`
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			}
			if err := json.Unmarshal([]byte(data), &delta); err != nil {
				continue
			}
			pc, ok := callsByID[delta.CallID]
			if !ok {
				pc = &pendingCall{callID: delta.CallID, name: delta.Name}
				callsByID[delta.CallID] = pc
				callOrder = append(callOrder, delta.CallID)
			}
			if delta.Name != "" && pc.name == "" {
				pc.name = delta.Name
			}
			pc.args.WriteString(delta.Delta)

		case "response.completed":
			// Extract usage from the completed event
			var completed struct {
				Response struct {
					Usage struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(data), &completed); err == nil {
				result := &llmResponse{
					Content:      contentBuf.String(),
					PromptTokens: completed.Response.Usage.InputTokens,
					OutputTokens: completed.Response.Usage.OutputTokens,
				}
				for _, id := range callOrder {
					pc := callsByID[id]
					result.ToolCalls = append(result.ToolCalls, toolCall{
						ID:   pc.callID,
						Type: "function",
						Function: functionCall{
							Name:      pc.name,
							Arguments: pc.args.String(),
						},
					})
				}
				return result, nil
			}
		}
	}

	// If we didn't get a response.completed event, assemble from what we have
	result := &llmResponse{Content: contentBuf.String()}
	for _, id := range callOrder {
		pc := callsByID[id]
		result.ToolCalls = append(result.ToolCalls, toolCall{
			ID:   pc.callID,
			Type: "function",
			Function: functionCall{
				Name:      pc.name,
				Arguments: pc.args.String(),
			},
		})
	}

	return result, scanner.Err()
}
