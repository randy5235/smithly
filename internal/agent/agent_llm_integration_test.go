package agent_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"smithly.dev/internal/agent"
	"smithly.dev/internal/config"
	"smithly.dev/internal/db/sqlite"
	"smithly.dev/internal/sandbox"
	"smithly.dev/internal/tools"
	"smithly.dev/internal/workspace"
)

// runLLMSkillTest is the shared test logic for real LLM integration tests.
// It gives the agent write_skill + run_code_skill tools, asks it to add
// two numbers, and verifies the LLM figures out the tool sequence on its own.
func runLLMSkillTest(t *testing.T, model, provider, baseURL, apiKey string) {
	t.Helper()

	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer store.Close()

	ws, _ := workspace.Load("")
	a := agent.New("test-agent", model, provider, baseURL, apiKey, ws, store)

	skillsDir := t.TempDir()

	sbx, err := sandbox.NewProvider(config.SandboxConfig{Provider: "none"}, nil, nil, "")
	if err != nil {
		t.Fatalf("create sandbox provider: %v", err)
	}

	mockNotify := &testNotifyProvider{}

	a.Tools.Register(tools.NewWriteSkill(a.Skills, skillsDir))
	a.Tools.Register(tools.NewRunCodeSkill(a.Skills, sbx))
	a.Tools.Register(tools.NewNotify(mockNotify))

	var toolCalls []string
	cb := &agent.Callbacks{
		Approve:    func(name, desc string) bool { return true },
		OnToolCall: func(name, args string) { toolCalls = append(toolCalls, name) },
		OnToolResult: func(name, result string) {
			t.Logf("tool result [%s]: %s", name, truncate(result, 200))
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := a.Chat(ctx, "Write a bash code skill that adds two numbers from JSON stdin (fields \"a\" and \"b\"), then run it with a=7 and b=8. If it fails, fix the skill and try again. Tell me the result.", cb)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	t.Logf("Tool calls: %v", toolCalls)
	t.Logf("Final response: %s", truncate(result, 500))

	if !containsStr(toolCalls, "write_skill") {
		t.Error("LLM never called write_skill")
	}
	if !containsStr(toolCalls, "run_code_skill") {
		t.Error("LLM never called run_code_skill")
	}
	if !strings.Contains(result, "15") {
		t.Errorf("expected result to contain '15', got: %s", truncate(result, 500))
	}
	if len(a.Skills.All()) == 0 {
		t.Error("no skills registered — write_skill may have failed")
	}
}

// TestLLMGeminiWriteAndRunSkill tests with Gemini via its OpenAI-compatible endpoint.
//
//	GEMINI_API_KEY=... go test ./internal/agent/ -run TestLLMGemini -v
func TestLLMGeminiWriteAndRunSkill(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set")
	}
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}
	runLLMSkillTest(t, model, "gemini", "https://generativelanguage.googleapis.com/v1beta/openai", apiKey)
}

// TestLLMOpenAIWriteAndRunSkill tests with OpenAI.
//
//	OPENAI_API_KEY=... go test ./internal/agent/ -run TestLLMOpenAI -v
func TestLLMOpenAIWriteAndRunSkill(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-5.3-codex"
	}
	runLLMSkillTest(t, model, "openai", "https://api.openai.com/v1", apiKey)
}

func containsStr(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
