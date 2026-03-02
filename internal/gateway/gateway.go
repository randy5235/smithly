// Package gateway implements the HTTP API server.
// It binds to localhost only with bearer token auth.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"smithly.dev/internal/agent"
	"smithly.dev/internal/db"
)

// Gateway is the HTTP server that exposes the agent API.
type Gateway struct {
	Bind    string
	Port    int
	Token   string
	Store   db.Store
	agents  map[string]*agent.Agent
	mu      sync.RWMutex
	server  *http.Server
	limiter *rateLimiter
}

// New creates a new gateway.
func New(bind string, port int, token string, rateLimit int, store db.Store) *Gateway {
	gw := &Gateway{
		Bind:   bind,
		Port:   port,
		Token:  token,
		Store:  store,
		agents: make(map[string]*agent.Agent),
	}
	if rateLimit > 0 {
		gw.limiter = newRateLimiter(time.Minute, rateLimit)
	}
	return gw
}

// RegisterAgent adds an agent to the gateway.
func (g *Gateway) RegisterAgent(a *agent.Agent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.agents[a.ID] = a
}

// GetAgent returns an agent by ID.
func (g *Gateway) GetAgent(id string) (*agent.Agent, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	a, ok := g.agents[id]
	return a, ok
}

// Handler returns the HTTP handler with all routes registered.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", g.handleHealth)
	mux.HandleFunc("GET /agents", g.rateLimit(g.requireAuth(g.handleListAgents)))
	mux.HandleFunc("POST /agents/{id}/chat", g.rateLimit(g.requireAuth(g.handleChat)))
	return mux
}

func (g *Gateway) rateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if g.limiter != nil {
			ip := extractIP(r)
			if !g.limiter.allow(ip) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
		}
		next(w, r)
	}
}

// Start begins serving HTTP on the configured address.
func (g *Gateway) Start() error {
	addr := fmt.Sprintf("%s:%d", g.Bind, g.Port)
	g.server = &http.Server{
		Addr:         addr,
		Handler:      g.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	slog.Info("gateway listening", "addr", addr)
	return g.server.Serve(ln)
}

// Shutdown gracefully stops the server.
func (g *Gateway) Shutdown(ctx context.Context) error {
	if g.server != nil {
		return g.server.Shutdown(ctx)
	}
	return nil
}

func (g *Gateway) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if token != g.Token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		slog.Error("health response write failed", "err", err)
	}
}

func (g *Gateway) handleListAgents(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	type agentInfo struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Paused bool   `json:"paused"`
	}
	agents := []agentInfo{}
	for _, a := range g.agents {
		agents = append(agents, agentInfo{
			ID:     a.ID,
			Model:  a.Model,
			Paused: a.Paused(),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(agents); err != nil {
		slog.Error("list agents response write failed", "err", err)
	}
}

// chatTimeout is the per-request write deadline for the chat endpoint.
// Agent loops can run many LLM round-trips, so this must be much longer
// than the server-level WriteTimeout (which covers regular endpoints).
const chatTimeout = 5 * time.Minute

func (g *Gateway) handleChat(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")

	a, ok := g.GetAgent(agentID)
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Detect whether the client wants SSE streaming.
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		g.handleChatSSE(w, r, a, req.Message)
		return
	}

	// Non-streaming JSON response. Extend the write deadline so the agent
	// loop has enough time to complete multiple LLM round-trips.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(chatTimeout)); err != nil {
		slog.Warn("could not extend write deadline", "err", err)
	}

	response, err := a.Chat(r.Context(), req.Message, &agent.Callbacks{Source: "api"})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"response": response}); err != nil {
		slog.Error("chat response write failed", "err", err)
	}
}

// handleChatSSE streams agent events as Server-Sent Events.
//
// Event types:
//
//	delta         – streamed token of assistant text  {"token":"..."}
//	tool_call     – agent invoked a tool              {"name":"...","args":"..."}
//	tool_result   – tool returned a result            {"name":"...","result":"..."}
//	done          – final response                    {"response":"..."}
//	error         – agent error                       {"error":"..."}
func (g *Gateway) handleChatSSE(w http.ResponseWriter, r *http.Request, a *agent.Agent, message string) {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(chatTimeout)); err != nil {
		slog.Warn("could not extend write deadline for SSE", "err", err)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func(event string, data any) {
		payload, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
		rc.Flush()
	}

	cb := &agent.Callbacks{
		Source: "api",
		OnDelta: func(token string) {
			send("delta", map[string]string{"token": token})
		},
		OnToolCall: func(name, args string) {
			send("tool_call", map[string]string{"name": name, "args": args})
		},
		OnToolResult: func(name, result string) {
			send("tool_result", map[string]string{"name": name, "result": result})
		},
	}

	response, err := a.Chat(r.Context(), message, cb)
	if err != nil {
		send("error", map[string]string{"error": "internal error"})
		return
	}

	send("done", map[string]string{"response": response})
}
