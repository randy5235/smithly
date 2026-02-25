package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// SQLite implements Store backed by a SQLite database.
// The store_objects table must already exist (created by migration 002_store.sql).
type SQLite struct {
	db *sql.DB
}

// NewSQLite wraps an existing *sql.DB connection as an object store.
func NewSQLite(db *sql.DB) *SQLite {
	return &SQLite{db: db}
}

func (s *SQLite) Put(ctx context.Context, obj *Object) (*Object, error) {
	if obj.ID == "" {
		obj.ID = generateID()
	}
	if obj.Type == "" {
		return nil, fmt.Errorf("type is required")
	}
	if obj.Skill == "" {
		return nil, fmt.Errorf("skill is required")
	}

	data, err := json.Marshal(obj.Data)
	if err != nil {
		return nil, fmt.Errorf("marshal data: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Get next version
	var version int
	err = tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) + 1 FROM store_objects WHERE id = ?", obj.ID,
	).Scan(&version)
	if err != nil {
		return nil, err
	}

	pub := 0
	if obj.Public {
		pub = 1
	}
	del := 0
	if obj.Deleted {
		del = 1
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO store_objects (id, version, type, skill, data, public, deleted)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		obj.ID, version, obj.Type, obj.Skill, string(data), pub, del,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	result := *obj
	result.Version = version
	result.Data = json.RawMessage(data)
	return &result, nil
}

func (s *SQLite) Get(ctx context.Context, id string) (*Object, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, version, type, skill, data, public, deleted, created_at
		 FROM store_objects WHERE id = ? ORDER BY version DESC LIMIT 1`, id,
	)
	return scanObject(row)
}

func (s *SQLite) Delete(ctx context.Context, id, skill string) error {
	// Get latest to preserve type and skill
	latest, err := s.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("object %q not found: %w", id, err)
	}
	if latest.Skill != skill {
		return fmt.Errorf("skill %q does not own object %q", skill, id)
	}

	_, err = s.Put(ctx, &Object{
		ID:      id,
		Type:    latest.Type,
		Skill:   skill,
		Data:    latest.Data,
		Public:  latest.Public,
		Deleted: true,
	})
	return err
}

func (s *SQLite) Query(ctx context.Context, q *Query) ([]*Object, error) {
	// Latest non-deleted version per ID
	query := `SELECT o.id, o.version, o.type, o.skill, o.data, o.public, o.deleted, o.created_at
		FROM store_objects o
		INNER JOIN (
			SELECT id, MAX(version) AS max_ver
			FROM store_objects
			GROUP BY id
		) latest ON o.id = latest.id AND o.version = latest.max_ver
		WHERE o.deleted = 0`

	var args []any

	if q.Type != "" {
		query += " AND o.type = ?"
		args = append(args, q.Type)
	}
	if q.Skill != "" {
		// Show objects owned by this skill, or public objects from any skill
		query += " AND (o.skill = ? OR o.public = 1)"
		args = append(args, q.Skill)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	query += " ORDER BY o.created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var objects []*Object
	for rows.Next() {
		obj, err := scanObjectRow(rows)
		if err != nil {
			return nil, err
		}

		// Apply JSON field filters
		if len(q.Filter) > 0 && !matchesFilter(obj.Data, q.Filter) {
			continue
		}

		objects = append(objects, obj)
	}
	return objects, rows.Err()
}

func (s *SQLite) History(ctx context.Context, id string) ([]*Object, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, version, type, skill, data, public, deleted, created_at
		 FROM store_objects WHERE id = ? ORDER BY version ASC`, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var objects []*Object
	for rows.Next() {
		obj, err := scanObjectRow(rows)
		if err != nil {
			return nil, err
		}
		objects = append(objects, obj)
	}
	return objects, rows.Err()
}

// --- helpers ---

type scannable interface {
	Scan(dest ...any) error
}

func scanObject(row *sql.Row) (*Object, error) {
	return scanObjectRow(row)
}

func scanObjectRow(row scannable) (*Object, error) {
	obj := &Object{}
	var data, createdAt string
	var pub, del int
	err := row.Scan(&obj.ID, &obj.Version, &obj.Type, &obj.Skill, &data, &pub, &del, &createdAt)
	if err != nil {
		return nil, err
	}
	obj.Data = json.RawMessage(data)
	obj.Public = pub != 0
	obj.Deleted = del != 0
	obj.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return obj, nil
}

func matchesFilter(data json.RawMessage, filter map[string]any) bool {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	for k, v := range filter {
		val, ok := m[k]
		if !ok {
			return false
		}
		if fmt.Sprintf("%v", val) != fmt.Sprintf("%v", v) {
			return false
		}
	}
	return true
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
