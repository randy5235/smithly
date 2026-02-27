package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnerBasicScript(t *testing.T) {
	dir := t.TempDir()

	// Write a simple bash script
	script := `#!/bin/bash
read input
echo "{\"greeting\": \"hello from bash\"}"
`
	os.WriteFile(filepath.Join(dir, "main.sh"), []byte(script), 0755)

	skill := &Skill{
		Path: dir,
		Manifest: Manifest{
			Skill: SkillMeta{Name: "test", Type: "code"},
			Code: &CodeSkillConfig{
				Runtime:    "bash",
				Entrypoint: "main.sh",
			},
		},
	}

	runner := NewRunner(5*time.Second, nil, nil)
	result, err := runner.Run(context.Background(), skill, json.RawMessage(`{}`), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, stderr: %s", result.ExitCode, result.Error)
	}
	if result.Output == "" {
		t.Error("expected output from script")
	}
}

func TestRunnerEnvVars(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/bash
echo "$TEST_TOKEN"
`
	os.WriteFile(filepath.Join(dir, "main.sh"), []byte(script), 0755)

	skill := &Skill{
		Path: dir,
		Manifest: Manifest{
			Skill: SkillMeta{Name: "test", Type: "code"},
			Code: &CodeSkillConfig{
				Runtime:    "bash",
				Entrypoint: "main.sh",
			},
		},
	}

	runner := NewRunner(5*time.Second, nil, nil)
	env := []string{"TEST_TOKEN=secret-123", "PATH=" + os.Getenv("PATH")}
	result, err := runner.Run(context.Background(), skill, nil, env)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "secret-123\n" {
		t.Errorf("output = %q, want %q", result.Output, "secret-123\n")
	}
}

func TestRunnerNonZeroExit(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/bash
echo "error output" >&2
exit 1
`
	os.WriteFile(filepath.Join(dir, "main.sh"), []byte(script), 0755)

	skill := &Skill{
		Path: dir,
		Manifest: Manifest{
			Skill: SkillMeta{Name: "test", Type: "code"},
			Code: &CodeSkillConfig{
				Runtime:    "bash",
				Entrypoint: "main.sh",
			},
		},
	}

	runner := NewRunner(5*time.Second, nil, nil)
	result, err := runner.Run(context.Background(), skill, nil, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}
	if result.Error == "" {
		t.Error("expected stderr output")
	}
}

func TestRunnerNotCodeSkill(t *testing.T) {
	skill := &Skill{
		Manifest: Manifest{
			Skill: SkillMeta{Name: "test", Type: "instruction"},
		},
	}

	runner := NewRunner(5*time.Second, nil, nil)
	_, err := runner.Run(context.Background(), skill, nil, nil)
	if err == nil {
		t.Error("expected error for non-code skill")
	}
}

func TestRunnerMissingCodeConfig(t *testing.T) {
	skill := &Skill{
		Manifest: Manifest{
			Skill: SkillMeta{Name: "test", Type: "code"},
			// No Code config
		},
	}

	runner := NewRunner(5*time.Second, nil, nil)
	_, err := runner.Run(context.Background(), skill, nil, nil)
	if err == nil {
		t.Error("expected error for missing code config")
	}
}

func TestRunnerTimeout(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/bash
sleep 60
`
	os.WriteFile(filepath.Join(dir, "main.sh"), []byte(script), 0755)

	skill := &Skill{
		Path: dir,
		Manifest: Manifest{
			Skill: SkillMeta{Name: "test", Type: "code"},
			Code: &CodeSkillConfig{
				Runtime:    "bash",
				Entrypoint: "main.sh",
			},
		},
	}

	start := time.Now()
	runner := NewRunner(200*time.Millisecond, nil, nil)
	result, err := runner.Run(context.Background(), skill, nil, os.Environ())
	elapsed := time.Since(start)

	// Should complete quickly (not wait for sleep 60)
	if elapsed > 5*time.Second {
		t.Errorf("took %v, expected quick timeout", elapsed)
	}
	// Should complete with error or non-zero exit
	if err == nil && result.ExitCode == 0 {
		t.Error("expected timeout to cause error or non-zero exit")
	}
}

func TestRunnerDefaultTimeout(t *testing.T) {
	runner := NewRunner(0, nil, nil)
	if runner.timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want 30s", runner.timeout)
	}
}

func TestRunnerSidecarEnvInjection(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/bash
echo "API=$SMITHLY_API TOKEN=$SMITHLY_TOKEN"
`
	os.WriteFile(filepath.Join(dir, "main.sh"), []byte(script), 0755)

	skill := &Skill{
		Path: dir,
		Manifest: Manifest{
			Skill: SkillMeta{Name: "test-skill", Type: "code"},
			Code: &CodeSkillConfig{
				Runtime:    "bash",
				Entrypoint: "main.sh",
			},
		},
	}

	sc := &mockSidecar{url: "http://127.0.0.1:18791"}
	runner := NewRunner(5*time.Second, sc, nil)
	env := []string{"PATH=" + os.Getenv("PATH")}
	result, err := runner.Run(context.Background(), skill, nil, env)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, stderr: %s", result.ExitCode, result.Error)
	}
	// Output should contain the sidecar URL and a token
	if !contains(result.Output, "API=http://127.0.0.1:18791") {
		t.Errorf("output missing SMITHLY_API: %q", result.Output)
	}
	if !contains(result.Output, "TOKEN=mock-token-") {
		t.Errorf("output missing SMITHLY_TOKEN: %q", result.Output)
	}
	if !sc.revoked {
		t.Error("expected token to be revoked after run")
	}
}

func TestRunnerProxyEnvInjection(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/bash
echo "HTTP=$HTTP_PROXY HTTPS=$HTTPS_PROXY http=$http_proxy https=$https_proxy"
`
	os.WriteFile(filepath.Join(dir, "main.sh"), []byte(script), 0755)

	skill := &Skill{
		Path: dir,
		Manifest: Manifest{
			Skill: SkillMeta{Name: "test", Type: "code"},
			Code: &CodeSkillConfig{
				Runtime:    "bash",
				Entrypoint: "main.sh",
			},
		},
	}

	runner := NewRunner(5*time.Second, nil, nil)
	runner.SetProxy("127.0.0.1:18792")
	env := []string{"PATH=" + os.Getenv("PATH")}
	result, err := runner.Run(context.Background(), skill, nil, env)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, stderr: %s", result.ExitCode, result.Error)
	}

	expected := "http://127.0.0.1:18792"
	if !contains(result.Output, "HTTP="+expected) {
		t.Errorf("output missing HTTP_PROXY: %q", result.Output)
	}
	if !contains(result.Output, "HTTPS="+expected) {
		t.Errorf("output missing HTTPS_PROXY: %q", result.Output)
	}
	if !contains(result.Output, "http="+expected) {
		t.Errorf("output missing http_proxy: %q", result.Output)
	}
	if !contains(result.Output, "https="+expected) {
		t.Errorf("output missing https_proxy: %q", result.Output)
	}
}

func TestRunnerNoProxyWhenUnset(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/bash
echo "HTTP=$HTTP_PROXY"
`
	os.WriteFile(filepath.Join(dir, "main.sh"), []byte(script), 0755)

	skill := &Skill{
		Path: dir,
		Manifest: Manifest{
			Skill: SkillMeta{Name: "test", Type: "code"},
			Code: &CodeSkillConfig{
				Runtime:    "bash",
				Entrypoint: "main.sh",
			},
		},
	}

	runner := NewRunner(5*time.Second, nil, nil)
	// No SetProxy call
	env := []string{"PATH=" + os.Getenv("PATH")}
	result, err := runner.Run(context.Background(), skill, nil, env)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "HTTP=\n" {
		t.Errorf("expected empty HTTP_PROXY, got %q", result.Output)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

type mockSidecar struct {
	url     string
	revoked bool
}

func (m *mockSidecar) IssueToken(skill string, ttl time.Duration) string {
	return "mock-token-" + skill
}

func (m *mockSidecar) RevokeToken(token string) {
	m.revoked = true
}

func (m *mockSidecar) URL() string {
	return m.url
}
