// Package db defines the Store interface for Smithly's data layer.
// Each backend (SQLite, Postgres, etc.) lives in its own sub-package
// and owns its own schema, migrations, and connection logic.
package db

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// Store is the data access interface. Each backend implements this fully,
// including its own migration strategy. SQLite uses embedded SQL files,
// Postgres would use its own SQL dialect, MongoDB wouldn't use SQL at all.
type Store interface {
	// Lifecycle
	Close() error

	// Migrate applies any pending schema changes.
	// Each implementation owns its migration format and strategy.
	Migrate(ctx context.Context) error

	// Agents
	CreateAgent(ctx context.Context, agent *Agent) error
	GetAgent(ctx context.Context, id string) (*Agent, error)
	ListAgents(ctx context.Context) ([]*Agent, error)
	DeleteAgent(ctx context.Context, id string) error

	// Memory (conversation history per agent)
	AppendMessage(ctx context.Context, msg *Message) error
	GetMessages(ctx context.Context, agentID string, limit int) ([]*Message, error)
	GetMessagesByID(ctx context.Context, agentID string, beforeID int64, limit int) ([]*Message, error)
	SearchMessages(ctx context.Context, agentID, query string, limit int) ([]*Message, error)
	SearchMessagesFTS(ctx context.Context, agentID, query string, limit int) ([]*SearchResult, error)
	InsertSummary(ctx context.Context, agentID, summary string) error

	// Embeddings
	StoreEmbedding(ctx context.Context, memoryID int64, embedding []float32, model string, dimensions int) error
	GetEmbeddings(ctx context.Context, agentID string) ([]MemoryEmbedding, error)
	GetEmbeddingCount(ctx context.Context, agentID string) (int, error)
	GetUnembeddedMessages(ctx context.Context, agentID string, limit int) ([]*Message, error)

	// Audit log (append-only)
	LogAudit(ctx context.Context, entry *AuditEntry) error
	GetAuditLog(ctx context.Context, opts AuditQuery) ([]*AuditEntry, error)

	// Domain allowlist
	GetDomain(ctx context.Context, domain string) (*DomainEntry, error)
	ListDomains(ctx context.Context) ([]*DomainEntry, error)
	SetDomain(ctx context.Context, entry *DomainEntry) error
	TouchDomain(ctx context.Context, domain string) error
}

// Agent represents an agent configuration in the database.
type Agent struct {
	ID                string
	Model             string
	WorkspacePath     string
	HeartbeatInterval string
	HeartbeatEnabled  bool
	QuietHours        string
	CreatedAt         time.Time
}

// Message is a single conversation turn stored in memory.
type Message struct {
	ID        int64
	AgentID   string
	Role      string // "user", "assistant", "system"
	Content   string
	Source    string // "user", "channel:telegram", etc.
	Trust     string // "trusted", "semi-trusted", "untrusted"
	CreatedAt time.Time
}

// SearchResult is a message with a relevance score from FTS5 search.
type SearchResult struct {
	Message
	Score float64 // BM25 score (lower = more relevant in SQLite FTS5)
}

// MemoryEmbedding is a stored embedding vector for a message.
type MemoryEmbedding struct {
	MemoryID   int64
	Embedding  []float32
	Model      string
	Dimensions int
	Trust      string // trust level of the original message
}

// AuditEntry is an append-only audit log record.
type AuditEntry struct {
	ID         int64
	Timestamp  time.Time
	Actor      string // "agent:assistant", "skill:weather", "user"
	Action     string
	Target     string
	Details    string // JSON
	TrustLevel string
	ApprovedBy string
	Domain     string
}

// DomainEntry represents a row in the domain_allowlist table.
type DomainEntry struct {
	Domain       string
	Status       string // "allow", "deny"
	GrantedBy    string // "user", "skill:<name>", "default"
	GrantedAt    time.Time
	LastAccessed time.Time
	AccessCount  int
	RequestedBy  string
	Notes        string
}

// AuditQuery filters audit log queries.
type AuditQuery struct {
	AgentID string
	Skill   string
	Domain  string
	Limit   int
}
