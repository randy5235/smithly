package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func setup(t *testing.T) *SQLite {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	s := NewSQLite(db)
	if err := s.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutCreatesVersion1(t *testing.T) {
	s := setup(t)
	ctx := context.Background()

	obj, err := s.Put(ctx, &Object{
		Type:  "email",
		Skill: "gmail",
		Data:  json.RawMessage(`{"subject":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if obj.Version != 1 {
		t.Errorf("version = %d, want 1", obj.Version)
	}
	if obj.ID == "" {
		t.Error("expected auto-generated ID")
	}
}

func TestPutIncrementsVersion(t *testing.T) {
	s := setup(t)
	ctx := context.Background()

	v1, _ := s.Put(ctx, &Object{
		ID: "obj1", Type: "email", Skill: "gmail",
		Data: json.RawMessage(`{"v":1}`),
	})
	v2, _ := s.Put(ctx, &Object{
		ID: "obj1", Type: "email", Skill: "gmail",
		Data: json.RawMessage(`{"v":2}`),
	})

	if v1.Version != 1 {
		t.Errorf("v1.Version = %d, want 1", v1.Version)
	}
	if v2.Version != 2 {
		t.Errorf("v2.Version = %d, want 2", v2.Version)
	}
}

func TestGetReturnsLatest(t *testing.T) {
	s := setup(t)
	ctx := context.Background()

	s.Put(ctx, &Object{ID: "obj1", Type: "email", Skill: "gmail", Data: json.RawMessage(`{"v":1}`)})
	s.Put(ctx, &Object{ID: "obj1", Type: "email", Skill: "gmail", Data: json.RawMessage(`{"v":2}`)})

	got, err := s.Get(ctx, "obj1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 {
		t.Errorf("version = %d, want 2", got.Version)
	}
	if string(got.Data) != `{"v":2}` {
		t.Errorf("data = %s, want {\"v\":2}", got.Data)
	}
}

func TestGetNotFound(t *testing.T) {
	s := setup(t)
	_, err := s.Get(context.Background(), "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteSoftDeletes(t *testing.T) {
	s := setup(t)
	ctx := context.Background()

	s.Put(ctx, &Object{ID: "obj1", Type: "email", Skill: "gmail", Data: json.RawMessage(`{"x":1}`)})
	if err := s.Delete(ctx, "obj1", "gmail"); err != nil {
		t.Fatal(err)
	}

	// Get still returns the object (latest version is deleted)
	got, err := s.Get(ctx, "obj1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Deleted {
		t.Error("expected deleted=true")
	}

	// Query excludes deleted
	results, err := s.Query(ctx, &Query{Type: "email", Skill: "gmail"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("query returned %d results, want 0 (deleted should be excluded)", len(results))
	}
}

func TestDeleteWrongSkill(t *testing.T) {
	s := setup(t)
	ctx := context.Background()

	s.Put(ctx, &Object{ID: "obj1", Type: "email", Skill: "gmail", Data: json.RawMessage(`{}`)})
	err := s.Delete(ctx, "obj1", "other-skill")
	if err == nil {
		t.Error("expected error when deleting object owned by another skill")
	}
}

func TestHistory(t *testing.T) {
	s := setup(t)
	ctx := context.Background()

	s.Put(ctx, &Object{ID: "obj1", Type: "email", Skill: "gmail", Data: json.RawMessage(`{"v":1}`)})
	s.Put(ctx, &Object{ID: "obj1", Type: "email", Skill: "gmail", Data: json.RawMessage(`{"v":2}`)})
	s.Delete(ctx, "obj1", "gmail")

	history, err := s.History(ctx, "obj1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history length = %d, want 3", len(history))
	}
	// Oldest first
	if history[0].Version != 1 {
		t.Errorf("first version = %d, want 1", history[0].Version)
	}
	if history[2].Version != 3 {
		t.Errorf("last version = %d, want 3", history[2].Version)
	}
	if !history[2].Deleted {
		t.Error("last version should be deleted")
	}
}

func TestQueryByType(t *testing.T) {
	s := setup(t)
	ctx := context.Background()

	s.Put(ctx, &Object{ID: "e1", Type: "email", Skill: "gmail", Data: json.RawMessage(`{}`)})
	s.Put(ctx, &Object{ID: "c1", Type: "contact", Skill: "gmail", Data: json.RawMessage(`{}`)})

	results, err := s.Query(ctx, &Query{Type: "email", Skill: "gmail"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].ID != "e1" {
		t.Errorf("got ID %q, want %q", results[0].ID, "e1")
	}
}

func TestQuerySkillScoping(t *testing.T) {
	s := setup(t)
	ctx := context.Background()

	// gmail skill creates a private and a public object
	s.Put(ctx, &Object{ID: "priv", Type: "email", Skill: "gmail", Data: json.RawMessage(`{}`), Public: false})
	s.Put(ctx, &Object{ID: "pub", Type: "email", Skill: "gmail", Data: json.RawMessage(`{}`), Public: true})

	// other-skill queries: should see only public
	results, err := s.Query(ctx, &Query{Type: "email", Skill: "other-skill"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (only public)", len(results))
	}
	if results[0].ID != "pub" {
		t.Errorf("got ID %q, want %q", results[0].ID, "pub")
	}

	// gmail queries: should see both
	results, err = s.Query(ctx, &Query{Type: "email", Skill: "gmail"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2 (own private + public)", len(results))
	}
}

func TestQueryFilter(t *testing.T) {
	s := setup(t)
	ctx := context.Background()

	s.Put(ctx, &Object{ID: "e1", Type: "email", Skill: "gmail", Data: json.RawMessage(`{"from":"alice@example.com"}`)})
	s.Put(ctx, &Object{ID: "e2", Type: "email", Skill: "gmail", Data: json.RawMessage(`{"from":"bob@example.com"}`)})

	results, err := s.Query(ctx, &Query{
		Type:   "email",
		Skill:  "gmail",
		Filter: map[string]any{"from": "alice@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].ID != "e1" {
		t.Errorf("got ID %q, want %q", results[0].ID, "e1")
	}
}

func TestQueryLimit(t *testing.T) {
	s := setup(t)
	ctx := context.Background()

	for range 10 {
		s.Put(ctx, &Object{Type: "item", Skill: "test", Data: json.RawMessage(`{}`)})
	}

	results, err := s.Query(ctx, &Query{Type: "item", Skill: "test", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("got %d results, want 3", len(results))
	}
}

func TestPutRequiresType(t *testing.T) {
	s := setup(t)
	_, err := s.Put(context.Background(), &Object{Skill: "gmail", Data: json.RawMessage(`{}`)})
	if err == nil {
		t.Error("expected error for missing type")
	}
}

func TestPutRequiresSkill(t *testing.T) {
	s := setup(t)
	_, err := s.Put(context.Background(), &Object{Type: "email", Data: json.RawMessage(`{}`)})
	if err == nil {
		t.Error("expected error for missing skill")
	}
}
