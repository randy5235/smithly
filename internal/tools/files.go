package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// errPathEscape is returned when a path tries to escape the root directory.
var errPathEscape = fmt.Errorf("path escapes root directory")

// safePath resolves a user-provided path against rootDir, preventing
// traversal outside the root via absolute paths or "../" sequences.
// When rootDir is empty, any path is allowed (no sandbox).
func safePath(rootDir, path string) (string, error) {
	if rootDir == "" {
		return path, nil
	}
	if filepath.IsAbs(path) {
		return "", errPathEscape
	}
	resolved := filepath.Clean(filepath.Join(rootDir, path))

	// Ensure the resolved path is still under rootDir.
	root := filepath.Clean(rootDir) + string(os.PathSeparator)
	if !strings.HasPrefix(resolved+string(os.PathSeparator), root) && resolved != filepath.Clean(rootDir) {
		return "", errPathEscape
	}
	return resolved, nil
}

// ReadFile reads a file and returns its contents.
type ReadFile struct {
	rootDir string // if set, paths are relative to this directory
}

func NewReadFile(rootDir string) *ReadFile {
	return &ReadFile{rootDir: rootDir}
}

func (r *ReadFile) Name() string        { return "read_file" }
func (r *ReadFile) Description() string { return "Read the contents of a file." }
func (r *ReadFile) NeedsApproval() bool { return false }

func (r *ReadFile) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Path to the file to read"
			}
		},
		"required": ["path"]
	}`)
}

func (r *ReadFile) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	path, err := safePath(r.rootDir, params.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	content := string(data)
	if len(content) > 100000 {
		content = content[:100000] + "\n\n[truncated — file too large]"
	}
	return content, nil
}

// WriteFile writes content to a file. Requires approval.
type WriteFile struct {
	rootDir string
}

func NewWriteFile(rootDir string) *WriteFile {
	return &WriteFile{rootDir: rootDir}
}

func (w *WriteFile) Name() string        { return "write_file" }
func (w *WriteFile) Description() string { return "Write content to a file. Creates parent directories if needed." }
func (w *WriteFile) NeedsApproval() bool { return true }

func (w *WriteFile) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Path to the file to write"
			},
			"content": {
				"type": "string",
				"description": "Content to write to the file"
			}
		},
		"required": ["path", "content"]
	}`)
}

func (w *WriteFile) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	path, err := safePath(w.rootDir, params.Path)
	if err != nil {
		return "", err
	}

	// Create parent directories
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}

	if err := os.WriteFile(path, []byte(params.Content), 0o644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	return fmt.Sprintf("Wrote %d bytes to %s", len(params.Content), params.Path), nil
}

// ListFiles lists files in a directory.
type ListFiles struct {
	rootDir string
}

func NewListFiles(rootDir string) *ListFiles {
	return &ListFiles{rootDir: rootDir}
}

func (l *ListFiles) Name() string        { return "list_files" }
func (l *ListFiles) Description() string { return "List files and directories at a given path." }
func (l *ListFiles) NeedsApproval() bool { return false }

func (l *ListFiles) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Directory path to list. Defaults to current directory."
			}
		}
	}`)
}

func (l *ListFiles) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	path := params.Path
	if path == "" {
		path = "."
	}
	path, err := safePath(l.rootDir, path)
	if err != nil {
		return "", err
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("list directory: %w", err)
	}

	var out strings.Builder
	for _, entry := range entries {
		info, _ := entry.Info()
		switch {
		case entry.IsDir():
			fmt.Fprintf(&out, "  %s/\n", entry.Name())
		case info != nil:
			fmt.Fprintf(&out, "  %s (%d bytes)\n", entry.Name(), info.Size())
		default:
			fmt.Fprintf(&out, "  %s\n", entry.Name())
		}
	}

	if out.Len() == 0 {
		return "(empty directory)", nil
	}
	return out.String(), nil
}
