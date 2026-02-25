package agent

import (
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"hi", 1},       // 2 chars → ceil(2/4) = 1
		{"hello", 2},    // 5 chars → ceil(5/4) = 2
		{"12345678", 2}, // 8 chars → 8/4 = 2
	}
	for _, tt := range tests {
		got := estimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("estimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestTrimHistoryNoTruncation(t *testing.T) {
	a := &Agent{MaxContext: 10000}
	history := []chatMessage{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
		{Role: "user", Content: "How are you?"},
	}

	result := a.trimHistory("You are helpful.", history, 0)
	if len(result) != 3 {
		t.Errorf("got %d messages, want 3", len(result))
	}
}

func TestTrimHistoryDropsOldest(t *testing.T) {
	// Tiny context window — should drop older messages
	a := &Agent{MaxContext: 200}

	// System prompt ~40 chars = ~10 tokens
	// Budget: 200 * 0.8 = 160 tokens, minus ~10 for system = 150 remaining
	// Each message ~10 tokens of overhead + content
	history := make([]chatMessage, 20)
	for i := range history {
		history[i] = chatMessage{Role: "user", Content: strings.Repeat("x", 40)} // ~10 tokens each + 10 overhead = 20 per msg
	}

	result := a.trimHistory("System prompt here.", history, 0)
	if len(result) >= 20 {
		t.Errorf("expected truncation, got all %d messages", len(result))
	}
	if len(result) == 0 {
		t.Error("expected at least some messages")
	}

	// Verify we kept the newest messages (last ones in the slice)
	lastOriginal := history[len(history)-1]
	lastKept := result[len(result)-1]
	if lastOriginal.Content != lastKept.Content {
		t.Error("newest message should be preserved")
	}
}

func TestTrimHistoryHugeSystemPrompt(t *testing.T) {
	// System prompt exceeds entire budget
	a := &Agent{MaxContext: 100}
	systemPrompt := strings.Repeat("x", 1000) // ~250 tokens, way over 80-token budget

	history := []chatMessage{
		{Role: "user", Content: "Hello"},
	}

	result := a.trimHistory(systemPrompt, history, 0)
	if result != nil {
		t.Errorf("expected nil when system prompt exceeds budget, got %d messages", len(result))
	}
}

func TestTrimHistoryWithToolDefs(t *testing.T) {
	a := &Agent{MaxContext: 500}
	// Budget: 400 tokens, minus system (~5), minus tool defs (200) = ~195 remaining
	history := make([]chatMessage, 30)
	for i := range history {
		history[i] = chatMessage{Role: "user", Content: strings.Repeat("y", 40)}
	}

	withTools := a.trimHistory("Hi", history, 200)
	withoutTools := a.trimHistory("Hi", history, 0)

	if len(withTools) >= len(withoutTools) {
		t.Errorf("tool defs should reduce available messages: with=%d, without=%d", len(withTools), len(withoutTools))
	}
}

func TestTrimHistoryDefaultBudget(t *testing.T) {
	a := &Agent{} // MaxContext = 0, should use 128k default

	history := []chatMessage{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "World"},
	}

	result := a.trimHistory("System", history, 0)
	if len(result) != 2 {
		t.Errorf("with 128k default, 2 short messages should fit: got %d", len(result))
	}
}
