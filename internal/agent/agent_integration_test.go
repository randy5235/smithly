package agent_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"smithly.dev/internal/agent"
	"smithly.dev/internal/config"
	"smithly.dev/internal/sandbox"
	"smithly.dev/internal/tools"
)

// testNotifyProvider captures notifications for assertions.
type testNotifyProvider struct {
	lastTitle    string
	lastMessage  string
	lastPriority int
}

func (p *testNotifyProvider) Name() string { return "test" }
func (p *testNotifyProvider) Send(_ context.Context, title, message string, priority int) error {
	p.lastTitle = title
	p.lastMessage = message
	p.lastPriority = priority
	return nil
}

// TestAgentWritesAndRunsCodeSkill is an integration test that verifies the full
// agent loop: the LLM writes a code skill, executes it, and sends a notification
// with the result. The bash skill adds two numbers from JSON input.
func TestAgentWritesAndRunsCodeSkill(t *testing.T) {
	skillsDir := t.TempDir()

	// Build tool call args using Go maps so we don't have to fight JSON escaping.
	writeArgs, _ := json.Marshal(map[string]any{
		"name":        "add-numbers",
		"description": "Adds two numbers from JSON input",
		"runtime":     "bash",
		"entrypoint":  "add.sh",
		"code": `#!/bin/bash
input=$(cat)
a=$(echo "$input" | sed 's/.*"a":\([0-9]*\).*/\1/')
b=$(echo "$input" | sed 's/.*"b":\([0-9]*\).*/\1/')
echo $((a + b))
`,
	})

	runArgs, _ := json.Marshal(map[string]any{
		"name":  "add-numbers",
		"input": map[string]any{"a": 2, "b": 3},
	})

	notifyArgs, _ := json.Marshal(map[string]any{
		"title":   "Addition Result",
		"message": "The sum of 2 and 3 is 5",
	})

	mock := &mockLLM{
		responses: []mockResponse{
			// Turn 1: LLM calls write_skill to create the bash skill
			{toolCalls: []mockTool{
				{id: "call_1", name: "write_skill", args: string(writeArgs)},
			}},
			// Turn 2: LLM calls run_code_skill to execute it
			{toolCalls: []mockTool{
				{id: "call_2", name: "run_code_skill", args: string(runArgs)},
			}},
			// Turn 3: LLM calls notify with the result
			{toolCalls: []mockTool{
				{id: "call_3", name: "notify", args: string(notifyArgs)},
			}},
			// Turn 4: final text response
			{content: "The sum of 2 and 3 is 5."},
		},
	}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	a := newTestAgent(t, srv)

	// Sandbox provider — real subprocess execution, no sidecar/proxy needed
	provider, err := sandbox.NewProvider(config.SandboxConfig{Provider: "none"}, nil, nil, "")
	if err != nil {
		t.Fatalf("create sandbox provider: %v", err)
	}

	mockNotify := &testNotifyProvider{}

	// Register the three tools the LLM will call
	a.Tools.Register(tools.NewWriteSkill(a.Skills, skillsDir))
	a.Tools.Register(tools.NewRunCodeSkill(a.Skills, provider))
	a.Tools.Register(tools.NewNotify(mockNotify))

	// Track tool calls and results
	var toolCalls []string
	var toolResults []string
	cb := &agent.Callbacks{
		Approve:      func(name, desc string) bool { return true },
		OnToolCall:   func(name, args string) { toolCalls = append(toolCalls, name) },
		OnToolResult: func(name, result string) { toolResults = append(toolResults, name+": "+result) },
	}

	result, err := a.Chat(context.Background(), "Please add 2 and 3 together", cb)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// --- Assertions ---

	// Final response
	if result != "The sum of 2 and 3 is 5." {
		t.Errorf("result = %q, want %q", result, "The sum of 2 and 3 is 5.")
	}

	// Tool call sequence
	wantCalls := []string{"write_skill", "run_code_skill", "notify"}
	if len(toolCalls) != len(wantCalls) {
		t.Fatalf("tool calls = %v, want %v", toolCalls, wantCalls)
	}
	for i, want := range wantCalls {
		if toolCalls[i] != want {
			t.Errorf("tool call [%d] = %q, want %q", i, toolCalls[i], want)
		}
	}

	// LLM made 4 requests (write + run + notify + final text)
	if mock.calls != 4 {
		t.Errorf("LLM calls = %d, want 4", mock.calls)
	}

	// Skill files created on disk
	codePath := filepath.Join(skillsDir, "add-numbers", "add.sh")
	if _, err := os.Stat(codePath); err != nil {
		t.Errorf("skill code file not found: %v", err)
	}
	manifestPath := filepath.Join(skillsDir, "add-numbers", "manifest.toml")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Errorf("skill manifest not found: %v", err)
	}

	// Skill registered in the live registry
	if _, ok := a.Skills.Get("add-numbers"); !ok {
		t.Error("skill not found in registry after write_skill")
	}

	// run_code_skill actually executed the bash script and got the sum
	foundRun := false
	for _, r := range toolResults {
		if len(r) > len("run_code_skill: ") && r[:len("run_code_skill: ")] == "run_code_skill: " {
			foundRun = true
			body := r[len("run_code_skill: "):]
			if !contains(body, "success") {
				t.Errorf("run_code_skill did not succeed: %s", body)
			}
			if !contains(body, "5") {
				t.Errorf("run_code_skill output missing sum '5': %s", body)
			}
		}
	}
	if !foundRun {
		t.Error("run_code_skill result not captured")
	}

	// Notification was sent with the right content
	if mockNotify.lastTitle != "Addition Result" {
		t.Errorf("notify title = %q, want %q", mockNotify.lastTitle, "Addition Result")
	}
	if mockNotify.lastMessage != "The sum of 2 and 3 is 5" {
		t.Errorf("notify message = %q, want %q", mockNotify.lastMessage, "The sum of 2 and 3 is 5")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
