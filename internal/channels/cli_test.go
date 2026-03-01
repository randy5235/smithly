package channels_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"smithly.dev/internal/agent"
	"smithly.dev/internal/channels"
	"smithly.dev/internal/db/sqlite"
	"smithly.dev/internal/workspace"
)

// mockLLMServer returns a server that handles both streaming and non-streaming requests.
// CLI always uses streaming (provides OnDelta), so this returns SSE format.
func mockLLMServer(responses ...string) *httptest.Server {
	idx := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := "I have no more responses"
		if idx < len(responses) {
			content = responses[idx]
			idx++
		}

		var req struct {
			Stream bool `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			data := fmt.Sprintf(`{"choices":[{"delta":{"content":%s}}]}`, mustJSON(content))
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		} else {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"content": content}},
				},
			})
		}
	}))
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func newTestCLIAgent(t *testing.T, srv *httptest.Server) *agent.Agent {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ws := &workspace.Workspace{
		Identity: workspace.Identity{Name: "TestBot"},
	}
	return agent.NewWithClient("test", "test-model", "", srv.URL, "key", ws, store, srv.Client())
}

func TestCLIExitCommand(t *testing.T) {
	srv := mockLLMServer()
	defer srv.Close()

	a := newTestCLIAgent(t, srv)
	input := strings.NewReader("exit\n")
	output := &bytes.Buffer{}

	cli := &channels.CLI{Agent: a, Input: input, Output: output}
	err := cli.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(output.String(), "Goodbye") {
		t.Errorf("output = %q, expected Goodbye", output.String())
	}
}

func TestCLIQuitCommand(t *testing.T) {
	srv := mockLLMServer()
	defer srv.Close()

	a := newTestCLIAgent(t, srv)
	input := strings.NewReader("quit\n")
	output := &bytes.Buffer{}

	cli := &channels.CLI{Agent: a, Input: input, Output: output}
	err := cli.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(output.String(), "Goodbye") {
		t.Errorf("output = %q, expected Goodbye", output.String())
	}
}

func TestCLIBasicChat(t *testing.T) {
	srv := mockLLMServer("Hello, human!")
	defer srv.Close()

	a := newTestCLIAgent(t, srv)
	input := strings.NewReader("hi there\nexit\n")
	output := &bytes.Buffer{}

	cli := &channels.CLI{Agent: a, Input: input, Output: output}
	err := cli.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	out := output.String()
	if !strings.Contains(out, "you>") {
		t.Errorf("missing prompt: %q", out)
	}
	if !strings.Contains(out, "TestBot>") {
		t.Errorf("missing agent name: %q", out)
	}
	if !strings.Contains(out, "Hello, human!") {
		t.Errorf("missing response content: %q", out)
	}
}

func TestCLIMultipleMessages(t *testing.T) {
	srv := mockLLMServer("First response", "Second response")
	defer srv.Close()

	a := newTestCLIAgent(t, srv)
	input := strings.NewReader("hello\nhow are you\nexit\n")
	output := &bytes.Buffer{}

	cli := &channels.CLI{Agent: a, Input: input, Output: output}
	err := cli.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify messages were persisted
	msgs, err := a.Store.GetMessages(context.Background(), "test", 50)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 4 { // 2 user + 2 assistant
		t.Errorf("messages = %d, want 4", len(msgs))
	}
}

func TestCLIEmptyInput(t *testing.T) {
	srv := mockLLMServer()
	defer srv.Close()

	a := newTestCLIAgent(t, srv)
	// Empty lines should be skipped, no LLM call
	input := strings.NewReader("\n\n\nexit\n")
	output := &bytes.Buffer{}

	cli := &channels.CLI{Agent: a, Input: input, Output: output}
	err := cli.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have no messages persisted
	msgs, _ := a.Store.GetMessages(context.Background(), "test", 50)
	if len(msgs) != 0 {
		t.Errorf("messages = %d, want 0 (empty lines should be skipped)", len(msgs))
	}
}

func TestCLIEOF(t *testing.T) {
	srv := mockLLMServer()
	defer srv.Close()

	a := newTestCLIAgent(t, srv)
	// EOF without exit command
	input := strings.NewReader("")
	output := &bytes.Buffer{}

	cli := &channels.CLI{Agent: a, Input: input, Output: output}
	err := cli.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestCLIToolCallDisplay(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		if callCount == 1 {
			// Return a tool call via SSE
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"echo_tool","arguments":"{\"text\":\"hello\"}"}}]}}]}`+"\n\n")
			flusher.Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		} else {
			// Return text response
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Tool used successfully"}}]}`+"\n\n")
			flusher.Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()

	a := newTestCLIAgent(t, srv)
	a.Tools.Register(&echoTool{})

	input := strings.NewReader("use the tool\nexit\n")
	output := &bytes.Buffer{}

	cli := &channels.CLI{Agent: a, Input: input, Output: output}
	err := cli.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	out := output.String()
	if !strings.Contains(out, "[tool: echo_tool]") {
		t.Errorf("missing tool call display: %q", out)
	}
	if !strings.Contains(out, "echoed: hello") {
		t.Errorf("missing tool result: %q", out)
	}
}

func TestCLIBanner(t *testing.T) {
	srv := mockLLMServer()
	defer srv.Close()

	a := newTestCLIAgent(t, srv)
	a.Tools.Register(&echoTool{})

	input := strings.NewReader("exit\n")
	output := &bytes.Buffer{}

	cli := &channels.CLI{Agent: a, Input: input, Output: output}
	cli.Run(context.Background())

	out := output.String()
	if !strings.Contains(out, "Smithly") {
		t.Errorf("missing banner: %q", out)
	}
	if !strings.Contains(out, "TestBot") {
		t.Errorf("missing agent name in banner: %q", out)
	}
	if !strings.Contains(out, "1 tools") {
		t.Errorf("missing tool count in banner: %q", out)
	}
}

// --- Test tools for CLI tests ---

type echoTool struct{}

func (e *echoTool) Name() string        { return "echo_tool" }
func (e *echoTool) Description() string { return "Echoes text" }
func (e *echoTool) NeedsApproval() bool { return false }
func (e *echoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
}
func (e *echoTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct{ Text string `json:"text"` }
	json.Unmarshal(args, &p)
	return fmt.Sprintf("echoed: %s", p.Text), nil
}
