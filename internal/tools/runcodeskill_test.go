package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"smithly.dev/internal/sandbox"
	"smithly.dev/internal/skills"
	"smithly.dev/internal/tools"
)

type mockProvider struct {
	result *sandbox.RunResult
	err    error
	opts   sandbox.RunOpts // captured for assertions
}

func (m *mockProvider) Name() string                  { return "mock" }
func (m *mockProvider) Available() (bool, string)     { return true, "mock provider" }
func (m *mockProvider) Run(_ context.Context, opts sandbox.RunOpts) (*sandbox.RunResult, error) {
	m.opts = opts
	return m.result, m.err
}

func TestRunCodeSkillNotFound(t *testing.T) {
	reg := skills.NewRegistry()
	reg.Add(&skills.Skill{
		Manifest: skills.Manifest{Skill: skills.SkillMeta{Name: "other-skill"}},
	})

	tool := tools.NewRunCodeSkill(reg, &mockProvider{})
	result, err := tool.Run(context.Background(), json.RawMessage(`{"name":"missing"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found', got %q", result)
	}
	if !strings.Contains(result, "other-skill") {
		t.Errorf("expected available skills listed, got %q", result)
	}
}

func TestRunCodeSkillInstructionSkill(t *testing.T) {
	reg := skills.NewRegistry()
	reg.Add(&skills.Skill{
		Manifest: skills.Manifest{Skill: skills.SkillMeta{Name: "my-instruction", Type: "instruction"}},
	})

	tool := tools.NewRunCodeSkill(reg, &mockProvider{})
	result, err := tool.Run(context.Background(), json.RawMessage(`{"name":"my-instruction"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "not a code skill") {
		t.Errorf("expected 'not a code skill', got %q", result)
	}
}

func TestRunCodeSkillSuccess(t *testing.T) {
	reg := skills.NewRegistry()
	reg.Add(&skills.Skill{
		Manifest: skills.Manifest{
			Skill: skills.SkillMeta{Name: "echo-skill", Type: "code"},
			Code:  &skills.CodeSkillConfig{Runtime: "bash", Entrypoint: "run.sh"},
		},
	})

	mp := &mockProvider{
		result: &sandbox.RunResult{Output: "hello world\n", ExitCode: 0},
	}
	tool := tools.NewRunCodeSkill(reg, mp)
	result, err := tool.Run(context.Background(), json.RawMessage(`{"name":"echo-skill"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "success") {
		t.Errorf("expected 'success', got %q", result)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("expected output, got %q", result)
	}
}

func TestRunCodeSkillNonZeroExit(t *testing.T) {
	reg := skills.NewRegistry()
	reg.Add(&skills.Skill{
		Manifest: skills.Manifest{
			Skill: skills.SkillMeta{Name: "fail-skill", Type: "code"},
			Code:  &skills.CodeSkillConfig{Runtime: "bash", Entrypoint: "run.sh"},
		},
	})

	mp := &mockProvider{
		result: &sandbox.RunResult{ExitCode: 1, Error: "something broke\n"},
	}
	tool := tools.NewRunCodeSkill(reg, mp)
	result, err := tool.Run(context.Background(), json.RawMessage(`{"name":"fail-skill"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Exit code: 1") {
		t.Errorf("expected exit code 1, got %q", result)
	}
	if !strings.Contains(result, "something broke") {
		t.Errorf("expected stderr, got %q", result)
	}
}

func TestRunCodeSkillNilInput(t *testing.T) {
	reg := skills.NewRegistry()
	reg.Add(&skills.Skill{
		Manifest: skills.Manifest{
			Skill: skills.SkillMeta{Name: "input-skill", Type: "code"},
			Code:  &skills.CodeSkillConfig{Runtime: "bash", Entrypoint: "run.sh"},
		},
	})

	mp := &mockProvider{
		result: &sandbox.RunResult{Output: "ok", ExitCode: 0},
	}
	tool := tools.NewRunCodeSkill(reg, mp)

	// No input field at all
	_, err := tool.Run(context.Background(), json.RawMessage(`{"name":"input-skill"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify default input was passed
	if string(mp.opts.Input) != "{}" {
		t.Errorf("expected default input {}, got %s", string(mp.opts.Input))
	}
}

func TestRunCodeSkillMetadata(t *testing.T) {
	tool := tools.NewRunCodeSkill(skills.NewRegistry(), &mockProvider{})
	if tool.Name() != "run_code_skill" {
		t.Errorf("name = %q", tool.Name())
	}
	if !tool.NeedsApproval() {
		t.Error("run_code_skill should need approval")
	}

	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("invalid schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v", schema["type"])
	}
}
