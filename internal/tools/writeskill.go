package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"smithly.dev/internal/skills"
)

var validSkillName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// WriteSkill lets the agent create a code skill on disk and load it into the live registry.
type WriteSkill struct {
	registry  *skills.Registry
	skillsDir string
}

// NewWriteSkill creates a write_skill tool that writes to the given skills directory.
func NewWriteSkill(registry *skills.Registry, skillsDir string) *WriteSkill {
	return &WriteSkill{registry: registry, skillsDir: skillsDir}
}

func (w *WriteSkill) Name() string { return "write_skill" }
func (w *WriteSkill) Description() string {
	return "Create a code skill on disk and register it. The skill can then be executed with run_code_skill or set as a heartbeat skill."
}
func (w *WriteSkill) NeedsApproval() bool { return true }

func (w *WriteSkill) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "Skill name (alphanumeric, hyphens, underscores)"
			},
			"description": {
				"type": "string",
				"description": "Short description of what the skill does"
			},
			"runtime": {
				"type": "string",
				"description": "Runtime: python3, bash, node, bun, go"
			},
			"entrypoint": {
				"type": "string",
				"description": "Script filename to execute (e.g. main.py, run.sh)"
			},
			"code": {
				"type": "string",
				"description": "The source code for the entrypoint file"
			},
			"build": {
				"type": "string",
				"description": "Optional build command (e.g. go build -o skill .)"
			},
			"triggers": {
				"type": "array",
				"description": "Optional triggers for the skill",
				"items": {
					"type": "object",
					"properties": {
						"type": { "type": "string", "description": "keyword, regex, or always" },
						"pattern": { "type": "string", "description": "Trigger pattern" }
					},
					"required": ["type"]
				}
			}
		},
		"required": ["name", "description", "runtime", "entrypoint", "code"]
	}`)
}

func (w *WriteSkill) Run(_ context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Runtime     string `json:"runtime"`
		Entrypoint  string `json:"entrypoint"`
		Code        string `json:"code"`
		Build       string `json:"build"`
		Triggers    []struct {
			Type    string `json:"type"`
			Pattern string `json:"pattern"`
		} `json:"triggers"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	// Validate required fields
	if params.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	if params.Description == "" {
		return "", fmt.Errorf("description is required")
	}
	if params.Runtime == "" {
		return "", fmt.Errorf("runtime is required")
	}
	if params.Entrypoint == "" {
		return "", fmt.Errorf("entrypoint is required")
	}
	if params.Code == "" {
		return "", fmt.Errorf("code is required")
	}

	// Security: validate name
	if !validSkillName.MatchString(params.Name) {
		return "", fmt.Errorf("invalid skill name %q: must match [a-zA-Z0-9_-]+", params.Name)
	}

	// Security: reject path traversal in entrypoint
	if strings.Contains(params.Entrypoint, "..") || strings.ContainsAny(params.Entrypoint, "/\\") {
		return "", fmt.Errorf("invalid entrypoint %q: must be a simple filename, no path separators or '..'", params.Entrypoint)
	}

	// Create skill directory
	skillDir := filepath.Join(w.skillsDir, params.Name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return "", fmt.Errorf("create skill dir: %w", err)
	}

	// Build manifest.toml
	var manifest strings.Builder
	fmt.Fprintf(&manifest, "[skill]\nname = %q\ndescription = %q\ntype = \"code\"\n", params.Name, params.Description)
	fmt.Fprintf(&manifest, "\n[code]\nruntime = %q\nentrypoint = %q\n", params.Runtime, params.Entrypoint)
	if params.Build != "" {
		fmt.Fprintf(&manifest, "build = %q\n", params.Build)
	}
	for _, t := range params.Triggers {
		fmt.Fprintf(&manifest, "\n[[triggers]]\ntype = %q\n", t.Type)
		if t.Pattern != "" {
			fmt.Fprintf(&manifest, "pattern = %q\n", t.Pattern)
		}
	}

	// Write manifest.toml
	manifestPath := filepath.Join(skillDir, "manifest.toml")
	if err := os.WriteFile(manifestPath, []byte(manifest.String()), 0644); err != nil {
		return "", fmt.Errorf("write manifest: %w", err)
	}

	// Write code file
	codePath := filepath.Join(skillDir, params.Entrypoint)
	if err := os.WriteFile(codePath, []byte(params.Code), 0755); err != nil {
		return "", fmt.Errorf("write code: %w", err)
	}

	// Remove old skill from registry if it exists (allows iteration)
	w.registry.Remove(params.Name)

	// Load and register the new skill
	skill, err := skills.Load(skillDir)
	if err != nil {
		return "", fmt.Errorf("load skill: %w", err)
	}
	if err := w.registry.Add(skill); err != nil {
		return "", fmt.Errorf("register skill: %w", err)
	}

	return fmt.Sprintf("Skill %q created and registered. Use run_code_skill to test it.", params.Name), nil
}
