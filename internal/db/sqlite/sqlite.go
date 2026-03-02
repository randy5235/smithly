// Package sqlite implements db.Store using modernc.org/sqlite (pure Go, no CGo).
// Migrations are embedded SQL files in the migrations/ subdirectory.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"smithly.dev/internal/db"

	_ "modernc.org/sqlite"
)

// wrapNotFound converts sql.ErrNoRows into db.ErrNotFound.
func wrapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return db.ErrNotFound
	}
	return err
}

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
				if rbErr := tx.Rollback(); rbErr != nil {
					slog.Error("migration rollback failed", "err", rbErr)
				}
				return fmt.Errorf("migration %s: %w\nstatement: %s", entry.Name(), err, stmt)
			}
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version (version) VALUES (?)", version); err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				slog.Error("migration rollback failed", "err", rbErr)
			}
			return fmt.Errorf("record migration %d: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}

	return nil
}

// SplitStatements is exported for testing only.
func SplitStatements(sql string) []string { return splitStatements(sql) }

func splitStatements(sql string) []string {
	var stmts []string
	var current strings.Builder
	depth := 0 // tracks BEGIN/END nesting for triggers
	for line := range strings.SplitSeq(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		upper := strings.ToUpper(trimmed)
		if upper == "BEGIN" || strings.HasPrefix(upper, "BEGIN ") || strings.HasSuffix(upper, " BEGIN") {
			depth++
		}
		if strings.HasPrefix(upper, "END;") || upper == "END" {
			if depth > 0 {
				depth--
			}
		}
		current.WriteString(line)
		current.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") && depth == 0 {
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
	res, err := s.conn.ExecContext(ctx,
		`INSERT INTO memory (agent_id, role, content, source, trust)
		 VALUES (?, ?, ?, ?, ?)`,
		msg.AgentID, msg.Role, msg.Content, msg.Source, msg.Trust,
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err == nil {
		msg.ID = id
	}
	return nil
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
		m.CreatedAt = parseTime(createdAt)
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

func (s *Store) SearchMessages(ctx context.Context, agentID, query string, limit int) ([]*db.Message, error) {
	if limit <= 0 {
		limit = 20
	}
	// Quote query as a phrase to avoid FTS5 syntax interpretation
	// (e.g., "Summary:" would otherwise be parsed as column:term)
	ftsQuery := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	rows, err := s.conn.QueryContext(ctx,
		`SELECT m.id, m.agent_id, m.role, m.content, m.source, m.trust, m.created_at
		 FROM memory_fts f
		 JOIN memory m ON m.id = f.rowid
		 WHERE memory_fts MATCH ? AND m.agent_id = ?
		 ORDER BY bm25(memory_fts) ASC
		 LIMIT ?`,
		ftsQuery, agentID, limit,
	)
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
		m.CreatedAt = parseTime(createdAt)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (s *Store) InsertSummary(ctx context.Context, agentID, summary string) error {
	_, err := s.conn.ExecContext(ctx,
		`INSERT INTO memory (agent_id, role, content, source, trust)
		 VALUES (?, 'system', ?, 'summary', 'trusted')`,
		agentID, summary,
	)
	return err
}

func (s *Store) GetMessagesByID(ctx context.Context, agentID string, beforeID int64, limit int) ([]*db.Message, error) {
	if limit <= 0 {
		limit = 20
	}
	var query string
	var args []any
	if beforeID > 0 {
		query = `SELECT id, agent_id, role, content, source, trust, created_at
			FROM memory
			WHERE agent_id = ? AND deleted = 0 AND id < ?
			ORDER BY id DESC
			LIMIT ?`
		args = []any{agentID, beforeID, limit}
	} else {
		query = `SELECT id, agent_id, role, content, source, trust, created_at
			FROM memory
			WHERE agent_id = ? AND deleted = 0
			ORDER BY id DESC
			LIMIT ?`
		args = []any{agentID, limit}
	}

	rows, err := s.conn.QueryContext(ctx, query, args...)
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
		m.CreatedAt = parseTime(createdAt)
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

func (s *Store) SearchMessagesFTS(ctx context.Context, agentID, query string, limit int) ([]*db.SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	ftsQuery := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	rows, err := s.conn.QueryContext(ctx,
		`SELECT m.id, m.agent_id, m.role, m.content, m.source, m.trust, m.created_at, bm25(memory_fts) AS score
		 FROM memory_fts f
		 JOIN memory m ON m.id = f.rowid
		 WHERE memory_fts MATCH ? AND m.agent_id = ?
		 ORDER BY score ASC
		 LIMIT ?`,
		ftsQuery, agentID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*db.SearchResult
	for rows.Next() {
		r := &db.SearchResult{}
		var createdAt string
		if err := rows.Scan(&r.ID, &r.AgentID, &r.Role, &r.Content, &r.Source, &r.Trust, &createdAt, &r.Score); err != nil {
			return nil, err
		}
		r.CreatedAt = parseTime(createdAt)
		results = append(results, r)
	}
	return results, rows.Err()
}

// --- Embeddings ---

func (s *Store) StoreEmbedding(ctx context.Context, memoryID int64, embedding []float32, model string, dimensions int) error {
	blob := encodeFloat32(embedding)
	_, err := s.conn.ExecContext(ctx,
		`INSERT OR REPLACE INTO memory_embeddings (memory_id, embedding, model, dimensions)
		 VALUES (?, ?, ?, ?)`,
		memoryID, blob, model, dimensions,
	)
	return err
}

func (s *Store) GetEmbeddings(ctx context.Context, agentID string) ([]db.MemoryEmbedding, error) {
	rows, err := s.conn.QueryContext(ctx,
		`SELECT e.memory_id, e.embedding, e.model, e.dimensions, m.trust
		 FROM memory_embeddings e
		 JOIN memory m ON m.id = e.memory_id
		 WHERE m.agent_id = ?`,
		agentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var embeddings []db.MemoryEmbedding
	for rows.Next() {
		var me db.MemoryEmbedding
		var blob []byte
		if err := rows.Scan(&me.MemoryID, &blob, &me.Model, &me.Dimensions, &me.Trust); err != nil {
			return nil, err
		}
		me.Embedding = decodeFloat32(blob)
		embeddings = append(embeddings, me)
	}
	return embeddings, rows.Err()
}

func (s *Store) GetEmbeddingCount(ctx context.Context, agentID string) (int, error) {
	var count int
	err := s.conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_embeddings e
		 JOIN memory m ON m.id = e.memory_id
		 WHERE m.agent_id = ?`, agentID).Scan(&count)
	return count, err
}

func (s *Store) GetUnembeddedMessages(ctx context.Context, agentID string, limit int) ([]*db.Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.conn.QueryContext(ctx,
		`SELECT m.id, m.agent_id, m.role, m.content, m.source, m.trust, m.created_at
		 FROM memory m
		 LEFT JOIN memory_embeddings e ON e.memory_id = m.id
		 WHERE m.agent_id = ? AND m.deleted = 0 AND e.memory_id IS NULL
		 ORDER BY m.id
		 LIMIT ?`,
		agentID, limit,
	)
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
		m.CreatedAt = parseTime(createdAt)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// encode/decode helpers for embedding BLOBs

func encodeFloat32(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		bits := math.Float32bits(f)
		buf[i*4] = byte(bits)
		buf[i*4+1] = byte(bits >> 8)
		buf[i*4+2] = byte(bits >> 16)
		buf[i*4+3] = byte(bits >> 24)
	}
	return buf
}

func decodeFloat32(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := range n {
		bits := uint32(b[i*4]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24
		v[i] = math.Float32frombits(bits)
	}
	return v
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
		e.Timestamp = parseTime(ts)
		e.Target = target.String
		e.Details = details.String
		e.ApprovedBy = approvedBy.String
		e.Domain = domain.String
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// --- Domains ---

func (s *Store) GetDomain(ctx context.Context, domain string) (*db.DomainEntry, error) {
	row := s.conn.QueryRowContext(ctx,
		`SELECT domain, status, granted_by, granted_at, last_accessed, access_count, requested_by, notes
		 FROM domain_allowlist WHERE domain = ?`, domain)

	d := &db.DomainEntry{}
	var grantedAt string
	var lastAccessed, requestedBy, notes sql.NullString
	err := row.Scan(&d.Domain, &d.Status, &d.GrantedBy, &grantedAt, &lastAccessed, &d.AccessCount, &requestedBy, &notes)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	d.GrantedAt = parseTime(grantedAt)
	if lastAccessed.Valid {
		d.LastAccessed = parseTime(lastAccessed.String)
	}
	d.RequestedBy = requestedBy.String
	d.Notes = notes.String
	return d, nil
}

func (s *Store) ListDomains(ctx context.Context) ([]*db.DomainEntry, error) {
	rows, err := s.conn.QueryContext(ctx,
		`SELECT domain, status, granted_by, granted_at, last_accessed, access_count, requested_by, notes
		 FROM domain_allowlist ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*db.DomainEntry
	for rows.Next() {
		d := &db.DomainEntry{}
		var grantedAt string
		var lastAccessed, requestedBy, notes sql.NullString
		if err := rows.Scan(&d.Domain, &d.Status, &d.GrantedBy, &grantedAt, &lastAccessed, &d.AccessCount, &requestedBy, &notes); err != nil {
			return nil, err
		}
		d.GrantedAt = parseTime(grantedAt)
		if lastAccessed.Valid {
			d.LastAccessed = parseTime(lastAccessed.String)
		}
		d.RequestedBy = requestedBy.String
		d.Notes = notes.String
		entries = append(entries, d)
	}
	return entries, rows.Err()
}

func (s *Store) SetDomain(ctx context.Context, entry *db.DomainEntry) error {
	_, err := s.conn.ExecContext(ctx,
		`INSERT INTO domain_allowlist (domain, status, granted_by, requested_by, notes)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(domain) DO UPDATE SET
		   status = excluded.status,
		   granted_by = excluded.granted_by,
		   granted_at = datetime('now'),
		   requested_by = excluded.requested_by,
		   notes = excluded.notes`,
		entry.Domain, entry.Status, entry.GrantedBy, entry.RequestedBy, entry.Notes)
	return err
}

func (s *Store) TouchDomain(ctx context.Context, domain string) error {
	_, err := s.conn.ExecContext(ctx,
		`UPDATE domain_allowlist
		 SET access_count = access_count + 1, last_accessed = datetime('now')
		 WHERE domain = ?`, domain)
	return err
}

// --- helpers ---

type scannable interface {
	Scan(dest ...any) error
}

func scanAgent(row *sql.Row) (*db.Agent, error) {
	a, err := scanAgentRow(row)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	return a, nil
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
	a.CreatedAt = parseTime(createdAt)
	return a, nil
}

func parseTime(value string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil && value != "" {
		slog.Warn("failed to parse sqlite datetime", "value", value, "err", err)
	}
	return t
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
