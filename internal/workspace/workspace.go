// Package workspace loads agent workspace files (SOUL.md, IDENTITY.toml, etc.)
// and assembles them into a system prompt.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Identity holds the agent's external presentation from IDENTITY.toml.
type Identity struct {
	Name   string `toml:"name"`
	Emoji  string `toml:"emoji"`
	Avatar string `toml:"avatar"`
}

// Workspace represents the loaded workspace files for a single agent.
type Workspace struct {
	Path         string
	Soul         string   // SOUL.md contents
	Identity     Identity // IDENTITY.toml
	User         string   // USER.md contents
	Instructions string   // INSTRUCTIONS.md contents
	Boot         string   // BOOT.md — sent as first message on startup
	Heartbeat    string   // HEARTBEAT.md — sent periodically
}

// Load reads all workspace files from the given directory.
// Missing files are treated as empty (not an error).
func Load(dir string) (*Workspace, error) {
	w := &Workspace{Path: dir}

	w.Soul = readFileOr(filepath.Join(dir, "SOUL.md"), "")
	w.User = readFileOr(filepath.Join(dir, "USER.md"), "")
	w.Instructions = readFileOr(filepath.Join(dir, "INSTRUCTIONS.md"), "")
	w.Boot = readFileOr(filepath.Join(dir, "BOOT.md"), "")
	w.Heartbeat = readFileOr(filepath.Join(dir, "HEARTBEAT.md"), "")

	identityPath := filepath.Join(dir, "IDENTITY.toml")
	if data, err := os.ReadFile(identityPath); err == nil {
		if _, err := toml.Decode(string(data), &w.Identity); err != nil {
			return nil, fmt.Errorf("parse %s: %w", identityPath, err)
		}
	}

	return w, nil
}

// SystemPrompt assembles the workspace files into a single system prompt.
func (w *Workspace) SystemPrompt() string {
	var parts []string

	if w.Soul != "" {
		parts = append(parts, "## Soul\n\n"+w.Soul)
	}

	if w.Identity.Name != "" {
		id := fmt.Sprintf("## Identity\n\nName: %s", w.Identity.Name)
		if w.Identity.Emoji != "" {
			id += "\nEmoji: " + w.Identity.Emoji
		}
		parts = append(parts, id)
	}

	if w.User != "" {
		parts = append(parts, "## User\n\n"+w.User)
	}

	if w.Instructions != "" {
		parts = append(parts, "## Instructions\n\n"+w.Instructions)
	}

	if len(parts) == 0 {
		return "You are a helpful AI assistant."
	}

	return strings.Join(parts, "\n\n---\n\n")
}

func readFileOr(path, fallback string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	return strings.TrimSpace(string(data))
}
