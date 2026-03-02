// Package sidecar provides a localhost HTTP API for code skills.
// Skills use it to get OAuth2 tokens, send notifications, log audit entries,
// read secrets, and optionally use the versioned object store.
package sidecar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"smithly.dev/internal/db"
	"smithly.dev/internal/store"
	"smithly.dev/internal/tools"
)

// SecretStore provides read-only access to secrets for skills.
type SecretStore interface {
	GetSecret(name string) (string, bool)
}

// Sidecar is the localhost HTTP server that code skills talk to.
type Sidecar struct {
	bind     string
	port     int
	oauth2   *tools.OAuth2Tool
	notify   tools.NotifyProvider
	audit    db.Store
	objStore store.Store
	secrets  SecretStore
	server   *http.Server
	tokens   map[string]tokenInfo
	mu       sync.RWMutex
}

type tokenInfo struct {
	skill   string
	expires time.Time
}

// Config holds sidecar construction options.
type Config struct {
	Bind     string
	Port     int
	OAuth2   *tools.OAuth2Tool
	Notify   tools.NotifyProvider
	Audit    db.Store
	ObjStore store.Store
	Secrets  SecretStore
}

// New creates a new Sidecar.
func New(cfg Config) *Sidecar {
	if cfg.Bind == "" {
		cfg.Bind = "127.0.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = 18791
	}
	return &Sidecar{
		bind:     cfg.Bind,
		port:     cfg.Port,
		oauth2:   cfg.OAuth2,
		notify:   cfg.Notify,
		audit:    cfg.Audit,
		objStore: cfg.ObjStore,
		secrets:  cfg.Secrets,
		tokens:   make(map[string]tokenInfo),
	}
}

// URL returns the sidecar's base URL.
func (s *Sidecar) URL() string {
	return fmt.Sprintf("http://%s:%d", s.bind, s.port)
}

// IssueToken creates a short-lived random token scoped to a skill.
func (s *Sidecar) IssueToken(skill string, ttl time.Duration) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	token := hex.EncodeToString(b)

	s.mu.Lock()
	s.tokens[token] = tokenInfo{
		skill:   skill,
		expires: time.Now().Add(ttl),
	}
	s.mu.Unlock()

	return token
}

// RevokeToken invalidates a token after skill execution completes.
func (s *Sidecar) RevokeToken(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

// Start begins listening. Blocks until the server is shut down.
func (s *Sidecar) Start() error {
	s.server = &http.Server{
		Addr:         net.JoinHostPort(s.bind, fmt.Sprintf("%d", s.port)),
		Handler:      s.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	err := s.server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown gracefully stops the sidecar.
func (s *Sidecar) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

// Handler returns the HTTP handler for the sidecar.
func (s *Sidecar) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /oauth2/{provider}", s.requireToken(s.handleOAuth2))
	mux.HandleFunc("POST /notify", s.requireToken(s.handleNotify))
	mux.HandleFunc("POST /audit", s.requireToken(s.handleAudit))
	mux.HandleFunc("GET /secrets/{name}", s.requireToken(s.handleSecret))

	if s.objStore != nil {
		mux.HandleFunc("POST /store/put", s.requireToken(s.handleStorePut))
		mux.HandleFunc("POST /store/get", s.requireToken(s.handleStoreGet))
		mux.HandleFunc("POST /store/delete", s.requireToken(s.handleStoreDelete))
		mux.HandleFunc("POST /store/query", s.requireToken(s.handleStoreQuery))
		mux.HandleFunc("POST /store/history", s.requireToken(s.handleStoreHistory))
	}

	return mux
}

// --- middleware ---

type contextKey string

const skillKey contextKey = "skill"

func (s *Sidecar) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if token == "" || token == auth {
			jsonError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
			return
		}

		s.mu.RLock()
		info, ok := s.tokens[token]
		s.mu.RUnlock()

		if !ok {
			jsonError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if time.Now().After(info.expires) {
			jsonError(w, http.StatusUnauthorized, "token expired")
			return
		}

		ctx := context.WithValue(r.Context(), skillKey, info.skill)
		next(w, r.WithContext(ctx))
	}
}

func skillFromContext(ctx context.Context) string {
	s, _ := ctx.Value(skillKey).(string)
	return s
}

// --- handlers ---

func (s *Sidecar) handleHealth(w http.ResponseWriter, _ *http.Request) {
	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Sidecar) handleOAuth2(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if s.oauth2 == nil {
		jsonError(w, http.StatusNotFound, "no OAuth2 providers configured")
		return
	}

	token, err := s.oauth2.GetToken(r.Context(), provider)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResp(w, http.StatusOK, map[string]string{"token": "Bearer " + token})
}

func (s *Sidecar) handleNotify(w http.ResponseWriter, r *http.Request) {
	if s.notify == nil {
		jsonError(w, http.StatusNotFound, "no notification provider configured")
		return
	}

	var req struct {
		Title    string `json:"title"`
		Message  string `json:"message"`
		Priority int    `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Priority == 0 {
		req.Priority = 3
	}

	if err := s.notify.Send(r.Context(), req.Title, req.Message, req.Priority); err != nil {
		slog.Error("sidecar notify failed", "err", err)
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Sidecar) handleAudit(w http.ResponseWriter, r *http.Request) {
	if s.audit == nil {
		jsonError(w, http.StatusNotFound, "no audit store configured")
		return
	}

	var req struct {
		Action  string `json:"action"`
		Target  string `json:"target"`
		Details string `json:"details"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	skill := skillFromContext(r.Context())
	entry := &db.AuditEntry{
		Actor:      "skill:" + skill,
		Action:     req.Action,
		Target:     req.Target,
		Details:    req.Details,
		TrustLevel: "trusted",
	}

	if err := s.audit.LogAudit(r.Context(), entry); err != nil {
		slog.Error("sidecar audit failed", "err", err)
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Sidecar) handleSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.secrets == nil {
		jsonError(w, http.StatusNotFound, "no secrets configured")
		return
	}

	value, ok := s.secrets.GetSecret(name)
	if !ok {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("secret %q not found", name))
		return
	}

	jsonResp(w, http.StatusOK, map[string]string{"value": value})
}

// --- store handlers ---

func (s *Sidecar) handleStorePut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Data   json.RawMessage `json:"data"`
		Public bool            `json:"public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	skill := skillFromContext(r.Context())
	obj := &store.Object{
		ID:     req.ID,
		Type:   req.Type,
		Skill:  skill,
		Data:   req.Data,
		Public: req.Public,
	}

	result, err := s.objStore.Put(r.Context(), obj)
	if err != nil {
		slog.Error("sidecar store put failed", "err", err)
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	jsonResp(w, http.StatusOK, result)
}

func (s *Sidecar) handleStoreGet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	obj, err := s.objStore.Get(r.Context(), req.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			jsonError(w, http.StatusNotFound, "not found")
		} else {
			slog.Error("sidecar store get failed", "err", err)
			jsonError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	// Enforce visibility: skill can see its own objects or public ones
	skill := skillFromContext(r.Context())
	if obj.Skill != skill && !obj.Public {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}

	jsonResp(w, http.StatusOK, obj)
}

func (s *Sidecar) handleStoreDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	skill := skillFromContext(r.Context())
	if err := s.objStore.Delete(r.Context(), req.ID, skill); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			jsonError(w, http.StatusNotFound, "not found")
		} else {
			slog.Error("sidecar store delete failed", "err", err)
			jsonError(w, http.StatusBadRequest, "delete failed")
		}
		return
	}

	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Sidecar) handleStoreQuery(w http.ResponseWriter, r *http.Request) {
	var q store.Query
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Enforce skill scoping
	q.Skill = skillFromContext(r.Context())

	results, err := s.objStore.Query(r.Context(), &q)
	if err != nil {
		slog.Error("sidecar store query failed", "err", err)
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if results == nil {
		results = []*store.Object{}
	}

	jsonResp(w, http.StatusOK, results)
}

func (s *Sidecar) handleStoreHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Check visibility on latest version
	latest, err := s.objStore.Get(r.Context(), req.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			jsonError(w, http.StatusNotFound, "not found")
		} else {
			slog.Error("sidecar store history get failed", "err", err)
			jsonError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	skill := skillFromContext(r.Context())
	if latest.Skill != skill && !latest.Public {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}

	history, err := s.objStore.History(r.Context(), req.ID)
	if err != nil {
		slog.Error("sidecar store history failed", "err", err)
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	jsonResp(w, http.StatusOK, history)
}

// --- JSON helpers ---

func jsonResp(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("json response write failed", "err", err)
	}
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	jsonResp(w, status, map[string]string{"error": msg})
}
