package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"smithly.dev/internal/db"
	"smithly.dev/internal/db/sqlite"
	"smithly.dev/internal/db/storetest"
)

func TestSplitStatements(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "simple statements",
			input: "CREATE TABLE foo (id INTEGER);\nCREATE TABLE bar (id INTEGER);",
			want:  2,
		},
		{
			name: "trigger with inner semicolons",
			input: `CREATE TRIGGER memory_ai AFTER INSERT ON memory
BEGIN
  INSERT INTO memory_fts(rowid, content) VALUES (new.id, new.content);
END;`,
			want: 1,
		},
		{
			name: "trigger followed by another statement",
			input: `CREATE TRIGGER memory_ai AFTER INSERT ON memory
BEGIN
  INSERT INTO memory_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TABLE other (id INTEGER);`,
			want: 2,
		},
		{
			name: "multiple triggers",
			input: `CREATE TRIGGER t1 AFTER INSERT ON memory
BEGIN
  INSERT INTO fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER t2 AFTER DELETE ON memory
BEGIN
  INSERT INTO fts(fts, rowid, content) VALUES ('delete', old.id, old.content);
END;`,
			want: 2,
		},
		{
			name: "comments are skipped",
			input: `-- This is a comment
CREATE TABLE foo (id INTEGER);
-- Another comment
CREATE TABLE bar (id INTEGER);`,
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sqlite.SplitStatements(tt.input)
			if len(got) != tt.want {
				t.Errorf("got %d statements, want %d", len(got), tt.want)
				for i, s := range got {
					t.Logf("  stmt[%d]: %s", i, s)
				}
			}
		})
	}
}

func newTestStore(t *testing.T) db.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSQLiteConformance(t *testing.T) {
	storetest.RunAll(t, newTestStore)
}
