package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"smithly.dev/internal/skills"
	"smithly.dev/internal/tools"
)

func TestWriteSkillCreatesFiles(t *testing.T) {
	dir := t.TempDir()
	reg := skills.NewRegistry()
	tool := tools.NewWriteSkill(reg, dir)

	result, err := tool.Run(context.Background(), json.RawMessage(`{
		"name": "my-skill",
		"description": "A test skill",
		"runtime": "bash",
		"entrypoint": "run.sh",
		"code": "#!/bin/bash\necho hello"
	}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result, "created and registered") {
		t.Errorf("result = %q", result)
	}

	// Verify manifest.toml exists
	manifest, err := os.ReadFile(filepath.Join(dir, "my-skill", "manifest.toml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !strings.Contains(string(manifest), `name = "my-skill"`) {
		t.Errorf("manifest missing name: %s", manifest)
	}
	if !strings.Contains(string(manifest), `type = "code"`) {
		t.Errorf("manifest missing type: %s", manifest)
	}
	if !strings.Contains(string(manifest), `runtime = "bash"`) {
		t.Errorf("manifest missing runtime: %s", manifest)
	}

	// Verify code file exists
	code, err := os.ReadFile(filepath.Join(dir, "my-skill", "run.sh"))
	if err != nil {
		t.Fatalf("read code: %v", err)
	}
	if string(code) != "#!/bin/bash\necho hello" {
		t.Errorf("code = %q", string(code))
	}
}

func TestWriteSkillLoadsIntoRegistry(t *testing.T) {
	dir := t.TempDir()
	reg := skills.NewRegistry()
	tool := tools.NewWriteSkill(reg, dir)

	_, err := tool.Run(context.Background(), json.RawMessage(`{
		"name": "reg-test",
		"description": "Registry test",
		"runtime": "bash",
		"entrypoint": "main.sh",
		"code": "echo hi"
	}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	s, ok := reg.Get("reg-test")
	if !ok {
		t.Fatal("skill not found in registry")
	}
	if s.Manifest.Skill.Type != "code" {
		t.Errorf("type = %q, want code", s.Manifest.Skill.Type)
	}
	if s.Manifest.Code.Runtime != "bash" {
		t.Errorf("runtime = %q", s.Manifest.Code.Runtime)
	}
}

func TestWriteSkillOverwrite(t *testing.T) {
	dir := t.TempDir()
	reg := skills.NewRegistry()
	tool := tools.NewWriteSkill(reg, dir)

	// Create initial skill
	_, err := tool.Run(context.Background(), json.RawMessage(`{
		"name": "overwrite-test",
		"description": "v1",
		"runtime": "bash",
		"entrypoint": "run.sh",
		"code": "echo v1"
	}`))
	if err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Overwrite with new version
	_, err = tool.Run(context.Background(), json.RawMessage(`{
		"name": "overwrite-test",
		"description": "v2",
		"runtime": "bash",
		"entrypoint": "run.sh",
		"code": "echo v2"
	}`))
	if err != nil {
		t.Fatalf("second write: %v", err)
	}

	s, ok := reg.Get("overwrite-test")
	if !ok {
		t.Fatal("skill not found after overwrite")
	}
	if s.Manifest.Skill.Description != "v2" {
		t.Errorf("description = %q, want v2", s.Manifest.Skill.Description)
	}

	code, _ := os.ReadFile(filepath.Join(dir, "overwrite-test", "run.sh"))
	if string(code) != "echo v2" {
		t.Errorf("code = %q, want 'echo v2'", string(code))
	}
}

func TestWriteSkillInvalidName(t *testing.T) {
	dir := t.TempDir()
	reg := skills.NewRegistry()
	tool := tools.NewWriteSkill(reg, dir)

	_, err := tool.Run(context.Background(), json.RawMessage(`{
		"name": "../escape",
		"description": "bad",
		"runtime": "bash",
		"entrypoint": "run.sh",
		"code": "echo bad"
	}`))
	if err == nil {
		t.Fatal("expected error for invalid name")
	}
	if !strings.Contains(err.Error(), "invalid skill name") {
		t.Errorf("error = %v", err)
	}
}

func TestWriteSkillPathTraversalEntrypoint(t *testing.T) {
	dir := t.TempDir()
	reg := skills.NewRegistry()
	tool := tools.NewWriteSkill(reg, dir)

	_, err := tool.Run(context.Background(), json.RawMessage(`{
		"name": "safe-name",
		"description": "bad entrypoint",
		"runtime": "bash",
		"entrypoint": "../../etc/passwd",
		"code": "echo bad"
	}`))
	if err == nil {
		t.Fatal("expected error for path traversal entrypoint")
	}
	if !strings.Contains(err.Error(), "invalid entrypoint") {
		t.Errorf("error = %v", err)
	}
}

func TestWriteSkillWithTriggers(t *testing.T) {
	dir := t.TempDir()
	reg := skills.NewRegistry()
	tool := tools.NewWriteSkill(reg, dir)

	_, err := tool.Run(context.Background(), json.RawMessage(`{
		"name": "trigger-test",
		"description": "Has triggers",
		"runtime": "bash",
		"entrypoint": "run.sh",
		"code": "echo triggered",
		"triggers": [
			{"type": "keyword", "pattern": "deploy"},
			{"type": "always"}
		]
	}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	manifest, err := os.ReadFile(filepath.Join(dir, "trigger-test", "manifest.toml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	content := string(manifest)
	if !strings.Contains(content, `type = "keyword"`) {
		t.Errorf("manifest missing keyword trigger: %s", content)
	}
	if !strings.Contains(content, `pattern = "deploy"`) {
		t.Errorf("manifest missing trigger pattern: %s", content)
	}
	if !strings.Contains(content, `type = "always"`) {
		t.Errorf("manifest missing always trigger: %s", content)
	}
}

func TestWriteSkillWithBuild(t *testing.T) {
	dir := t.TempDir()
	reg := skills.NewRegistry()
	tool := tools.NewWriteSkill(reg, dir)

	_, err := tool.Run(context.Background(), json.RawMessage(`{
		"name": "build-test",
		"description": "Has build command",
		"runtime": "go",
		"entrypoint": "main.go",
		"code": "package main\nfunc main() {}",
		"build": "go build -o skill ."
	}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	manifest, err := os.ReadFile(filepath.Join(dir, "build-test", "manifest.toml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !strings.Contains(string(manifest), `build = "go build -o skill ."`) {
		t.Errorf("manifest missing build: %s", manifest)
	}
}

func TestWriteSkillMissingRequiredFields(t *testing.T) {
	dir := t.TempDir()
	reg := skills.NewRegistry()
	tool := tools.NewWriteSkill(reg, dir)

	cases := []struct {
		name string
		args string
	}{
		{"missing name", `{"description":"d","runtime":"bash","entrypoint":"r.sh","code":"echo"}`},
		{"missing description", `{"name":"x","runtime":"bash","entrypoint":"r.sh","code":"echo"}`},
		{"missing runtime", `{"name":"x","description":"d","entrypoint":"r.sh","code":"echo"}`},
		{"missing entrypoint", `{"name":"x","description":"d","runtime":"bash","code":"echo"}`},
		{"missing code", `{"name":"x","description":"d","runtime":"bash","entrypoint":"r.sh"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tool.Run(context.Background(), json.RawMessage(tc.args))
			if err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestWriteSkillMetadata(t *testing.T) {
	tool := tools.NewWriteSkill(skills.NewRegistry(), t.TempDir())
	if tool.Name() != "write_skill" {
		t.Errorf("name = %q", tool.Name())
	}
	if !tool.NeedsApproval() {
		t.Error("write_skill should need approval")
	}

	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("invalid schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v", schema["type"])
	}
}
