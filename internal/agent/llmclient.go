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

// LLMClient abstracts the wire format for sending chat requests to an LLM API.
// Each implementation speaks a different API format (Chat Completions, Responses, etc.)
// while the agent loop works with the same normalized types.
type LLMClient interface {
	SendChat(ctx context.Context, model string, messages []chatMessage,
		tools []tools.OpenAITool, onDelta func(string)) (*llmResponse, error)
}

// ChatCompletionsClient speaks the OpenAI Chat Completions API format (/chat/completions).
// Works with OpenAI, Gemini, Ollama, OpenRouter, and other compatible endpoints.
type ChatCompletionsClient struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func (c *ChatCompletionsClient) SendChat(ctx context.Context, model string, messages []chatMessage, toolDefs []tools.OpenAITool, onDelta func(string)) (*llmResponse, error) {
	reqBody := chatRequest{
		Model:    model,
		Messages: messages,
		Tools:    toolDefs,
		Stream:   onDelta != nil,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := c.BaseURL + "/chat/completions"
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
		return readStream(resp.Body, onDelta)
	}
	return readFull(resp.Body)
}

func readStream(body io.Reader, onDelta func(string)) (*llmResponse, error) {
	scanner := bufio.NewScanner(body)
	// Increase scanner buffer for large responses
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var contentBuf strings.Builder
	toolCallMap := make(map[int]*toolCall)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		// Stream text content
		if delta.Content != "" {
			contentBuf.WriteString(delta.Content)
			onDelta(delta.Content)
		}

		// Accumulate tool calls
		for _, tc := range delta.ToolCalls {
			existing, ok := toolCallMap[tc.Index]
			if !ok {
				toolCallMap[tc.Index] = &toolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: functionCall{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			} else {
				// Append streamed arguments
				if tc.Function.Arguments != "" {
					existing.Function.Arguments += tc.Function.Arguments
				}
			}
		}
	}

	resp := &llmResponse{Content: contentBuf.String()}
	for i := 0; i < len(toolCallMap); i++ {
		if tc, ok := toolCallMap[i]; ok {
			resp.ToolCalls = append(resp.ToolCalls, *tc)
		}
	}

	return resp, scanner.Err()
}

func readFull(body io.Reader) (*llmResponse, error) {
	var apiResp struct {
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []toolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			CacheReadTokens  int `json:"cache_read_input_tokens"` // Anthropic
			PromptDetails    *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"` // OpenAI (object)
		} `json:"usage"`
	}
	if err := json.NewDecoder(body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode llm response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("llm returned no choices")
	}

	cachedTokens := apiResp.Usage.CacheReadTokens // Anthropic
	if apiResp.Usage.PromptDetails != nil {
		cachedTokens += apiResp.Usage.PromptDetails.CachedTokens // OpenAI
	}

	return &llmResponse{
		Content:      apiResp.Choices[0].Message.Content,
		ToolCalls:    apiResp.Choices[0].Message.ToolCalls,
		PromptTokens: apiResp.Usage.PromptTokens,
		OutputTokens: apiResp.Usage.CompletionTokens,
		CachedTokens: cachedTokens,
	}, nil
}

// newLLMClient creates the appropriate LLMClient for the given provider.
func newLLMClient(provider, baseURL, apiKey string, client *http.Client) LLMClient {
	switch provider {
	case "openai-responses":
		return &ResponsesClient{
			BaseURL: baseURL,
			APIKey:  apiKey,
			Client:  client,
		}
	default:
		// "", "openai", "gemini", "ollama", "openrouter", "anthropic" — all use Chat Completions
		return &ChatCompletionsClient{
			BaseURL: baseURL,
			APIKey:  apiKey,
			Client:  client,
		}
	}
}

// NewLLMClientForModel creates an LLMClient, auto-detecting the Responses API
// for codex models when provider is "openai".
func NewLLMClientForModel(provider, model, baseURL, apiKey string, client *http.Client) LLMClient {
	// Codex models only work with the Responses API
	if isResponsesOnly(provider, model) {
		return &ResponsesClient{
			BaseURL: baseURL,
			APIKey:  apiKey,
			Client:  client,
		}
	}
	return newLLMClient(provider, baseURL, apiKey, client)
}

// isResponsesOnly returns true if the model requires the Responses API.
func isResponsesOnly(provider, model string) bool {
	if provider == "openai-responses" {
		return true
	}
	// OpenAI Codex models are Responses-only
	if provider == "" || provider == "openai" {
		return strings.HasSuffix(model, "-codex")
	}
	return false
}
