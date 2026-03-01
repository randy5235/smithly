package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"smithly.dev/internal/db"
)

// ReadHistory lets the agent page backward through its conversation history.
// The agent calls it with a before_id to read messages before a specific point,
// or with no before_id to read the most recent messages.
type ReadHistory struct {
	store   db.Store
	agentID string
}

// NewReadHistory creates a read_history tool for the given agent.
func NewReadHistory(store db.Store, agentID string) *ReadHistory {
	return &ReadHistory{store: store, agentID: agentID}
}

func (rh *ReadHistory) Name() string { return "read_history" }
func (rh *ReadHistory) Description() string {
	return "Read past conversation messages. Returns messages in chronological order. Use before_id to page backward through history — the oldest message ID in the response can be passed as before_id to get the previous page."
}
func (rh *ReadHistory) NeedsApproval() bool { return false }

func (rh *ReadHistory) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"before_id": {
				"type": "integer",
				"description": "Return messages before this message ID. Omit to get the most recent messages."
			},
			"limit": {
				"type": "integer",
				"description": "Number of messages to return (default 20, max 100)"
			}
		}
	}`)
}

func (rh *ReadHistory) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		BeforeID int64 `json:"before_id"`
		Limit    int   `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if params.Limit <= 0 {
		params.Limit = 20
	}
	if params.Limit > 100 {
		params.Limit = 100
	}

	msgs, err := rh.store.GetMessagesByID(ctx, rh.agentID, params.BeforeID, params.Limit)
	if err != nil {
		return "", fmt.Errorf("read history: %w", err)
	}

	if len(msgs) == 0 {
		if params.BeforeID > 0 {
			return "No more messages before that point.", nil
		}
		return "No conversation history.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d message(s) (IDs %d–%d):\n\n", len(msgs), msgs[0].ID, msgs[len(msgs)-1].ID)
	for _, m := range msgs {
		ts := m.CreatedAt.Format("2006-01-02 15:04:05")
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		fmt.Fprintf(&b, "[#%d %s] %s: %s\n\n", m.ID, ts, m.Role, content)
	}
	if msgs[0].ID > 1 {
		fmt.Fprintf(&b, "Use before_id=%d to see earlier messages.", msgs[0].ID)
	}
	return b.String(), nil
}
