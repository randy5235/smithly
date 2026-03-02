package sidecar

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"smithly.dev/internal/db/sqlite"
	"smithly.dev/internal/store"

	_ "modernc.org/sqlite"
)

// testSecrets is a simple in-memory secret store for tests.
type testSecrets map[string]string

func (s testSecrets) GetSecret(name string) (string, bool) {
	v, ok := s[name]
	return v, ok
}

// testNotifier records calls to Send.
type testNotifier struct {
	calls []notifyCall
}

type notifyCall struct {
	title    string
	message  string
	priority int
}

func (n *testNotifier) Name() string { return "test" }
func (n *testNotifier) Send(_ context.Context, title, message string, priority int) error {
	n.calls = append(n.calls, notifyCall{title, message, priority})
	return nil
}

func setupSidecar(t *testing.T) (*Sidecar, *testNotifier) {
	t.Helper()

	// Audit store (agent runtime DB)
	sqliteStore, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteStore.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqliteStore.Close() })

	// Object store (separate DB)
	objDB, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	objDB.SetMaxOpenConns(1)
	t.Cleanup(func() { objDB.Close() })

	objStore := store.NewSQLite(objDB)
	if err := objStore.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}

	notifier := &testNotifier{}
	secrets := testSecrets{
		"my_api_key": "sk-secret-123",
		"db_pass":    "hunter2",
	}

	sc := New(Config{
		Bind:     "127.0.0.1",
		Port:     18791,
		Notify:   notifier,
		Audit:    sqliteStore,
		ObjStore: objStore,
		Secrets:  secrets,
	})

	return sc, notifier
}

func doReq(t *testing.T, handler http.Handler, method, path, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestHealthNoAuth(t *testing.T) {
	sc, _ := setupSidecar(t)
	w := doReq(t, sc.Handler(), "GET", "/health", "", "")
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRequireTokenMissing(t *testing.T) {
	sc, _ := setupSidecar(t)
	w := doReq(t, sc.Handler(), "POST", "/notify", "", `{"title":"x","message":"y"}`)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRequireTokenInvalid(t *testing.T) {
	sc, _ := setupSidecar(t)
	w := doReq(t, sc.Handler(), "POST", "/notify", "bad-token", `{"title":"x","message":"y"}`)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRequireTokenExpired(t *testing.T) {
	sc, _ := setupSidecar(t)
	token := sc.IssueToken("test-skill", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	w := doReq(t, sc.Handler(), "POST", "/notify", token, `{"title":"x","message":"y"}`)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestIssueAndRevokeToken(t *testing.T) {
	sc, _ := setupSidecar(t)
	token := sc.IssueToken("test-skill", 5*time.Minute)

	// Valid token works
	w := doReq(t, sc.Handler(), "POST", "/notify", token, `{"title":"x","message":"y"}`)
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}

	// Revoke
	sc.RevokeToken(token)

	// Now fails
	w = doReq(t, sc.Handler(), "POST", "/notify", token, `{"title":"x","message":"y"}`)
	if w.Code != 401 {
		t.Errorf("after revoke: status = %d, want 401", w.Code)
	}
}

func TestNotifyEndpoint(t *testing.T) {
	sc, notifier := setupSidecar(t)
	token := sc.IssueToken("test-skill", 5*time.Minute)

	w := doReq(t, sc.Handler(), "POST", "/notify", token,
		`{"title":"Alert","message":"Something happened","priority":4}`)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 notify call, got %d", len(notifier.calls))
	}
	if notifier.calls[0].title != "Alert" {
		t.Errorf("title = %q, want %q", notifier.calls[0].title, "Alert")
	}
	if notifier.calls[0].priority != 4 {
		t.Errorf("priority = %d, want 4", notifier.calls[0].priority)
	}
}

func TestAuditEndpoint(t *testing.T) {
	sc, _ := setupSidecar(t)
	token := sc.IssueToken("gmail", 5*time.Minute)

	w := doReq(t, sc.Handler(), "POST", "/audit", token,
		`{"action":"send_email","target":"bob@example.com","details":"sent invoice"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestSecretEndpoint(t *testing.T) {
	sc, _ := setupSidecar(t)
	token := sc.IssueToken("test-skill", 5*time.Minute)

	w := doReq(t, sc.Handler(), "GET", "/secrets/my_api_key", token, "")
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["value"] != "sk-secret-123" {
		t.Errorf("secret value = %q, want %q", resp["value"], "sk-secret-123")
	}
}

func TestSecretNotFound(t *testing.T) {
	sc, _ := setupSidecar(t)
	token := sc.IssueToken("test-skill", 5*time.Minute)

	w := doReq(t, sc.Handler(), "GET", "/secrets/nonexistent", token, "")
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestStorePutAndGet(t *testing.T) {
	sc, _ := setupSidecar(t)
	token := sc.IssueToken("gmail", 5*time.Minute)

	// Put
	w := doReq(t, sc.Handler(), "POST", "/store/put", token,
		`{"type":"email","data":{"subject":"hello"},"public":false}`)
	if w.Code != 200 {
		t.Fatalf("put status = %d, body = %s", w.Code, w.Body.String())
	}

	var putResp store.Object
	json.NewDecoder(w.Body).Decode(&putResp)
	if putResp.Version != 1 {
		t.Errorf("version = %d, want 1", putResp.Version)
	}
	if putResp.Skill != "gmail" {
		t.Errorf("skill = %q, want %q", putResp.Skill, "gmail")
	}

	// Get
	w = doReq(t, sc.Handler(), "POST", "/store/get", token,
		`{"id":"`+putResp.ID+`"}`)
	if w.Code != 200 {
		t.Fatalf("get status = %d, body = %s", w.Code, w.Body.String())
	}

	var getResp store.Object
	json.NewDecoder(w.Body).Decode(&getResp)
	if getResp.ID != putResp.ID {
		t.Errorf("id = %q, want %q", getResp.ID, putResp.ID)
	}
}

func TestStoreSkillScoping(t *testing.T) {
	sc, _ := setupSidecar(t)

	// gmail puts a private object
	gmailToken := sc.IssueToken("gmail", 5*time.Minute)
	w := doReq(t, sc.Handler(), "POST", "/store/put", gmailToken,
		`{"id":"private-email","type":"email","data":{"x":1},"public":false}`)
	if w.Code != 200 {
		t.Fatalf("put status = %d", w.Code)
	}

	// other-skill tries to get it — should fail
	otherToken := sc.IssueToken("other-skill", 5*time.Minute)
	w = doReq(t, sc.Handler(), "POST", "/store/get", otherToken,
		`{"id":"private-email"}`)
	if w.Code != 404 {
		t.Errorf("other-skill get private = %d, want 404", w.Code)
	}

	// gmail puts a public object
	w = doReq(t, sc.Handler(), "POST", "/store/put", gmailToken,
		`{"id":"public-email","type":"email","data":{"x":2},"public":true}`)
	if w.Code != 200 {
		t.Fatalf("put public status = %d", w.Code)
	}

	// other-skill can read public
	w = doReq(t, sc.Handler(), "POST", "/store/get", otherToken, `{"id":"public-email"}`)
	if w.Code != 200 {
		t.Errorf("other-skill get public = %d, want 200", w.Code)
	}
}

func TestStoreDeleteEndpoint(t *testing.T) {
	sc, _ := setupSidecar(t)
	token := sc.IssueToken("gmail", 5*time.Minute)

	doReq(t, sc.Handler(), "POST", "/store/put", token,
		`{"id":"del-me","type":"email","data":{}}`)

	w := doReq(t, sc.Handler(), "POST", "/store/delete", token, `{"id":"del-me"}`)
	if w.Code != 200 {
		t.Fatalf("delete status = %d, body = %s", w.Code, w.Body.String())
	}

	// Query should exclude deleted
	w = doReq(t, sc.Handler(), "POST", "/store/query", token, `{"type":"email"}`)
	var results []store.Object
	json.NewDecoder(w.Body).Decode(&results)
	for _, r := range results {
		if r.ID == "del-me" {
			t.Error("deleted object should not appear in query")
		}
	}
}

func TestStoreQueryEndpoint(t *testing.T) {
	sc, _ := setupSidecar(t)
	token := sc.IssueToken("gmail", 5*time.Minute)

	doReq(t, sc.Handler(), "POST", "/store/put", token,
		`{"id":"e1","type":"email","data":{"from":"alice"}}`)
	doReq(t, sc.Handler(), "POST", "/store/put", token,
		`{"id":"c1","type":"contact","data":{"name":"bob"}}`)

	w := doReq(t, sc.Handler(), "POST", "/store/query", token, `{"type":"email"}`)
	if w.Code != 200 {
		t.Fatalf("query status = %d", w.Code)
	}

	var results []store.Object
	json.NewDecoder(w.Body).Decode(&results)
	if len(results) != 1 {
		t.Errorf("got %d results, want 1", len(results))
	}
}

func TestStoreHistoryEndpoint(t *testing.T) {
	sc, _ := setupSidecar(t)
	token := sc.IssueToken("gmail", 5*time.Minute)

	doReq(t, sc.Handler(), "POST", "/store/put", token,
		`{"id":"h1","type":"email","data":{"v":1}}`)
	doReq(t, sc.Handler(), "POST", "/store/put", token,
		`{"id":"h1","type":"email","data":{"v":2}}`)

	w := doReq(t, sc.Handler(), "POST", "/store/history", token, `{"id":"h1"}`)
	if w.Code != 200 {
		t.Fatalf("history status = %d", w.Code)
	}

	var results []store.Object
	json.NewDecoder(w.Body).Decode(&results)
	if len(results) != 2 {
		t.Fatalf("got %d versions, want 2", len(results))
	}
	if results[0].Version != 1 || results[1].Version != 2 {
		t.Errorf("versions = [%d, %d], want [1, 2]", results[0].Version, results[1].Version)
	}
}

func TestURL(t *testing.T) {
	sc := New(Config{Bind: "127.0.0.1", Port: 18791})
	if sc.URL() != "http://127.0.0.1:18791" {
		t.Errorf("URL = %q", sc.URL())
	}
}
