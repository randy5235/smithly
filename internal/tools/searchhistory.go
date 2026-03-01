package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"smithly.dev/internal/db"
	"smithly.dev/internal/memory"
)

// SearchHistory lets the agent search its own past conversation messages.
// Supports FTS5 keyword search, optional vector semantic search, and hybrid mode.
type SearchHistory struct {
	store    db.Store
	agentID  string
	searcher *memory.Searcher
}

// NewSearchHistory creates a search_history tool for the given agent.
// searcher may be nil, in which case the tool falls back to basic store search.
func NewSearchHistory(store db.Store, agentID string, searcher *memory.Searcher) *SearchHistory {
	return &SearchHistory{store: store, agentID: agentID, searcher: searcher}
}

func (sh *SearchHistory) Name() string { return "search_history" }
func (sh *SearchHistory) Description() string {
	desc := "Search past conversation messages by keyword. Returns matching messages with surrounding context and timestamps. Use this to recall details from earlier in the conversation."
	if sh.searcher != nil && sh.searcher.HasEmbedder() {
		desc += " Supports modes: keyword (default), semantic (meaning-based), hybrid (combined)."
	}
	return desc
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
			},
			"context": {
				"type": "integer",
				"description": "Number of messages to show before and after each match for conversation context (default 2)"
			},
			"mode": {
				"type": "string",
				"enum": ["keyword", "semantic", "hybrid"],
				"description": "Search mode: keyword (FTS5), semantic (vector similarity), hybrid (combined). Default: keyword or hybrid if embeddings are configured."
			}
		},
		"required": ["query"]
	}`)
}

func (sh *SearchHistory) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Query   string `json:"query"`
		Limit   int    `json:"limit"`
		Context int    `json:"context"`
		Mode    string `json:"mode"`
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
	if params.Context < 0 {
		params.Context = 0
	}
	if params.Context == 0 {
		params.Context = 2
	}

	if sh.searcher != nil {
		return sh.runWithSearcher(ctx, params.Query, params.Limit, params.Context, params.Mode)
	}

	// Fallback: basic store search (no scoring)
	msgs, err := sh.store.SearchMessages(ctx, sh.agentID, params.Query, params.Limit)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	return sh.formatMessages(ctx, msgs, params.Context), nil
}

func (sh *SearchHistory) runWithSearcher(ctx context.Context, query string, limit, ctxWindow int, mode string) (string, error) {
	results, err := sh.searcher.Search(ctx, sh.agentID, query, mode, limit)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}

	if len(results) == 0 {
		return fmt.Sprintf("No messages found matching %q.", query), nil
	}

	// Collect matched IDs for context fetching
	matchIDs := make(map[int64]bool, len(results))
	for _, r := range results {
		matchIDs[r.ID] = true
	}

	// Fetch context messages around each match
	type contextGroup struct {
		before []*db.Message
		match  memory.Result
		after  []*db.Message
	}
	var groups []contextGroup
	for _, r := range results {
		g := contextGroup{match: r}
		if ctxWindow > 0 {
			// Get messages before the match
			before, err := sh.store.GetMessagesByID(ctx, sh.agentID, r.ID, ctxWindow)
			if err == nil {
				g.before = before
			}
			// Get messages after the match
			after, err := sh.store.GetMessagesByID(ctx, sh.agentID, 0, 10000)
			if err == nil {
				var afterMatch []*db.Message
				for _, m := range after {
					if m.ID > r.ID && len(afterMatch) < ctxWindow {
						afterMatch = append(afterMatch, m)
					}
				}
				g.after = afterMatch
			}
		}
		groups = append(groups, g)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d message(s) matching %q:\n", len(results), query)
	for _, g := range groups {
		b.WriteString("\n---\n")
		// Before context
		for _, m := range g.before {
			if !matchIDs[m.ID] {
				writeMessage(&b, m, "")
			}
		}
		// The match itself
		writeMessage(&b, &g.match.Message, fmt.Sprintf(" [score: %.2f]", g.match.Score))
		// After context
		for _, m := range g.after {
			if !matchIDs[m.ID] {
				writeMessage(&b, m, "")
			}
		}
	}
	return b.String(), nil
}

func (sh *SearchHistory) formatMessages(ctx context.Context, msgs []*db.Message, ctxWindow int) string {
	if len(msgs) == 0 {
		return fmt.Sprintf("No messages found.")
	}

	matchIDs := make(map[int64]bool, len(msgs))
	for _, m := range msgs {
		matchIDs[m.ID] = true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d message(s):\n", len(msgs))
	for _, m := range msgs {
		b.WriteString("\n---\n")
		// Before context
		if ctxWindow > 0 {
			before, err := sh.store.GetMessagesByID(ctx, sh.agentID, m.ID, ctxWindow)
			if err == nil {
				for _, bm := range before {
					if !matchIDs[bm.ID] {
						writeMessage(&b, bm, "")
					}
				}
			}
		}
		writeMessage(&b, m, " [match]")
		// After context
		if ctxWindow > 0 {
			allMsgs, err := sh.store.GetMessagesByID(ctx, sh.agentID, 0, 10000)
			if err == nil {
				count := 0
				for _, am := range allMsgs {
					if am.ID > m.ID && count < ctxWindow && !matchIDs[am.ID] {
						writeMessage(&b, am, "")
						count++
					}
				}
			}
		}
	}
	return b.String()
}

func writeMessage(b *strings.Builder, m *db.Message, suffix string) {
	ts := m.CreatedAt.Format("2006-01-02 15:04:05")
	content := m.Content
	if len(content) > 500 {
		content = content[:500] + "..."
	}
	fmt.Fprintf(b, "[%s] %s: %s%s\n", ts, m.Role, content, suffix)
}
