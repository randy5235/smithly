// Package storetest provides a shared conformance test suite for db.Store implementations.
// Any backend (SQLite, Postgres, MongoDB, etc.) imports this package and calls
// RunAll(t, factory) to verify it satisfies the Store contract.
package storetest

import (
	"context"
	"fmt"
	"testing"

	"smithly.dev/internal/db"
)

// Factory creates a fresh, empty, migrated Store for each test.
type Factory func(t *testing.T) db.Store

// RunAll runs the full conformance suite against the given factory.
func RunAll(t *testing.T, factory Factory) {
	t.Helper()

	tests := []struct {
		name string
		fn   func(*testing.T, db.Store)
	}{
		{"CreateAndGetAgent", testCreateAndGetAgent},
		{"ListAgents", testListAgents},
		{"DeleteAgent", testDeleteAgent},
		{"AgentNotFound", testAgentNotFound},
		{"DuplicateAgent", testDuplicateAgent},
		{"AppendAndGetMessages", testAppendAndGetMessages},
		{"GetMessagesLimit", testGetMessagesLimit},
		{"MessagesIsolatedPerAgent", testMessagesIsolatedPerAgent},
		{"MessagesChronologicalOrder", testMessagesChronologicalOrder},
		{"AuditLog", testAuditLog},
		{"AuditFilterByAgent", testAuditFilterByAgent},
		{"AuditFilterByDomain", testAuditFilterByDomain},
		{"DomainSetAndGet", testDomainSetAndGet},
		{"DomainList", testDomainList},
		{"DomainTouch", testDomainTouch},
		{"DomainNotFound", testDomainNotFound},
		{"DomainUpsert", testDomainUpsert},
		{"MigrateIdempotent", testMigrateIdempotent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := factory(t)
			tt.fn(t, store)
		})
	}
}

// --- Agent Tests ---

func testCreateAndGetAgent(t *testing.T, store db.Store) {
	ctx := context.Background()
	agent := &db.Agent{
		ID:            "test-agent",
		Model:         "gpt-4o",
		WorkspacePath: "workspaces/test/",
	}

	if err := store.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	got, err := store.GetAgent(ctx, "test-agent")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.ID != "test-agent" {
		t.Errorf("ID = %q, want %q", got.ID, "test-agent")
	}
	if got.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", got.Model, "gpt-4o")
	}
	if got.WorkspacePath != "workspaces/test/" {
		t.Errorf("WorkspacePath = %q, want %q", got.WorkspacePath, "workspaces/test/")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
}

func testListAgents(t *testing.T, store db.Store) {
	ctx := context.Background()

	agents := []*db.Agent{
		{ID: "a1", Model: "gpt-4o", WorkspacePath: "ws/a1"},
		{ID: "a2", Model: "claude-sonnet", WorkspacePath: "ws/a2"},
		{ID: "a3", Model: "llama3.2", WorkspacePath: "ws/a3"},
	}
	for _, a := range agents {
		if err := store.CreateAgent(ctx, a); err != nil {
			t.Fatalf("CreateAgent %s: %v", a.ID, err)
		}
	}

	list, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
}

func testDeleteAgent(t *testing.T, store db.Store) {
	ctx := context.Background()
	store.CreateAgent(ctx, &db.Agent{ID: "del", Model: "m", WorkspacePath: "w"})

	if err := store.DeleteAgent(ctx, "del"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	_, err := store.GetAgent(ctx, "del")
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func testAgentNotFound(t *testing.T, store db.Store) {
	ctx := context.Background()
	_, err := store.GetAgent(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent, got nil")
	}
}

func testDuplicateAgent(t *testing.T, store db.Store) {
	ctx := context.Background()
	a := &db.Agent{ID: "dup", Model: "m", WorkspacePath: "w"}
	store.CreateAgent(ctx, a)

	err := store.CreateAgent(ctx, a)
	if err == nil {
		t.Fatal("expected error for duplicate agent, got nil")
	}
}

// --- Memory Tests ---

func testAppendAndGetMessages(t *testing.T, store db.Store) {
	ctx := context.Background()
	store.CreateAgent(ctx, &db.Agent{ID: "agent1", Model: "m", WorkspacePath: "w"})

	msgs := []*db.Message{
		{AgentID: "agent1", Role: "user", Content: "hello", Source: "cli", Trust: "trusted"},
		{AgentID: "agent1", Role: "assistant", Content: "hi there", Source: "llm", Trust: "trusted"},
		{AgentID: "agent1", Role: "user", Content: "how are you?", Source: "cli", Trust: "trusted"},
	}
	for _, m := range msgs {
		if err := store.AppendMessage(ctx, m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	got, err := store.GetMessages(ctx, "agent1", 10)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Content != "hello" {
		t.Errorf("first message = %q, want %q", got[0].Content, "hello")
	}
	if got[1].Role != "assistant" {
		t.Errorf("second role = %q, want %q", got[1].Role, "assistant")
	}
	if got[2].Content != "how are you?" {
		t.Errorf("last message = %q, want %q", got[2].Content, "how are you?")
	}
}

func testGetMessagesLimit(t *testing.T, store db.Store) {
	ctx := context.Background()
	store.CreateAgent(ctx, &db.Agent{ID: "agent1", Model: "m", WorkspacePath: "w"})

	for i := 0; i < 10; i++ {
		store.AppendMessage(ctx, &db.Message{
			AgentID: "agent1", Role: "user",
			Content: fmt.Sprintf("msg %d", i),
			Source:  "cli", Trust: "trusted",
		})
	}

	got, err := store.GetMessages(ctx, "agent1", 3)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// Should be the 3 most recent, in chronological order
	if got[0].Content != "msg 7" {
		t.Errorf("first = %q, want %q", got[0].Content, "msg 7")
	}
	if got[2].Content != "msg 9" {
		t.Errorf("last = %q, want %q", got[2].Content, "msg 9")
	}
}

func testMessagesIsolatedPerAgent(t *testing.T, store db.Store) {
	ctx := context.Background()
	store.CreateAgent(ctx, &db.Agent{ID: "a1", Model: "m", WorkspacePath: "w"})
	store.CreateAgent(ctx, &db.Agent{ID: "a2", Model: "m", WorkspacePath: "w"})

	store.AppendMessage(ctx, &db.Message{AgentID: "a1", Role: "user", Content: "for a1", Source: "cli", Trust: "trusted"})
	store.AppendMessage(ctx, &db.Message{AgentID: "a1", Role: "user", Content: "also a1", Source: "cli", Trust: "trusted"})
	store.AppendMessage(ctx, &db.Message{AgentID: "a2", Role: "user", Content: "for a2", Source: "cli", Trust: "trusted"})

	msgs1, _ := store.GetMessages(ctx, "a1", 10)
	if len(msgs1) != 2 {
		t.Fatalf("a1 messages = %d, want 2", len(msgs1))
	}

	msgs2, _ := store.GetMessages(ctx, "a2", 10)
	if len(msgs2) != 1 {
		t.Fatalf("a2 messages = %d, want 1", len(msgs2))
	}
	if msgs2[0].Content != "for a2" {
		t.Errorf("a2 content = %q, want %q", msgs2[0].Content, "for a2")
	}
}

func testMessagesChronologicalOrder(t *testing.T, store db.Store) {
	ctx := context.Background()
	store.CreateAgent(ctx, &db.Agent{ID: "agent1", Model: "m", WorkspacePath: "w"})

	for i := 0; i < 5; i++ {
		store.AppendMessage(ctx, &db.Message{
			AgentID: "agent1", Role: "user",
			Content: fmt.Sprintf("msg %d", i),
			Source:  "cli", Trust: "trusted",
		})
	}

	got, _ := store.GetMessages(ctx, "agent1", 100)
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Errorf("messages not in order: id %d <= %d", got[i].ID, got[i-1].ID)
		}
	}
}

// --- Audit Tests ---

func testAuditLog(t *testing.T, store db.Store) {
	ctx := context.Background()

	entries := []*db.AuditEntry{
		{Actor: "agent:bot1", Action: "llm_chat", TrustLevel: "trusted"},
		{Actor: "skill:weather", Action: "http_get", TrustLevel: "trusted", Domain: "api.weather.com"},
		{Actor: "agent:bot1", Action: "llm_chat", TrustLevel: "trusted"},
	}
	for _, e := range entries {
		if err := store.LogAudit(ctx, e); err != nil {
			t.Fatalf("LogAudit: %v", err)
		}
	}

	all, err := store.GetAuditLog(ctx, db.AuditQuery{Limit: 10})
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len = %d, want 3", len(all))
	}
}

func testAuditFilterByAgent(t *testing.T, store db.Store) {
	ctx := context.Background()

	store.LogAudit(ctx, &db.AuditEntry{Actor: "agent:bot1", Action: "llm_chat", TrustLevel: "trusted"})
	store.LogAudit(ctx, &db.AuditEntry{Actor: "agent:bot2", Action: "llm_chat", TrustLevel: "trusted"})
	store.LogAudit(ctx, &db.AuditEntry{Actor: "agent:bot1", Action: "tool_call", TrustLevel: "trusted"})

	got, err := store.GetAuditLog(ctx, db.AuditQuery{AgentID: "bot1", Limit: 10})
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("filtered len = %d, want 2", len(got))
	}
}

func testAuditFilterByDomain(t *testing.T, store db.Store) {
	ctx := context.Background()

	store.LogAudit(ctx, &db.AuditEntry{Actor: "skill:web", Action: "http_get", TrustLevel: "trusted", Domain: "api.example.com"})
	store.LogAudit(ctx, &db.AuditEntry{Actor: "skill:web", Action: "http_get", TrustLevel: "trusted", Domain: "api.other.com"})

	got, err := store.GetAuditLog(ctx, db.AuditQuery{Domain: "api.example.com", Limit: 10})
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("domain filtered len = %d, want 1", len(got))
	}
}

// --- Domain Tests ---

func testDomainSetAndGet(t *testing.T, store db.Store) {
	ctx := context.Background()

	entry := &db.DomainEntry{
		Domain:    "api.example.com",
		Status:    "allow",
		GrantedBy: "user",
	}
	if err := store.SetDomain(ctx, entry); err != nil {
		t.Fatalf("SetDomain: %v", err)
	}

	got, err := store.GetDomain(ctx, "api.example.com")
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if got.Domain != "api.example.com" {
		t.Errorf("Domain = %q, want %q", got.Domain, "api.example.com")
	}
	if got.Status != "allow" {
		t.Errorf("Status = %q, want %q", got.Status, "allow")
	}
	if got.GrantedBy != "user" {
		t.Errorf("GrantedBy = %q, want %q", got.GrantedBy, "user")
	}
	if got.AccessCount != 0 {
		t.Errorf("AccessCount = %d, want 0", got.AccessCount)
	}
}

func testDomainList(t *testing.T, store db.Store) {
	ctx := context.Background()

	domains := []*db.DomainEntry{
		{Domain: "api.a.com", Status: "allow", GrantedBy: "user"},
		{Domain: "api.b.com", Status: "deny", GrantedBy: "user"},
		{Domain: "api.c.com", Status: "allow", GrantedBy: "skill:web"},
	}
	for _, d := range domains {
		if err := store.SetDomain(ctx, d); err != nil {
			t.Fatalf("SetDomain %s: %v", d.Domain, err)
		}
	}

	list, err := store.ListDomains(ctx)
	if err != nil {
		t.Fatalf("ListDomains: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	// Should be ordered alphabetically
	if list[0].Domain != "api.a.com" {
		t.Errorf("first = %q, want %q", list[0].Domain, "api.a.com")
	}
	if list[1].Status != "deny" {
		t.Errorf("second status = %q, want %q", list[1].Status, "deny")
	}
}

func testDomainTouch(t *testing.T, store db.Store) {
	ctx := context.Background()

	store.SetDomain(ctx, &db.DomainEntry{Domain: "api.touch.com", Status: "allow", GrantedBy: "user"})

	// Touch twice
	if err := store.TouchDomain(ctx, "api.touch.com"); err != nil {
		t.Fatalf("TouchDomain: %v", err)
	}
	if err := store.TouchDomain(ctx, "api.touch.com"); err != nil {
		t.Fatalf("TouchDomain: %v", err)
	}

	got, err := store.GetDomain(ctx, "api.touch.com")
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if got.AccessCount != 2 {
		t.Errorf("AccessCount = %d, want 2", got.AccessCount)
	}
	if got.LastAccessed.IsZero() {
		t.Error("LastAccessed should not be zero after touch")
	}
}

func testDomainNotFound(t *testing.T, store db.Store) {
	ctx := context.Background()
	_, err := store.GetDomain(ctx, "nonexistent.com")
	if err == nil {
		t.Fatal("expected error for nonexistent domain, got nil")
	}
}

func testDomainUpsert(t *testing.T, store db.Store) {
	ctx := context.Background()

	// Set as allow
	store.SetDomain(ctx, &db.DomainEntry{Domain: "api.upsert.com", Status: "allow", GrantedBy: "skill:web"})

	// Upsert to deny
	store.SetDomain(ctx, &db.DomainEntry{Domain: "api.upsert.com", Status: "deny", GrantedBy: "user"})

	got, err := store.GetDomain(ctx, "api.upsert.com")
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if got.Status != "deny" {
		t.Errorf("Status = %q, want %q after upsert", got.Status, "deny")
	}
	if got.GrantedBy != "user" {
		t.Errorf("GrantedBy = %q, want %q after upsert", got.GrantedBy, "user")
	}
}

// --- Migration Tests ---

func testMigrateIdempotent(t *testing.T, store db.Store) {
	ctx := context.Background()
	// Running Migrate again should be a no-op
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	// And a third time
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("third Migrate: %v", err)
	}
}
