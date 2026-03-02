package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"smithly.dev/internal/agent"
	"smithly.dev/internal/db"
	"smithly.dev/internal/db/sqlite"
	"smithly.dev/internal/gateway"
	"smithly.dev/internal/workspace"
)

func testStore(t *testing.T) db.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	store.Migrate(context.Background())
	t.Cleanup(func() { store.Close() })
	return store
}

func testGateway(t *testing.T) (*gateway.Gateway, db.Store) {
	t.Helper()
	store := testStore(t)
	gw := gateway.New("127.0.0.1", 0, "test-token", 60, store)
	return gw, store
}

func TestHealthEndpoint(t *testing.T) {
	gw, _ := testGateway(t)
	handler := gw.Handler()

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("health status = %d, want 200", w.Code)
	}

	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
}

func TestAuthRequired(t *testing.T) {
	gw, _ := testGateway(t)
	handler := gw.Handler()

	// No auth
	req := httptest.NewRequest("GET", "/agents", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no auth = %d, want 401", w.Code)
	}

	// Wrong token
	req = httptest.NewRequest("GET", "/agents", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("wrong token = %d, want 403", w.Code)
	}

	// Correct token
	req = httptest.NewRequest("GET", "/agents", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("correct token = %d, want 200", w.Code)
	}
}

func TestListAgents(t *testing.T) {
	gw, store := testGateway(t)

	ws := &workspace.Workspace{
		Identity: workspace.Identity{Name: "Test Bot"},
	}
	a := agent.New(agent.Config{ID: "bot1", Model: "gpt-4o", Workspace: ws, Store: store})
	gw.RegisterAgent(a)

	handler := gw.Handler()
	req := httptest.NewRequest("GET", "/agents", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var agents []map[string]string
	json.NewDecoder(w.Body).Decode(&agents)
	if len(agents) != 1 {
		t.Fatalf("agents len = %d, want 1", len(agents))
	}
	if agents[0]["id"] != "bot1" {
		t.Errorf("id = %q, want %q", agents[0]["id"], "bot1")
	}
}

func TestChatAgentNotFound(t *testing.T) {
	gw, _ := testGateway(t)
	handler := gw.Handler()

	body := bytes.NewBufferString(`{"message":"hi"}`)
	req := httptest.NewRequest("POST", "/agents/nonexistent/chat", body)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestChatEndpoint(t *testing.T) {
	// Mock LLM server
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "Hello from the gateway!"}},
			},
		})
	}))
	defer llm.Close()

	gw, store := testGateway(t)
	ws := &workspace.Workspace{
		Identity: workspace.Identity{Name: "GatewayBot"},
	}
	a := agent.New(agent.Config{
		ID: "gw-bot", Model: "test-model",
		BaseURL: llm.URL, APIKey: "key",
		Workspace: ws, Store: store, Client: llm.Client(),
	})
	gw.RegisterAgent(a)

	handler := gw.Handler()

	// Valid chat request
	body := bytes.NewBufferString(`{"message":"hello"}`)
	req := httptest.NewRequest("POST", "/agents/gw-bot/chat", body)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["response"] != "Hello from the gateway!" {
		t.Errorf("response = %q", resp["response"])
	}
}

func TestChatBadRequest(t *testing.T) {
	// Mock LLM server (not actually called)
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("LLM should not be called for bad request")
	}))
	defer llm.Close()

	gw, store := testGateway(t)
	ws := &workspace.Workspace{
		Identity: workspace.Identity{Name: "Bot"},
	}
	a := agent.New(agent.Config{
		ID: "bot", Model: "model",
		BaseURL: llm.URL, APIKey: "key",
		Workspace: ws, Store: store, Client: llm.Client(),
	})
	gw.RegisterAgent(a)

	handler := gw.Handler()

	// Invalid JSON body
	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest("POST", "/agents/bot/chat", body)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestRateLimiting(t *testing.T) {
	gw, store := testGateway(t)

	// Register an agent so we have a valid endpoint to hit
	ws := &workspace.Workspace{
		Identity: workspace.Identity{Name: "Bot"},
	}
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "ok"}},
			},
		})
	}))
	defer llm.Close()
	a := agent.New(agent.Config{
		ID: "bot", Model: "model",
		BaseURL: llm.URL, APIKey: "key",
		Workspace: ws, Store: store, Client: llm.Client(),
	})
	gw.RegisterAgent(a)

	handler := gw.Handler()

	// Make requests up to the limit — should all succeed
	for i := range 60 {
		req := httptest.NewRequest("GET", "/agents", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, w.Code)
		}
	}

	// Next request should be rate limited
	req := httptest.NewRequest("GET", "/agents", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("over-limit status = %d, want 429", w.Code)
	}
}

func TestChatNoAuth(t *testing.T) {
	gw, _ := testGateway(t)
	handler := gw.Handler()

	body := bytes.NewBufferString(`{"message":"hi"}`)
	req := httptest.NewRequest("POST", "/agents/bot/chat", body)
	// No auth header
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestChatSSEStreaming(t *testing.T) {
	// Mock LLM that returns a streaming response when stream=true.
	callCount := 0
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var reqBody struct {
			Stream bool `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&reqBody)

		if reqBody.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			for _, tok := range []string{"Hello", " from", " SSE"} {
				fmt.Fprintf(w, "data: %s\n\n", mustJSON(t, map[string]any{
					"choices": []map[string]any{
						{"delta": map[string]any{"content": tok}},
					},
				}))
				flusher.Flush()
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		} else {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"content": "Hello from SSE"}},
				},
			})
		}
	}))
	defer llm.Close()

	gw, store := testGateway(t)
	ws := &workspace.Workspace{Identity: workspace.Identity{Name: "SSEBot"}}
	a := agent.New(agent.Config{
		ID: "sse-bot", Model: "test-model",
		BaseURL: llm.URL, APIKey: "key",
		Workspace: ws, Store: store, Client: llm.Client(),
	})
	gw.RegisterAgent(a)

	handler := gw.Handler()

	body := bytes.NewBufferString(`{"message":"hello"}`)
	req := httptest.NewRequest("POST", "/agents/sse-bot/chat", body)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// Parse SSE events from the response body.
	events := parseSSE(t, w.Body.String())

	// Expect delta events followed by a done event.
	var deltas []string
	var doneResponse string
	for _, ev := range events {
		switch ev.event {
		case "delta":
			var d map[string]string
			json.Unmarshal([]byte(ev.data), &d)
			deltas = append(deltas, d["token"])
		case "done":
			var d map[string]string
			json.Unmarshal([]byte(ev.data), &d)
			doneResponse = d["response"]
		}
	}

	if len(deltas) != 3 {
		t.Errorf("got %d delta events, want 3: %v", len(deltas), deltas)
	}
	if doneResponse != "Hello from SSE" {
		t.Errorf("done response = %q, want %q", doneResponse, "Hello from SSE")
	}
}

func TestChatSSEWithToolCalls(t *testing.T) {
	callCount := 0
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++

		// The agent sends stream:true when OnDelta is set, so we must
		// respond in SSE format for the streaming client to parse it.
		var reqBody struct {
			Stream bool `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&reqBody)

		if reqBody.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			if callCount == 1 {
				// Stream a tool call
				fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"echo_tool","arguments":"{\"text\":\"hello\"}"}}]}}]}`+"\n\n")
				flusher.Flush()
				fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
			} else {
				fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Tool said: echoed: hello"}}]}`+"\n\n")
				flusher.Flush()
				fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
			}
		} else {
			w.Header().Set("Content-Type", "application/json")
			if callCount == 1 {
				json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{
						{"message": map[string]any{
							"tool_calls": []map[string]any{
								{
									"id":   "call_1",
									"type": "function",
									"function": map[string]string{
										"name":      "echo_tool",
										"arguments": `{"text":"hello"}`,
									},
								},
							},
						}},
					},
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{
						{"message": map[string]any{"content": "Tool said: echoed: hello"}},
					},
				})
			}
		}
	}))
	defer llm.Close()

	gw, store := testGateway(t)
	ws := &workspace.Workspace{Identity: workspace.Identity{Name: "ToolBot"}}
	a := agent.New(agent.Config{
		ID: "tool-bot", Model: "test-model",
		BaseURL: llm.URL, APIKey: "key",
		Workspace: ws, Store: store, Client: llm.Client(),
	})
	a.Tools.Register(&echoTool{})
	gw.RegisterAgent(a)

	handler := gw.Handler()
	body := bytes.NewBufferString(`{"message":"use tool"}`)
	req := httptest.NewRequest("POST", "/agents/tool-bot/chat", body)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	events := parseSSE(t, w.Body.String())

	var hasToolCall, hasToolResult, hasDone bool
	for _, ev := range events {
		switch ev.event {
		case "tool_call":
			hasToolCall = true
			var d map[string]string
			json.Unmarshal([]byte(ev.data), &d)
			if d["name"] != "echo_tool" {
				t.Errorf("tool_call name = %q", d["name"])
			}
		case "tool_result":
			hasToolResult = true
			var d map[string]string
			json.Unmarshal([]byte(ev.data), &d)
			if d["name"] != "echo_tool" {
				t.Errorf("tool_result name = %q", d["name"])
			}
			if d["result"] != "echoed: hello" {
				t.Errorf("tool_result result = %q", d["result"])
			}
		case "done":
			hasDone = true
		}
	}

	if !hasToolCall {
		t.Error("missing tool_call event")
	}
	if !hasToolResult {
		t.Error("missing tool_result event")
	}
	if !hasDone {
		t.Error("missing done event")
	}
}

func TestChatErrorDoesNotLeakDetails(t *testing.T) {
	// LLM returns 500 with internal details.
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"internal db connection pool exhausted at /opt/llm/db.go:42"}}`, http.StatusInternalServerError)
	}))
	defer llm.Close()

	gw, store := testGateway(t)
	ws := &workspace.Workspace{Identity: workspace.Identity{Name: "Bot"}}
	a := agent.New(agent.Config{
		ID: "bot", Model: "model",
		BaseURL: llm.URL, APIKey: "key",
		Workspace: ws, Store: store, Client: llm.Client(),
	})
	gw.RegisterAgent(a)

	handler := gw.Handler()
	body := bytes.NewBufferString(`{"message":"hello"}`)
	req := httptest.NewRequest("POST", "/agents/bot/chat", body)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}

	// Must NOT contain the actual error details.
	respBody := w.Body.String()
	if strings.Contains(respBody, "db connection pool") || strings.Contains(respBody, "db.go") {
		t.Errorf("error response leaks internal details: %s", respBody)
	}
	if !strings.Contains(respBody, "internal error") {
		t.Errorf("expected generic error message, got: %s", respBody)
	}
}

func TestChatSSEErrorDoesNotLeakDetails(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"secret internal path"}}`, http.StatusInternalServerError)
	}))
	defer llm.Close()

	gw, store := testGateway(t)
	ws := &workspace.Workspace{Identity: workspace.Identity{Name: "Bot"}}
	a := agent.New(agent.Config{
		ID: "bot", Model: "model",
		BaseURL: llm.URL, APIKey: "key",
		Workspace: ws, Store: store, Client: llm.Client(),
	})
	gw.RegisterAgent(a)

	handler := gw.Handler()
	body := bytes.NewBufferString(`{"message":"hello"}`)
	req := httptest.NewRequest("POST", "/agents/bot/chat", body)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	events := parseSSE(t, w.Body.String())
	for _, ev := range events {
		if ev.event == "error" {
			if strings.Contains(ev.data, "secret") {
				t.Errorf("SSE error event leaks details: %s", ev.data)
			}
		}
	}
}

// --- SSE test helpers ---

type sseEvent struct {
	event string
	data  string
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	scanner := bufio.NewScanner(strings.NewReader(body))
	var currentEvent, currentData string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			currentData = strings.TrimPrefix(line, "data: ")
		case line == "":
			if currentEvent != "" || currentData != "" {
				events = append(events, sseEvent{event: currentEvent, data: currentData})
				currentEvent = ""
				currentData = ""
			}
		}
	}
	return events
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --- Test tools (same as agent_test.go) ---

type echoTool struct{}

func (e *echoTool) Name() string        { return "echo_tool" }
func (e *echoTool) Description() string { return "Echoes back text" }
func (e *echoTool) NeedsApproval() bool { return false }
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
