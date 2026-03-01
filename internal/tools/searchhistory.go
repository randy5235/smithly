package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"smithly.dev/internal/db"
)

// SearchHistory lets the agent search its own past conversation messages.
// Useful for recalling details that were compacted out of the active context.
type SearchHistory struct {
	store   db.Store
	agentID string
}

// NewSearchHistory creates a search_history tool for the given agent.
func NewSearchHistory(store db.Store, agentID string) *SearchHistory {
	return &SearchHistory{store: store, agentID: agentID}
}

func (sh *SearchHistory) Name() string        { return "search_history" }
func (sh *SearchHistory) Description() string {
	return "Search past conversation messages by keyword. Returns matching messages with timestamps. Use this to recall details from earlier in the conversation."
}
func (sh *SearchHistory) NeedsApproval() bool { return false }

func (sh *SearchHistory) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Search term or phrase to find in past messages"
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of results (default 20)"
			}
		},
		"required": ["query"]
	}`)
}

func (sh *SearchHistory) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if params.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	if params.Limit <= 0 {
		params.Limit = 20
	}

	msgs, err := sh.store.SearchMessages(ctx, sh.agentID, params.Query, params.Limit)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}

	if len(msgs) == 0 {
		return fmt.Sprintf("No messages found matching %q.", params.Query), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d message(s) matching %q:\n\n", len(msgs), params.Query)
	for _, m := range msgs {
		ts := m.CreatedAt.Format("2006-01-02 15:04:05")
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n\n", ts, m.Role, content)
	}
	return b.String(), nil
}
