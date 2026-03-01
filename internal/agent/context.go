package agent

import (
	"context"
	"fmt"
	"strings"

	"smithly.dev/internal/db"
	"smithly.dev/internal/tools"
)

// estimateTokens returns a rough token count for a string.
// Uses the ~4 characters per token heuristic (works across most models).
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// contextBudget returns the max tokens for the context window.
func (a *Agent) contextBudget() int {
	if a.MaxContext > 0 {
		return a.MaxContext
	}
	return 128_000 // sensible default for most modern models
}

// trimHistory drops the oldest messages to fit within the context budget.
// It reserves space for the system prompt and tool definitions, then fills
// the remaining budget with messages from newest to oldest.
func (a *Agent) trimHistory(systemPrompt string, history []chatMessage, toolDefsJSON int) []chatMessage {
	budget := a.contextBudget()

	// Reserve 20% for the LLM's response
	budget = budget * 80 / 100

	// Subtract system prompt and tool definitions
	budget -= estimateTokens(systemPrompt)
	budget -= toolDefsJSON

	if budget <= 0 {
		// System prompt alone exceeds budget — return nothing
		return nil
	}

	// Walk history backwards (newest first), accumulating until we run out of budget
	used := 0
	cutoff := len(history)
	for i := len(history) - 1; i >= 0; i-- {
		content, _ := history[i].Content.(string)
		msgTokens := estimateTokens(content) + 10 // overhead for role, formatting
		if used+msgTokens > budget {
			cutoff = i + 1
			break
		}
		used += msgTokens
		if i == 0 {
			cutoff = 0
		}
	}

	return history[cutoff:]
}

// compactHistory manages context window usage by summarizing old messages when
// history exceeds 60% of the context budget. The summary is stored as a message
// with source="summary" and acts as a context boundary — on future loads, only
// messages from the latest summary forward are used for context.
//
// All original messages remain in the DB and are searchable via search_history.
func (a *Agent) compactHistory(ctx context.Context, systemPrompt string, history []*db.Message, toolDefs []tools.OpenAITool) ([]chatMessage, error) {
	toolDefsTokens := len(toolDefs) * 100

	// Convert DB messages to chat messages, respecting summary boundaries.
	// Find the last summary message and only use it + everything after it.
	startIdx := 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Source == "summary" {
			startIdx = i
			break
		}
	}
	contextHistory := history[startIdx:]

	// Estimate total tokens for the active context messages
	totalTokens := 0
	for _, m := range contextHistory {
		totalTokens += estimateTokens(m.Content) + 10
	}

	budget := a.contextBudget()
	compactionThreshold := budget * 60 / 100

	// If under 60% budget, no compaction needed — just trim as before
	if totalTokens <= compactionThreshold {
		var msgs []chatMessage
		for _, m := range contextHistory {
			msgs = append(msgs, chatMessage{Role: m.Role, Content: m.Content})
		}
		return a.trimHistory(systemPrompt, msgs, toolDefsTokens), nil
	}

	// Over budget — compact. Split into old (to summarize) and recent (to keep).
	// Keep enough recent messages to fill ~40% of budget.
	recentBudget := budget * 40 / 100
	recentTokens := 0
	splitIdx := len(contextHistory)
	for i := len(contextHistory) - 1; i >= 0; i-- {
		msgTokens := estimateTokens(contextHistory[i].Content) + 10
		if recentTokens+msgTokens > recentBudget {
			splitIdx = i + 1
			break
		}
		recentTokens += msgTokens
		if i == 0 {
			splitIdx = 0
		}
	}

	// If nothing to compact (all messages are "recent"), just trim
	if splitIdx == 0 {
		var msgs []chatMessage
		for _, m := range contextHistory {
			msgs = append(msgs, chatMessage{Role: m.Role, Content: m.Content})
		}
		return a.trimHistory(systemPrompt, msgs, toolDefsTokens), nil
	}

	oldMessages := contextHistory[:splitIdx]
	recentMessages := contextHistory[splitIdx:]

	// Build compaction prompt and call LLM
	compactionPrompt := a.buildCompactionPrompt(oldMessages)
	compactionMessages := []chatMessage{
		{Role: "system", Content: compactionPrompt},
	}

	resp, err := a.LLM.SendChat(ctx, a.Model, compactionMessages, nil, nil)
	if err != nil {
		// Compaction failed — fall back to simple trim
		var msgs []chatMessage
		for _, m := range contextHistory {
			msgs = append(msgs, chatMessage{Role: m.Role, Content: m.Content})
		}
		return a.trimHistory(systemPrompt, msgs, toolDefsTokens), nil
	}

	summary := resp.Content

	// Store summary in DB as a context boundary
	if err := a.Store.InsertSummary(ctx, a.ID, summary); err != nil {
		// Non-fatal — we can still use the summary for this turn
		_ = err
	}

	// Build result: summary as system message + recent messages
	var result []chatMessage
	result = append(result, chatMessage{Role: "system", Content: summary})
	for _, m := range recentMessages {
		result = append(result, chatMessage{Role: m.Role, Content: m.Content})
	}

	return result, nil
}

// buildCompactionPrompt assembles the LLM prompt for summarizing old messages.
// Includes skill inventory so the summary preserves awareness of installed skills.
func (a *Agent) buildCompactionPrompt(oldMessages []*db.Message) string {
	var b strings.Builder

	b.WriteString("You are a conversation summarizer. Summarize the following conversation history into a concise context document that preserves the most important information.\n\n")
	b.WriteString("Your summary MUST include:\n")
	b.WriteString("- Key decisions and outcomes\n")
	b.WriteString("- Skills created or modified (name + one-line description)\n")
	b.WriteString("- Errors encountered and how they were resolved\n")
	b.WriteString("- User preferences or instructions expressed\n")
	b.WriteString("- Facts needed to continue the conversation\n\n")
	b.WriteString("Keep it concise — aim for under 500 words. Use bullet points.\n\n")

	// Include skill inventory
	if a.Skills != nil {
		allSkills := a.Skills.All()
		if len(allSkills) > 0 {
			b.WriteString("## Installed Skills\n")
			b.WriteString("(Always mention these in the summary so the agent remains aware of them.)\n")
			for _, s := range allSkills {
				desc := s.Manifest.Skill.Description
				if desc == "" {
					desc = "(no description)"
				}
				fmt.Fprintf(&b, "- %s: %s\n", s.Manifest.Skill.Name, desc)
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("## Conversation to Summarize\n\n")
	for _, m := range oldMessages {
		content := m.Content
		if len(content) > 2000 {
			content = content[:2000] + "... [truncated]"
		}
		fmt.Fprintf(&b, "**%s**: %s\n\n", m.Role, content)
	}

	return b.String()
}
