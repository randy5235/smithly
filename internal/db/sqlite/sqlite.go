// Package sqlite implements db.Store using modernc.org/sqlite (pure Go, no CGo).
// Migrations are embedded SQL files in the migrations/ subdirectory.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"

	"smithly.dev/internal/db"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store implements db.Store backed by SQLite.
type Store struct {
	conn *sql.DB
}

// New opens (or creates) a SQLite database at the given path.
func New(path string) (*Store, error) {
	dsn := path + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	d.SetMaxOpenConns(1) // SQLite single-writer
	return &Store{conn: d}, nil
}

func (s *Store) Close() error {
	return s.conn.Close()
}

// DB returns the underlying *sql.DB, for use by the object store layer.
func (s *Store) DB() *sql.DB {
	return s.conn
}

// Migrate runs all pending SQL migration files in order.
func (s *Store) Migrate(ctx context.Context) error {
	// Ensure schema_version table exists for tracking
	if _, err := s.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at TEXT DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	// Get current version
	var current int
	err := s.conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&current)
	if err != nil {
		return fmt.Errorf("get schema version: %w", err)
	}

	// Read migration files
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		// Parse version from filename (e.g., "001_init.sql" → 1)
		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%d_", &version); err != nil {
			continue
		}

		if version <= current {
			continue
		}

		data, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		tx, err := s.conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", entry.Name(), err)
		}

		stmts := splitStatements(string(data))
		for _, stmt := range stmts {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			// Skip schema_version inserts from migration files — we handle it below
			if strings.Contains(stmt, "schema_version") && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt)), "INSERT") {
				continue
			}
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %s: %w\nstatement: %s", entry.Name(), err, stmt)
			}
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version (version) VALUES (?)", version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}

	return nil
}

func splitStatements(sql string) []string {
	var stmts []string
	var current strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") {
			stmts = append(stmts, current.String())
			current.Reset()
		}
	}
	if s := strings.TrimSpace(current.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}

// --- Agents ---

func (s *Store) CreateAgent(ctx context.Context, agent *db.Agent) error {
	_, err := s.conn.ExecContext(ctx,
		`INSERT INTO agents (id, model, workspace_path, heartbeat_interval, heartbeat_enabled, quiet_hours)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		agent.ID, agent.Model, agent.WorkspacePath,
		agent.HeartbeatInterval, boolToInt(agent.HeartbeatEnabled), agent.QuietHours,
	)
	return err
}

func (s *Store) GetAgent(ctx context.Context, id string) (*db.Agent, error) {
	row := s.conn.QueryRowContext(ctx,
		`SELECT id, model, workspace_path, heartbeat_interval, heartbeat_enabled, quiet_hours, created_at
		 FROM agents WHERE id = ?`, id)
	return scanAgent(row)
}

func (s *Store) ListAgents(ctx context.Context) ([]*db.Agent, error) {
	rows, err := s.conn.QueryContext(ctx,
		`SELECT id, model, workspace_path, heartbeat_interval, heartbeat_enabled, quiet_hours, created_at
		 FROM agents ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*db.Agent
	for rows.Next() {
		a, err := scanAgentRow(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	_, err := s.conn.ExecContext(ctx, "DELETE FROM agents WHERE id = ?", id)
	return err
}

// --- Memory ---

func (s *Store) AppendMessage(ctx context.Context, msg *db.Message) error {
	_, err := s.conn.ExecContext(ctx,
		`INSERT INTO memory (agent_id, role, content, source, trust)
		 VALUES (?, ?, ?, ?, ?)`,
		msg.AgentID, msg.Role, msg.Content, msg.Source, msg.Trust,
	)
	return err
}

func (s *Store) GetMessages(ctx context.Context, agentID string, limit int) ([]*db.Message, error) {
	query := `SELECT id, agent_id, role, content, source, trust, created_at
		FROM memory
		WHERE agent_id = ? AND deleted = 0
		ORDER BY id DESC
		LIMIT ?`

	rows, err := s.conn.QueryContext(ctx, query, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*db.Message
	for rows.Next() {
		m := &db.Message{}
		var createdAt string
		if err := rows.Scan(&m.ID, &m.AgentID, &m.Role, &m.Content, &m.Source, &m.Trust, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reverse to chronological order
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// --- Audit ---

func (s *Store) LogAudit(ctx context.Context, entry *db.AuditEntry) error {
	_, err := s.conn.ExecContext(ctx,
		`INSERT INTO audit_log (actor, action, target, details, trust_level, approved_by, domain)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.Actor, entry.Action, entry.Target, entry.Details,
		entry.TrustLevel, entry.ApprovedBy, entry.Domain,
	)
	return err
}

func (s *Store) GetAuditLog(ctx context.Context, opts db.AuditQuery) ([]*db.AuditEntry, error) {
	query := `SELECT id, timestamp, actor, action, target, details, trust_level, approved_by, domain
		FROM audit_log WHERE 1=1`
	var args []any

	if opts.AgentID != "" {
		query += " AND actor LIKE ?"
		args = append(args, "agent:"+opts.AgentID+"%")
	}
	if opts.Skill != "" {
		query += " AND actor LIKE ?"
		args = append(args, "skill:"+opts.Skill+"%")
	}
	if opts.Domain != "" {
		query += " AND domain = ?"
		args = append(args, opts.Domain)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*db.AuditEntry
	for rows.Next() {
		e := &db.AuditEntry{}
		var ts string
		var target, details, approvedBy, domain sql.NullString
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &target, &details, &e.TrustLevel, &approvedBy, &domain); err != nil {
			return nil, err
		}
		e.Timestamp, _ = time.Parse("2006-01-02 15:04:05", ts)
		e.Target = target.String
		e.Details = details.String
		e.ApprovedBy = approvedBy.String
		e.Domain = domain.String
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// --- helpers ---

type scannable interface {
	Scan(dest ...any) error
}

func scanAgent(row *sql.Row) (*db.Agent, error) {
	return scanAgentRow(row)
}

func scanAgentRow(row scannable) (*db.Agent, error) {
	a := &db.Agent{}
	var hbInterval, quietHours sql.NullString
	var hbEnabled int
	var createdAt string
	err := row.Scan(&a.ID, &a.Model, &a.WorkspacePath, &hbInterval, &hbEnabled, &quietHours, &createdAt)
	if err != nil {
		return nil, err
	}
	a.HeartbeatInterval = hbInterval.String
	a.HeartbeatEnabled = hbEnabled != 0
	a.QuietHours = quietHours.String
	a.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return a, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
