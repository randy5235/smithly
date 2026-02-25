// Package store provides an optional versioned, append-only object store.
// Skills that want simple persistence without writing raw DB code use this.
// Skills that need full DB power connect directly via env vars instead.
package store

import (
	"context"
	"encoding/json"
	"time"
)

// Object is a versioned record in the store. Every mutation creates a new
// version — nothing is overwritten or hard-deleted.
type Object struct {
	ID        string          `json:"id"`
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	Skill     string          `json:"skill"`
	Data      json.RawMessage `json:"data"`
	Public    bool            `json:"public"`
	Deleted   bool            `json:"deleted"`
	CreatedAt time.Time       `json:"created_at"`
}

// Query filters objects in the store.
type Query struct {
	ID          string         `json:"id,omitempty"`
	Type        string         `json:"type,omitempty"`
	Skill       string         `json:"skill,omitempty"`
	Filter      map[string]any `json:"filter,omitempty"`
	WithHistory bool           `json:"with_history,omitempty"`
	Limit       int            `json:"limit,omitempty"`
}

// Store is the interface for versioned object persistence.
type Store interface {
	// Put creates a new version of an object. If obj.ID is empty, one is generated.
	Put(ctx context.Context, obj *Object) (*Object, error)

	// Get returns the latest version of an object by ID.
	Get(ctx context.Context, id string) (*Object, error)

	// Delete soft-deletes an object by creating a new version with Deleted=true.
	Delete(ctx context.Context, id, skill string) error

	// Query returns the latest version of objects matching the query, excluding deleted.
	Query(ctx context.Context, q *Query) ([]*Object, error)

	// History returns all versions of an object, oldest first.
	History(ctx context.Context, id string) ([]*Object, error)
}
