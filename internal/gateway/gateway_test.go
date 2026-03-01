package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	a := agent.New("bot1", "gpt-4o", "", "", "", ws, store)
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
	a := agent.NewWithClient("gw-bot", "test-model", "", llm.URL, "key", ws, store, llm.Client())
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
	a := agent.NewWithClient("bot", "model", "", llm.URL, "key", ws, store, llm.Client())
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
	a := agent.NewWithClient("bot", "model", "", llm.URL, "key", ws, store, llm.Client())
	gw.RegisterAgent(a)

	handler := gw.Handler()

	// Make requests up to the limit — should all succeed
	for i := 0; i < 60; i++ {
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
