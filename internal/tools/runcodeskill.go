package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"smithly.dev/internal/sandbox"
	"smithly.dev/internal/skills"
)

// RunCodeSkill lets the agent execute a code skill by name.
type RunCodeSkill struct {
	registry   *skills.Registry
	codeRunner sandbox.Provider
}

// NewRunCodeSkill creates a run_code_skill tool.
func NewRunCodeSkill(registry *skills.Registry, codeRunner sandbox.Provider) *RunCodeSkill {
	return &RunCodeSkill{registry: registry, codeRunner: codeRunner}
}

func (r *RunCodeSkill) Name() string { return "run_code_skill" }
func (r *RunCodeSkill) Description() string {
	return "Execute a code skill by name. Returns the skill's stdout, stderr, and exit code."
}
func (r *RunCodeSkill) NeedsApproval() bool { return true }

func (r *RunCodeSkill) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "The code skill name to execute"
			},
			"input": {
				"type": "object",
				"description": "JSON input passed to the skill on stdin (defaults to {})"
			}
		},
		"required": ["name"]
	}`)
}

func (r *RunCodeSkill) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if params.Name == "" {
		return "", fmt.Errorf("name is required")
	}

	skill, ok := r.registry.Get(params.Name)
	if !ok {
		var names []string
		for _, s := range r.registry.All() {
			names = append(names, s.Manifest.Skill.Name)
		}
		return fmt.Sprintf("Skill %q not found. Available skills: %v", params.Name, names), nil
	}

	if skill.Manifest.Skill.Type != "code" {
		return fmt.Sprintf("Skill %q is not a code skill (type: %q). Use read_skill instead.", params.Name, skill.Manifest.Skill.Type), nil
	}

	// Default input to {}
	input := params.Input
	if input == nil {
		input = json.RawMessage(`{}`)
	}

	result, err := r.codeRunner.Run(ctx, sandbox.RunOpts{
		Skill: skill,
		Input: input,
	})
	if err != nil {
		return "", fmt.Errorf("run skill: %w", err)
	}

	return formatRunResult(result), nil
}

const maxOutputLen = 50 * 1024 // 50KB

func formatRunResult(r *sandbox.RunResult) string {
	var sb strings.Builder

	if r.ExitCode == 0 {
		sb.WriteString("Exit code: 0 (success)\n")
	} else {
		fmt.Fprintf(&sb, "Exit code: %d\n", r.ExitCode)
	}

	if r.Output != "" {
		out := r.Output
		if len(out) > maxOutputLen {
			out = out[:maxOutputLen] + "\n... (truncated)"
		}
		fmt.Fprintf(&sb, "\n--- stdout ---\n%s", out)
	}

	if r.Error != "" {
		errOut := r.Error
		if len(errOut) > maxOutputLen {
			errOut = errOut[:maxOutputLen] + "\n... (truncated)"
		}
		fmt.Fprintf(&sb, "\n--- stderr ---\n%s", errOut)
	}

	return sb.String()
}
