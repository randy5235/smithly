package agent

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
