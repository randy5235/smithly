package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"smithly.dev/internal/config"
)

// CodeSkillConfig is the manifest section for code skills.
type CodeSkillConfig struct {
	Runtime    string `toml:"runtime"`    // "python3", "bash", "node", "bun", "go"
	Entrypoint string `toml:"entrypoint"` // "main.py", "./skill", etc.
	Build      string `toml:"build"`      // optional build command ("go build -o skill .")
}

// RunResult holds the output of a code skill execution.
type RunResult struct {
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// SidecarIface is the subset of sidecar.Sidecar used by the runner.
type SidecarIface interface {
	IssueToken(skill string, ttl time.Duration) string
	RevokeToken(token string)
	URL() string
}

// Runner executes code skills as subprocesses.
type Runner struct {
	timeout    time.Duration
	sidecar    SidecarIface
	dataStores []config.DataStoreConfig
}

// NewRunner creates a code skill runner with the given execution timeout.
func NewRunner(timeout time.Duration, sidecar SidecarIface, stores []config.DataStoreConfig) *Runner {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Runner{
		timeout:    timeout,
		sidecar:    sidecar,
		dataStores: stores,
	}
}

// Run executes a code skill with JSON input on stdin and captures JSON output on stdout.
// env is a list of "KEY=VALUE" strings for environment variables (OAuth2 tokens, notify URL, etc.)
func (r *Runner) Run(ctx context.Context, skill *Skill, input json.RawMessage, env []string) (*RunResult, error) {
	if skill.Manifest.Skill.Type != "code" {
		return nil, fmt.Errorf("skill %q is not a code skill", skill.Manifest.Skill.Name)
	}

	cfg := skill.Manifest.Code
	if cfg == nil {
		return nil, fmt.Errorf("skill %q missing [code] section in manifest", skill.Manifest.Skill.Name)
	}

	// Build step if configured
	if cfg.Build != "" {
		if err := r.build(ctx, skill.Path, cfg.Build, env); err != nil {
			return nil, fmt.Errorf("build: %w", err)
		}
	}

	// Inject sidecar env vars
	if r.sidecar != nil {
		token := r.sidecar.IssueToken(skill.Manifest.Skill.Name, r.timeout+30*time.Second)
		defer r.sidecar.RevokeToken(token)
		env = append(env,
			"SMITHLY_API="+r.sidecar.URL(),
			"SMITHLY_TOKEN="+token,
		)
	}

	// Inject data store env vars — skill connects directly
	dbTypeSet := false
	for _, ds := range r.dataStores {
		prefix := "SMITHLY_" + strings.ToUpper(ds.Type)
		switch ds.Type {
		case "sqlite":
			env = append(env, prefix+"_PATH="+ds.Path)
		default:
			env = append(env, prefix+"_URL="+ds.URL)
		}
		if !dbTypeSet {
			env = append(env, "SMITHLY_DB_TYPE="+ds.Type)
			dbTypeSet = true
		}
	}

	// Determine command to run
	var cmd *exec.Cmd
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if cfg.Runtime != "" {
		cmd = exec.CommandContext(ctx, cfg.Runtime, cfg.Entrypoint)
	} else {
		cmd = exec.CommandContext(ctx, cfg.Entrypoint)
	}

	cmd.Dir = skill.Path
	cmd.Env = env
	// Create a new process group so we can kill all child processes on timeout
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	// Pass input on stdin
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := &RunResult{
		Output: stdout.String(),
	}

	if stderr.Len() > 0 {
		result.Error = stderr.String()
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("run skill: %w", err)
		}
	}

	return result, nil
}

// build runs the build command for a compiled code skill.
func (r *Runner) build(ctx context.Context, dir, buildCmd string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", buildCmd)
	cmd.Dir = dir
	cmd.Env = env

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s", err, stderr.String())
	}
	return nil
}
