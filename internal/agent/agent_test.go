package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"smithly.dev/internal/agent"
	"smithly.dev/internal/db"
	"smithly.dev/internal/db/sqlite"
	"smithly.dev/internal/tools"
	"smithly.dev/internal/workspace"
)

// mockLLM is a test HTTP server that returns canned OpenAI-compatible responses.
type mockLLM struct {
	responses []mockResponse // responses are consumed in order
	calls     int            // how many requests we've received
	requests  []mockRequest  // captured requests
}

type mockResponse struct {
	content   string     // text content
	toolCalls []mockTool // tool calls (if any)
	status    int        // HTTP status (0 = 200)
}

type mockTool struct {
	id   string
	name string
	args string
}

type mockRequest struct {
	Model    string
	Messages []json.RawMessage
}

func (m *mockLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m.calls >= len(m.responses) {
		http.Error(w, "no more mock responses", http.StatusInternalServerError)
		return
	}

	// Capture request
	var reqBody struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
	}
	json.NewDecoder(r.Body).Decode(&reqBody)
	m.requests = append(m.requests, mockRequest{
		Model:    reqBody.Model,
		Messages: reqBody.Messages,
	})

	resp := m.responses[m.calls]
	m.calls++

	if resp.status != 0 {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"test error %d"}}`, resp.status), resp.status)
		return
	}

	// Build OpenAI response
	type tcResp struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}

	var toolCalls []tcResp
	for _, tc := range resp.toolCalls {
		toolCalls = append(toolCalls, tcResp{
			ID:   tc.id,
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: tc.name, Arguments: tc.args},
		})
	}

	apiResp := map[string]any{
		"choices": []map[string]any{
			{
				"message": map[string]any{
					"content":    resp.content,
					"tool_calls": toolCalls,
				},
			},
		},
	}
	// If no tool calls, omit the field (some models do this)
	if len(toolCalls) == 0 {
		apiResp["choices"] = []map[string]any{
			{
				"message": map[string]any{
					"content": resp.content,
				},
			},
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apiResp)
}

func newTestAgent(t *testing.T, srv *httptest.Server) *agent.Agent {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Load from empty dir gives default system prompt
	ws, _ := workspace.Load("")
	a := agent.NewWithClient("test-agent", "test-model", "", srv.URL, "test-key", ws, store, srv.Client())
	return a
}

func TestChatBasicResponse(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			{content: "Hello! How can I help you?"},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	result, err := a.Chat(context.Background(), "hi", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "Hello! How can I help you?" {
		t.Errorf("result = %q", result)
	}

	// Verify model was sent correctly
	if mock.requests[0].Model != "test-model" {
		t.Errorf("model = %q", mock.requests[0].Model)
	}
}

func TestChatWithToolCall(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			// First response: LLM wants to call a tool
			{toolCalls: []mockTool{
				{id: "call_1", name: "echo_tool", args: `{"text":"test"}`},
			}},
			// Second response: LLM gives final answer after tool result
			{content: "The tool said: echoed: test"},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)

	// Register a simple test tool
	a.Tools.Register(&echoTool{})

	var toolCallName, toolResult string
	cb := &agent.Callbacks{
		OnToolCall:   func(name, args string) { toolCallName = name },
		OnToolResult: func(name, result string) { toolResult = result },
	}

	result, err := a.Chat(context.Background(), "please echo test", cb)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "The tool said: echoed: test" {
		t.Errorf("result = %q", result)
	}
	if toolCallName != "echo_tool" {
		t.Errorf("tool call name = %q", toolCallName)
	}
	if toolResult != "echoed: test" {
		t.Errorf("tool result = %q", toolResult)
	}

	// Should have made 2 LLM requests (initial + after tool result)
	if mock.calls != 2 {
		t.Errorf("LLM calls = %d, want 2", mock.calls)
	}
}

func TestChatToolNeedsApprovalDenied(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			{toolCalls: []mockTool{
				{id: "call_1", name: "dangerous_tool", args: `{}`},
			}},
			{content: "OK, I won't do that."},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	a.Tools.Register(&dangerousTool{})

	cb := &agent.Callbacks{
		Approve: func(name, desc string) bool { return false }, // deny all
	}

	result, err := a.Chat(context.Background(), "do dangerous thing", cb)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// Agent should still get a response (the LLM responds to the denial)
	if result != "OK, I won't do that." {
		t.Errorf("result = %q", result)
	}
}

func TestChatToolNeedsApprovalApproved(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			{toolCalls: []mockTool{
				{id: "call_1", name: "dangerous_tool", args: `{}`},
			}},
			{content: "Done!"},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	a.Tools.Register(&dangerousTool{})

	cb := &agent.Callbacks{
		Approve: func(name, desc string) bool { return true }, // approve
	}

	var toolResult string
	cb.OnToolResult = func(name, result string) { toolResult = result }

	result, err := a.Chat(context.Background(), "do dangerous thing", cb)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "Done!" {
		t.Errorf("result = %q", result)
	}
	if toolResult != "danger executed" {
		t.Errorf("tool result = %q, expected 'danger executed'", toolResult)
	}
}

func TestChatMessagePersistence(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			{content: "First reply"},
			{content: "Second reply"},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)

	// First chat
	_, err := a.Chat(context.Background(), "hello", nil)
	if err != nil {
		t.Fatalf("first Chat: %v", err)
	}

	// Second chat
	_, err = a.Chat(context.Background(), "how are you", nil)
	if err != nil {
		t.Fatalf("second Chat: %v", err)
	}

	// Verify messages were persisted (2 user + 2 assistant = 4)
	msgs, err := a.Store.GetMessages(context.Background(), "test-agent", 50)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("msg[0] = %q/%q", msgs[0].Role, msgs[0].Content)
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "First reply" {
		t.Errorf("msg[1] = %q/%q", msgs[1].Role, msgs[1].Content)
	}

	// Verify second request included history
	if len(mock.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(mock.requests))
	}
	// Second request should have system + 3 messages (user, assistant, user)
	if len(mock.requests[1].Messages) != 4 { // system + hello + First reply + how are you
		t.Errorf("second request messages = %d, want 4", len(mock.requests[1].Messages))
	}
}

func TestChatAuditLogging(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			{toolCalls: []mockTool{
				{id: "call_1", name: "echo_tool", args: `{"text":"audit me"}`},
			}},
			{content: "done"},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	a.Tools.Register(&echoTool{})

	_, err := a.Chat(context.Background(), "test audit", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// Should have audit entries for tool call and llm_chat
	entries, err := a.Store.GetAuditLog(context.Background(), db.AuditQuery{})
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}

	var hasToolCall, hasLLMChat bool
	for _, e := range entries {
		if e.Action == "tool_call" && e.Target == "echo_tool" {
			hasToolCall = true
		}
		if e.Action == "llm_chat" {
			hasLLMChat = true
		}
	}
	if !hasToolCall {
		t.Error("missing tool_call audit entry")
	}
	if !hasLLMChat {
		t.Error("missing llm_chat audit entry")
	}
}

func TestChatLLMError(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			{status: 401},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	_, err := a.Chat(context.Background(), "hello", nil)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %q, expected to contain 401", err.Error())
	}
}

func TestChatLLMRateLimit(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			{status: 429},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	_, err := a.Chat(context.Background(), "hello", nil)
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error = %q, expected to contain 429", err.Error())
	}
}

func TestChatStreamingResponse(t *testing.T) {
	// Use a custom handler for streaming
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		if !req.Stream {
			t.Error("expected streaming request")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{"Hello", " world", "!"}
		for _, chunk := range chunks {
			data := fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	a := newTestAgent(t, srv)

	var deltas []string
	cb := &agent.Callbacks{
		OnDelta: func(token string) {
			deltas = append(deltas, token)
		},
	}

	result, err := a.Chat(context.Background(), "stream test", cb)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "Hello world!" {
		t.Errorf("result = %q", result)
	}
	if len(deltas) != 3 {
		t.Errorf("deltas = %v, want 3 chunks", deltas)
	}
}

func TestChatStreamingToolCalls(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		if callCount == 1 {
			// Stream a tool call in chunks
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"echo_tool","arguments":""}}]}}]}`+"\n\n")
			flusher.Flush()
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"text\""}}]}}]}`+"\n\n")
			flusher.Flush()
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"hello\"}"}}]}}]}`+"\n\n")
			flusher.Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		} else {
			// Return text response
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Got it: echoed: hello"}}]}`+"\n\n")
			flusher.Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()

	a := newTestAgent(t, srv)
	a.Tools.Register(&echoTool{})

	cb := &agent.Callbacks{
		OnDelta: func(token string) {}, // enable streaming
	}

	result, err := a.Chat(context.Background(), "stream tools", cb)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "Got it: echoed: hello" {
		t.Errorf("result = %q", result)
	}
}

func TestChatMultipleToolCalls(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			{toolCalls: []mockTool{
				{id: "call_1", name: "echo_tool", args: `{"text":"first"}`},
				{id: "call_2", name: "echo_tool", args: `{"text":"second"}`},
			}},
			{content: "Both done"},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	a.Tools.Register(&echoTool{})

	var toolCalls []string
	cb := &agent.Callbacks{
		OnToolCall: func(name, args string) { toolCalls = append(toolCalls, name) },
	}

	result, err := a.Chat(context.Background(), "do both", cb)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "Both done" {
		t.Errorf("result = %q", result)
	}
	if len(toolCalls) != 2 {
		t.Errorf("tool calls = %d, want 2", len(toolCalls))
	}
}

func TestChatUnknownToolError(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			{toolCalls: []mockTool{
				{id: "call_1", name: "nonexistent_tool", args: `{}`},
			}},
			{content: "I see it failed"},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)

	// The agent should handle the error gracefully and feed it back to the LLM
	result, err := a.Chat(context.Background(), "use missing tool", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "I see it failed" {
		t.Errorf("result = %q", result)
	}
}

func TestChatCostWindowPauses(t *testing.T) {
	// Each response is ~100 output tokens (400 chars / 4)
	// At Sonnet pricing ($15/1M output), 100 tokens = $0.0015
	// Set limit to $0.001 so first response exceeds it
	mock := &mockLLM{
		responses: []mockResponse{
			{content: strings.Repeat("a", 400)}, // ~100 output tokens = ~$0.0015
			{content: "should not reach this"},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	a.Pricing = agent.LookupPricing("claude-sonnet-4-6-20250514")
	a.CostWindows = []*agent.CostWindow{{LimitCents: 0.001, Window: time.Hour}}

	// First chat should trigger pause (output cost exceeds $0.001)
	_, err := a.Chat(context.Background(), "hello", nil)
	if err != agent.ErrTokenLimitReached {
		t.Errorf("expected ErrTokenLimitReached, got %v", err)
	}
	if !a.Paused() {
		t.Error("agent should be paused")
	}
}

func TestChatCostWindowCallsOnPaused(t *testing.T) {
	mock := &mockLLM{
		responses: []mockResponse{
			{content: strings.Repeat("x", 400)}, // ~100 output tokens
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	a.Pricing = agent.LookupPricing("claude-sonnet-4-6-20250514")
	a.CostWindows = []*agent.CostWindow{{LimitCents: 0.0001, Window: time.Hour}}

	var pausedCalled bool
	var pausedWindow string
	cb := &agent.Callbacks{
		OnPaused: func(window string, remaining time.Duration) {
			pausedCalled = true
			pausedWindow = window
		},
	}

	_, err := a.Chat(context.Background(), "trigger limit", cb)
	if err != agent.ErrTokenLimitReached {
		t.Fatalf("expected ErrTokenLimitReached, got %v", err)
	}
	if !pausedCalled {
		t.Error("OnPaused callback was not called")
	}
	if pausedWindow != "1 hour" {
		t.Errorf("window = %q, want '1 hour'", pausedWindow)
	}
}

func TestChatLoopDetection(t *testing.T) {
	// Simulate LLM stuck in a loop: calls same tool 3 times, then gets nudged and responds
	mock := &mockLLM{
		responses: []mockResponse{
			// Iteration 1-3: same tool call
			{toolCalls: []mockTool{{id: "call_1", name: "echo_tool", args: `{"text":"stuck"}`}}},
			{toolCalls: []mockTool{{id: "call_2", name: "echo_tool", args: `{"text":"stuck"}`}}},
			{toolCalls: []mockTool{{id: "call_3", name: "echo_tool", args: `{"text":"stuck"}`}}},
			// After nudge, LLM gives a text response
			{content: "Sorry, I was stuck in a loop."},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)
	a.Tools.Register(&echoTool{})

	result, err := a.Chat(context.Background(), "do something", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "Sorry, I was stuck in a loop." {
		t.Errorf("result = %q, want loop recovery message", result)
	}

	// Verify loop was audited
	entries, _ := a.Store.GetAuditLog(context.Background(), db.AuditQuery{})
	found := false
	for _, e := range entries {
		if e.Action == "loop_detected" {
			found = true
			break
		}
	}
	if !found {
		t.Error("loop_detected audit entry not found")
	}
}

func TestChatCompactionTriggered(t *testing.T) {
	// Set up: agent with a tiny context window so compaction triggers easily
	compactionCallCount := 0
	mock := &mockLLM{
		responses: []mockResponse{
			// Compaction summary response
			{content: "Summary: user discussed topics A, B, C."},
			// Actual chat response
			{content: "Here's my answer."},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compactionCallCount++
		mock.ServeHTTP(w, r)
	}))
	defer srv.Close()

	a := newTestAgent(t, srv)
	a.MaxContext = 500 // very small context to force compaction

	// Seed enough history to exceed 60% of 500 tokens (300 tokens = ~1200 chars)
	for i := 0; i < 20; i++ {
		a.Store.AppendMessage(context.Background(), &db.Message{
			AgentID: "test-agent",
			Role:    "user",
			Content: fmt.Sprintf("This is message number %d with some padding text to take up space.", i),
			Source:  "cli",
			Trust:   "trusted",
		})
		a.Store.AppendMessage(context.Background(), &db.Message{
			AgentID: "test-agent",
			Role:    "assistant",
			Content: fmt.Sprintf("Response to message %d with additional context and information.", i),
			Source:  "llm",
			Trust:   "trusted",
		})
	}

	result, err := a.Chat(context.Background(), "one more question", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "Here's my answer." {
		t.Errorf("result = %q, want 'Here's my answer.'", result)
	}

	// Should have made 2 LLM calls: one for compaction, one for the actual chat
	if compactionCallCount != 2 {
		t.Errorf("LLM calls = %d, want 2 (compaction + chat)", compactionCallCount)
	}

	// Verify summary was stored in DB
	msgs, err := a.Store.GetMessages(context.Background(), "test-agent", 200)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}

	foundSummary := false
	for _, m := range msgs {
		if m.Source == "summary" && m.Role == "system" {
			foundSummary = true
			if m.Content != "Summary: user discussed topics A, B, C." {
				t.Errorf("summary content = %q", m.Content)
			}
			break
		}
	}
	if !foundSummary {
		t.Error("summary message not found in DB")
	}
}

func TestChatNoCompactionWhenUnderBudget(t *testing.T) {
	// With default 128k context, a few messages should NOT trigger compaction
	mock := &mockLLM{
		responses: []mockResponse{
			{content: "Simple reply."},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)

	result, err := a.Chat(context.Background(), "hello", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "Simple reply." {
		t.Errorf("result = %q", result)
	}

	// Only 1 LLM call (no compaction call)
	if mock.calls != 1 {
		t.Errorf("LLM calls = %d, want 1 (no compaction)", mock.calls)
	}
}

func TestChatSummaryBoundary(t *testing.T) {
	// When a summary exists in history, only messages from the summary forward
	// should be included in context.
	mock := &mockLLM{
		responses: []mockResponse{
			{content: "I know about topic A from the summary."},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)

	// Seed old messages
	a.Store.AppendMessage(context.Background(), &db.Message{
		AgentID: "test-agent", Role: "user", Content: "old message about topic X",
		Source: "cli", Trust: "trusted",
	})
	a.Store.AppendMessage(context.Background(), &db.Message{
		AgentID: "test-agent", Role: "assistant", Content: "response about topic X",
		Source: "llm", Trust: "trusted",
	})

	// Insert a summary
	a.Store.InsertSummary(context.Background(), "test-agent", "Summary: discussed topic X.")

	// Add a recent message
	a.Store.AppendMessage(context.Background(), &db.Message{
		AgentID: "test-agent", Role: "user", Content: "follow up on topic X",
		Source: "cli", Trust: "trusted",
	})

	result, err := a.Chat(context.Background(), "continue", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "I know about topic A from the summary." {
		t.Errorf("result = %q", result)
	}

	// Verify the LLM request doesn't include the old messages before summary
	if len(mock.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(mock.requests))
	}
	// Messages should be: system prompt, summary, "follow up on topic X", "continue" (user msg from Chat())
	// The "old message about topic X" and "response about topic X" should NOT be included
	reqMsgs := mock.requests[0].Messages
	for _, raw := range reqMsgs {
		var msg struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		json.Unmarshal(raw, &msg)
		if strings.Contains(msg.Content, "old message about topic X") {
			t.Error("old message before summary boundary should not be in context")
		}
	}
}

func TestSearchHistoryTool(t *testing.T) {
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	store.CreateAgent(context.Background(), &db.Agent{
		ID: "agent1", Model: "m", WorkspacePath: "w",
	})
	store.AppendMessage(context.Background(), &db.Message{
		AgentID: "agent1", Role: "user", Content: "tell me about elephants",
		Source: "cli", Trust: "trusted",
	})
	store.AppendMessage(context.Background(), &db.Message{
		AgentID: "agent1", Role: "assistant", Content: "Elephants are large mammals",
		Source: "llm", Trust: "trusted",
	})

	// Use the search_history tool via the tools package
	tool := tools.NewSearchHistory(store, "agent1")
	result, err := tool.Run(context.Background(), json.RawMessage(`{"query":"elephants"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result, "elephants") {
		t.Errorf("result should contain 'elephants': %q", result)
	}
	if !strings.Contains(result, "2 message(s)") {
		t.Errorf("expected 2 matches: %q", result)
	}

	// No results
	result, err = tool.Run(context.Background(), json.RawMessage(`{"query":"nonexistent"}`))
	if err != nil {
		t.Fatalf("Run no match: %v", err)
	}
	if !strings.Contains(result, "No messages found") {
		t.Errorf("expected no results message: %q", result)
	}
}

// --- Test tools ---

type echoTool struct{}

func (e *echoTool) Name() string                  { return "echo_tool" }
func (e *echoTool) Description() string            { return "Echoes back text" }
func (e *echoTool) NeedsApproval() bool            { return false }
func (e *echoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
}
func (e *echoTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Text string `json:"text"`
	}
	json.Unmarshal(args, &params)
	return "echoed: " + params.Text, nil
}

type dangerousTool struct{}

func (d *dangerousTool) Name() string                  { return "dangerous_tool" }
func (d *dangerousTool) Description() string            { return "A dangerous tool that needs approval" }
func (d *dangerousTool) NeedsApproval() bool            { return true }
func (d *dangerousTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (d *dangerousTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	return "danger executed", nil
}
