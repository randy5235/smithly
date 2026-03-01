package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"smithly.dev/internal/agent"
	"smithly.dev/internal/db/sqlite"
	"smithly.dev/internal/workspace"
)

// --- helpers ---

func rawTestAgent(t *testing.T, srv *httptest.Server) *agent.Agent {
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

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// blockingServer sends one token then blocks until the request is cancelled.
func blockingServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		data := fmt.Sprintf(`{"choices":[{"delta":{"content":%s}}]}`, jsonStr("thinking..."))
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		<-r.Context().Done()
	}))
}

// quickServer responds immediately with a single token.
func quickServer(response string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		data := fmt.Sprintf(`{"choices":[{"delta":{"content":%s}}]}`, jsonStr(response))
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// gatedServer sends a first token, waits for gate to close, then finishes.
func gatedServer(gate chan struct{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		data := fmt.Sprintf(`{"choices":[{"delta":{"content":%s}}]}`, jsonStr("first "))
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		data = fmt.Sprintf(`{"choices":[{"delta":{"content":%s}}]}`, jsonStr("second"))
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// interruptHelper sets up a CLI with the given server and returns the pieces
// needed to drive a runWithInterrupt integration test.
func interruptHelper(t *testing.T, srv *httptest.Server) (*CLI, chan byte, *bytes.Buffer, func(string)) {
	t.Helper()
	a := rawTestAgent(t, srv)
	var out bytes.Buffer
	cli := &CLI{Agent: a, Output: &out}
	bytesCh := make(chan byte, 32)
	var mu sync.Mutex
	rprint := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprint(&out, s)
	}
	return cli, bytesCh, &out, rprint
}

// --- readRawLine tests ---

func sendBytes(ch chan byte, data []byte) {
	for _, b := range data {
		ch <- b
	}
}

func TestReadRawLineBasicInput(t *testing.T) {
	ch := make(chan byte, 32)
	var out bytes.Buffer
	sendBytes(ch, []byte("hello\r"))

	line, ok := readRawLine(ch, &out)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if line != "hello" {
		t.Errorf("line = %q, want %q", line, "hello")
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("echo = %q, expected 'hello'", out.String())
	}
}

func TestReadRawLineNewline(t *testing.T) {
	ch := make(chan byte, 32)
	var out bytes.Buffer
	sendBytes(ch, []byte("test\n"))

	line, ok := readRawLine(ch, &out)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if line != "test" {
		t.Errorf("line = %q, want %q", line, "test")
	}
}

func TestReadRawLineBackspace(t *testing.T) {
	ch := make(chan byte, 32)
	var out bytes.Buffer
	sendBytes(ch, []byte("helloo\x7f\r"))

	line, ok := readRawLine(ch, &out)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if line != "hello" {
		t.Errorf("line = %q, want %q", line, "hello")
	}
}

func TestReadRawLineBackspaceDelete(t *testing.T) {
	ch := make(chan byte, 32)
	var out bytes.Buffer
	// \b is the other backspace code handled
	sendBytes(ch, []byte("ab\bc\r"))

	line, ok := readRawLine(ch, &out)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if line != "ac" {
		t.Errorf("line = %q, want %q", line, "ac")
	}
}

func TestReadRawLineBackspaceOnEmpty(t *testing.T) {
	ch := make(chan byte, 32)
	var out bytes.Buffer
	sendBytes(ch, []byte("\x7f\x7fhi\r"))

	line, ok := readRawLine(ch, &out)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if line != "hi" {
		t.Errorf("line = %q, want %q", line, "hi")
	}
}

func TestReadRawLineCtrlC(t *testing.T) {
	ch := make(chan byte, 32)
	var out bytes.Buffer
	sendBytes(ch, []byte("hi\x03"))

	line, ok := readRawLine(ch, &out)
	if ok {
		t.Fatal("expected ok=false for Ctrl+C")
	}
	if line != "" {
		t.Errorf("line = %q, want empty", line)
	}
	if !strings.Contains(out.String(), "^C") {
		t.Errorf("expected ^C echo, got %q", out.String())
	}
}

func TestReadRawLineCtrlD(t *testing.T) {
	ch := make(chan byte, 32)
	var out bytes.Buffer
	ch <- 0x04

	line, ok := readRawLine(ch, &out)
	if ok {
		t.Fatal("expected ok=false for Ctrl+D")
	}
	if line != "" {
		t.Errorf("line = %q, want empty", line)
	}
}

func TestReadRawLineESCClearsLine(t *testing.T) {
	ch := make(chan byte, 32)
	var out bytes.Buffer
	sendBytes(ch, []byte("wrong\x1bright\r"))

	line, ok := readRawLine(ch, &out)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if line != "right" {
		t.Errorf("line = %q, want %q", line, "right")
	}
}

func TestReadRawLineEmptyEnter(t *testing.T) {
	ch := make(chan byte, 32)
	var out bytes.Buffer
	ch <- '\r'

	line, ok := readRawLine(ch, &out)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if line != "" {
		t.Errorf("line = %q, want empty", line)
	}
}

func TestReadRawLineChannelClose(t *testing.T) {
	ch := make(chan byte, 32)
	var out bytes.Buffer
	sendBytes(ch, []byte("hi"))
	close(ch)

	line, ok := readRawLine(ch, &out)
	if ok {
		t.Fatal("expected ok=false for channel close")
	}
	if line != "" {
		t.Errorf("line = %q, want empty", line)
	}
}

func TestReadRawLineIgnoresControlChars(t *testing.T) {
	ch := make(chan byte, 32)
	var out bytes.Buffer
	sendBytes(ch, []byte{0x01, 'h', 0x02, 'i', 0x00, '\r'})

	line, ok := readRawLine(ch, &out)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if line != "hi" {
		t.Errorf("line = %q, want %q", line, "hi")
	}
}

// --- Integration tests: runWithInterrupt ---

func TestESCTypeAheadAgentFinishesFirst(t *testing.T) {
	gate := make(chan struct{})
	srv := gatedServer(gate)
	defer srv.Close()

	cli, bytesCh, out, rprint := interruptHelper(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		typeAhead   string
		interrupted bool
	}
	done := make(chan result, 1)
	go func() {
		qCtx, qCancel := context.WithCancel(ctx)
		defer qCancel()
		ta, intr := cli.runWithInterrupt(qCtx, qCancel, bytesCh, "hello", "TestBot", rprint)
		done <- result{ta, intr}
	}()

	// Wait for agent to start streaming
	time.Sleep(200 * time.Millisecond)

	bytesCh <- 0x1b // ESC — enters type-ahead mode

	// Let the agent finish while user is typing
	time.Sleep(50 * time.Millisecond)
	close(gate)
	time.Sleep(50 * time.Millisecond)

	// User types next message + Enter
	sendBytes(bytesCh, []byte("next msg\r"))

	select {
	case r := <-done:
		if r.interrupted {
			t.Error("expected interrupted=false")
		}
		if r.typeAhead != "next msg" {
			t.Errorf("typeAhead = %q, want %q", r.typeAhead, "next msg")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}

	output := out.String()
	if !strings.Contains(output, "you> ") {
		t.Errorf("expected type-ahead prompt: %q", output)
	}
	if strings.Contains(output, "Stop current response?") {
		t.Error("should not show old confirmation prompt")
	}
}

func TestESCTypeAheadUserFinishesFirst(t *testing.T) {
	gate := make(chan struct{})
	srv := gatedServer(gate)
	defer srv.Close()

	cli, bytesCh, out, rprint := interruptHelper(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		typeAhead   string
		interrupted bool
	}
	done := make(chan result, 1)
	go func() {
		qCtx, qCancel := context.WithCancel(ctx)
		defer qCancel()
		ta, intr := cli.runWithInterrupt(qCtx, qCancel, bytesCh, "hello", "TestBot", rprint)
		done <- result{ta, intr}
	}()

	// Wait for agent to start streaming
	time.Sleep(200 * time.Millisecond)

	bytesCh <- 0x1b // ESC — enters type-ahead mode
	time.Sleep(50 * time.Millisecond)

	// User types next message + Enter (agent still running)
	sendBytes(bytesCh, []byte("next msg\r"))

	// Now let the agent finish
	time.Sleep(50 * time.Millisecond)
	close(gate)

	select {
	case r := <-done:
		if r.interrupted {
			t.Error("expected interrupted=false")
		}
		if r.typeAhead != "next msg" {
			t.Errorf("typeAhead = %q, want %q", r.typeAhead, "next msg")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}

	output := out.String()
	if !strings.Contains(output, "you> ") {
		t.Errorf("expected type-ahead prompt: %q", output)
	}
}

func TestInterruptCtrlCImmediate(t *testing.T) {
	srv := blockingServer()
	defer srv.Close()

	cli, bytesCh, out, rprint := interruptHelper(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		typeAhead   string
		interrupted bool
	}
	done := make(chan result, 1)
	go func() {
		qCtx, qCancel := context.WithCancel(ctx)
		defer qCancel()
		ta, intr := cli.runWithInterrupt(qCtx, qCancel, bytesCh, "hello", "TestBot", rprint)
		done <- result{ta, intr}
	}()

	time.Sleep(200 * time.Millisecond)

	bytesCh <- 0x03 // Ctrl+C

	select {
	case r := <-done:
		if !r.interrupted {
			t.Error("expected interrupted=true for Ctrl+C")
		}
		if r.typeAhead != "" {
			t.Errorf("typeAhead = %q, want empty", r.typeAhead)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}

	output := out.String()
	if !strings.Contains(output, "^C") {
		t.Errorf("expected ^C echo: %q", output)
	}
}

func TestInterruptNormalCompletion(t *testing.T) {
	srv := quickServer("All done!")
	defer srv.Close()

	cli, bytesCh, out, rprint := interruptHelper(t, srv)

	qCtx, qCancel := context.WithCancel(context.Background())
	defer qCancel()
	typeAhead, interrupted := cli.runWithInterrupt(qCtx, qCancel, bytesCh, "hello", "TestBot", rprint)

	if interrupted {
		t.Error("expected interrupted=false for normal completion")
	}
	if typeAhead != "" {
		t.Errorf("typeAhead = %q, want empty", typeAhead)
	}
	if !strings.Contains(out.String(), "All done!") {
		t.Errorf("missing response: %q", out.String())
	}
}

func TestInterruptStdinCloseCancels(t *testing.T) {
	srv := blockingServer()
	defer srv.Close()

	cli, bytesCh, _, rprint := interruptHelper(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		typeAhead   string
		interrupted bool
	}
	done := make(chan result, 1)
	go func() {
		qCtx, qCancel := context.WithCancel(ctx)
		defer qCancel()
		ta, intr := cli.runWithInterrupt(qCtx, qCancel, bytesCh, "hello", "TestBot", rprint)
		done <- result{ta, intr}
	}()

	time.Sleep(200 * time.Millisecond)
	close(bytesCh) // simulate stdin EOF

	select {
	case r := <-done:
		if r.interrupted {
			t.Error("expected interrupted=false for stdin close (graceful)")
		}
		if r.typeAhead != "" {
			t.Errorf("typeAhead = %q, want empty", r.typeAhead)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

func TestInterruptIgnoresOtherKeys(t *testing.T) {
	gate := make(chan struct{})
	srv := gatedServer(gate)
	defer srv.Close()

	cli, bytesCh, out, rprint := interruptHelper(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		typeAhead   string
		interrupted bool
	}
	done := make(chan result, 1)
	go func() {
		qCtx, qCancel := context.WithCancel(ctx)
		defer qCancel()
		ta, intr := cli.runWithInterrupt(qCtx, qCancel, bytesCh, "hello", "TestBot", rprint)
		done <- result{ta, intr}
	}()

	time.Sleep(200 * time.Millisecond)

	// Send random keys — should be ignored (no prompt, no interrupt)
	bytesCh <- 'a'
	bytesCh <- 'b'
	bytesCh <- 'c'

	// Let the server finish
	time.Sleep(50 * time.Millisecond)
	close(gate)

	select {
	case r := <-done:
		if r.interrupted {
			t.Error("expected interrupted=false — random keys should be ignored")
		}
		if r.typeAhead != "" {
			t.Errorf("typeAhead = %q, want empty", r.typeAhead)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}

	if strings.Contains(out.String(), "you> ") {
		t.Error("random keys should not trigger type-ahead prompt")
	}
}
