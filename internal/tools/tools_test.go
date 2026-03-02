package tools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"smithly.dev/internal/skills"
	"smithly.dev/internal/tools"
)

func TestRegistryBasics(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.NewSearch())
	reg.Register(tools.NewFetch())
	reg.Register(tools.NewBash())

	if len(reg.All()) != 3 {
		t.Fatalf("tools count = %d, want 3", len(reg.All()))
	}

	s, ok := reg.Get("search")
	if !ok {
		t.Fatal("search tool not found")
	}
	if s.Name() != "search" {
		t.Errorf("name = %q", s.Name())
	}

	_, ok = reg.Get("nonexistent")
	if ok {
		t.Error("should not find nonexistent tool")
	}
}

func TestOpenAIToolFormat(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.NewSearch())

	defs := reg.OpenAITools()
	if len(defs) != 1 {
		t.Fatalf("defs = %d, want 1", len(defs))
	}
	if defs[0].Type != "function" {
		t.Errorf("type = %q, want function", defs[0].Type)
	}
	if defs[0].Function.Name != "search" {
		t.Errorf("name = %q", defs[0].Function.Name)
	}

	// Parameters should be valid JSON schema
	var schema map[string]any
	if err := json.Unmarshal(defs[0].Function.Parameters, &schema); err != nil {
		t.Fatalf("invalid parameters schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v", schema["type"])
	}
}

func TestApprovalDenied(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.NewBash()) // bash needs approval

	result, err := reg.Execute(context.Background(), "bash",
		json.RawMessage(`{"command":"echo hi"}`),
		func(name, desc string) bool { return false }, // deny
	)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result != "Tool execution denied by user." {
		t.Errorf("result = %q", result)
	}
}

func TestApprovalApproved(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.NewBash())

	result, err := reg.Execute(context.Background(), "bash",
		json.RawMessage(`{"command":"echo hello_smithly"}`),
		func(name, desc string) bool { return true }, // approve
	)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(result, "hello_smithly") {
		t.Errorf("result = %q, want to contain hello_smithly", result)
	}
}

func TestUnknownTool(t *testing.T) {
	reg := tools.NewRegistry()
	_, err := reg.Execute(context.Background(), "nope", nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

// --- Individual Tool Tests ---

func TestBashTool(t *testing.T) {
	bash := tools.NewBash()

	if !bash.NeedsApproval() {
		t.Error("bash should need approval")
	}

	result, err := bash.Run(context.Background(), json.RawMessage(`{"command":"echo test123"}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(result, "test123") {
		t.Errorf("result = %q", result)
	}
}

func TestBashToolFailure(t *testing.T) {
	bash := tools.NewBash()
	result, err := bash.Run(context.Background(), json.RawMessage(`{"command":"exit 1"}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(result, "exit") {
		t.Errorf("result = %q, should contain exit info", result)
	}
}

func TestReadFileTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world"), 0644)

	rf := tools.NewReadFile("")
	if rf.NeedsApproval() {
		t.Error("read_file should not need approval")
	}

	result, err := rf.Run(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result != "hello world" {
		t.Errorf("result = %q", result)
	}
}

func TestReadFileWithRootDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "data.txt"), []byte("rooted"), 0644)

	rf := tools.NewReadFile(dir)
	result, err := rf.Run(context.Background(), json.RawMessage(`{"path":"data.txt"}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result != "rooted" {
		t.Errorf("result = %q", result)
	}
}

func TestWriteFileTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "out.txt")

	wf := tools.NewWriteFile("")
	if !wf.NeedsApproval() {
		t.Error("write_file should need approval")
	}

	result, err := wf.Run(context.Background(), json.RawMessage(`{"path":"`+path+`","content":"written by smithly"}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(result, "Wrote") {
		t.Errorf("result = %q", result)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "written by smithly" {
		t.Errorf("file content = %q", string(data))
	}
}

func TestListFilesTool(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bb"), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)

	lf := tools.NewListFiles("")
	result, err := lf.Run(context.Background(), json.RawMessage(`{"path":"`+dir+`"}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(result, "a.txt") {
		t.Errorf("missing a.txt: %q", result)
	}
	if !strings.Contains(result, "b.txt") {
		t.Errorf("missing b.txt: %q", result)
	}
	if !strings.Contains(result, "subdir/") {
		t.Errorf("missing subdir/: %q", result)
	}
}

func TestPathTraversalBlocked(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "safe.txt"), []byte("ok"), 0644)

	attacks := []struct {
		name string
		path string
	}{
		{"dotdot relative", "../../../etc/passwd"},
		{"absolute path", "/etc/passwd"},
		{"dotdot with subdir", "sub/../../etc/passwd"},
		{"absolute with dotdot", "/tmp/../etc/passwd"},
	}

	t.Run("ReadFile", func(t *testing.T) {
		rf := tools.NewReadFile(root)
		for _, tc := range attacks {
			t.Run(tc.name, func(t *testing.T) {
				_, err := rf.Run(context.Background(), json.RawMessage(`{"path":"`+tc.path+`"}`))
				if err == nil {
					t.Error("expected path traversal to be blocked")
				}
			})
		}
		// Legit relative path should still work.
		result, err := rf.Run(context.Background(), json.RawMessage(`{"path":"safe.txt"}`))
		if err != nil {
			t.Fatalf("legit path failed: %v", err)
		}
		if result != "ok" {
			t.Errorf("result = %q", result)
		}
	})

	t.Run("WriteFile", func(t *testing.T) {
		wf := tools.NewWriteFile(root)
		for _, tc := range attacks {
			t.Run(tc.name, func(t *testing.T) {
				_, err := wf.Run(context.Background(), json.RawMessage(`{"path":"`+tc.path+`","content":"pwned"}`))
				if err == nil {
					t.Error("expected path traversal to be blocked")
				}
			})
		}
	})

	t.Run("ListFiles", func(t *testing.T) {
		lf := tools.NewListFiles(root)
		for _, tc := range attacks {
			t.Run(tc.name, func(t *testing.T) {
				_, err := lf.Run(context.Background(), json.RawMessage(`{"path":"`+tc.path+`"}`))
				if err == nil {
					t.Error("expected path traversal to be blocked")
				}
			})
		}
	})
}

func TestNoRootDirAllowsAnything(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("allowed"), 0644)

	// With no rootDir, absolute paths should work (no sandbox).
	rf := tools.NewReadFile("")
	result, err := rf.Run(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result != "allowed" {
		t.Errorf("result = %q", result)
	}
}

func TestSearchTool(t *testing.T) {
	s := tools.NewSearch()
	if s.NeedsApproval() {
		t.Error("search should not need approval")
	}

	// Validate parameters schema
	var schema map[string]any
	if err := json.Unmarshal(s.Parameters(), &schema); err != nil {
		t.Fatalf("invalid schema: %v", err)
	}

	// Test search with empty query
	_, err := s.Run(context.Background(), json.RawMessage(`{"action":"search","query":""}`))
	if err == nil {
		t.Error("expected error for empty query")
	}

	// Test read with unknown URL (not from search results)
	_, err = s.Run(context.Background(), json.RawMessage(`{"action":"read","url":"https://evil.com"}`))
	if err == nil {
		t.Error("expected error for URL not in search results")
	}

	// Test read with empty URL
	_, err = s.Run(context.Background(), json.RawMessage(`{"action":"read","url":""}`))
	if err == nil {
		t.Error("expected error for empty URL")
	}

	// Test unknown action
	_, err = s.Run(context.Background(), json.RawMessage(`{"action":"delete"}`))
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestSearchReadOnlyAllowsSearchResults(t *testing.T) {
	// Create a mock provider that returns known URLs
	mock := &mockSearchProvider{
		results: &tools.SearchResults{
			Query: "test",
			Results: []tools.SearchResult{
				{Title: "Example", URL: "https://example.com/page1", Description: "A test page"},
			},
		},
	}
	s := tools.NewSearchWithProvider(mock)

	// Search first
	_, err := s.Run(context.Background(), json.RawMessage(`{"action":"search","query":"test"}`))
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	// Reading a URL NOT in results should fail
	_, err = s.Run(context.Background(), json.RawMessage(`{"action":"read","url":"https://evil.com/steal-data"}`))
	if err == nil {
		t.Error("should deny URL not in search results")
	}
}

type mockSearchProvider struct {
	results *tools.SearchResults
}

func (m *mockSearchProvider) Name() string { return "Mock" }
func (m *mockSearchProvider) Search(ctx context.Context, query string) (*tools.SearchResults, error) {
	return m.results, nil
}

func TestFetchTool(t *testing.T) {
	f := tools.NewFetch()
	if !f.NeedsApproval() {
		t.Error("fetch should need approval (arbitrary URL access)")
	}

	// Test with empty URL
	_, err := f.Run(context.Background(), json.RawMessage(`{"url":""}`))
	if err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestFetchRespectsRobotsTxt(t *testing.T) {
	srv := newRobotsTestServer(t)
	defer srv.Close()

	f := tools.NewFetchWithClient(srv.Client())

	// Allowed page
	result, err := f.Run(context.Background(),
		json.RawMessage(`{"url":"`+srv.URL+`/public/page"}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(result, "Blocked by robots.txt") {
		t.Errorf("public page should be allowed: %q", result)
	}

	// Blocked page
	result, err = f.Run(context.Background(),
		json.RawMessage(`{"url":"`+srv.URL+`/private/secret"}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(result, "Blocked by robots.txt") {
		t.Errorf("private page should be blocked: %q", result)
	}
}

func TestSearchReadRespectsRobotsTxt(t *testing.T) {
	srv := newRobotsTestServer(t)
	defer srv.Close()

	mock := &mockSearchProvider{
		results: &tools.SearchResults{
			Query: "test",
			Results: []tools.SearchResult{
				{Title: "Public", URL: srv.URL + "/public/page", Description: "allowed"},
				{Title: "Private", URL: srv.URL + "/private/secret", Description: "blocked"},
			},
		},
	}
	s := tools.NewSearchWithProviderAndClient(mock, srv.Client())

	// Search first to populate knownURLs
	_, err := s.Run(context.Background(), json.RawMessage(`{"action":"search","query":"test"}`))
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	// Read allowed URL
	result, err := s.Run(context.Background(),
		json.RawMessage(`{"action":"read","url":"`+srv.URL+`/public/page"}`))
	if err != nil {
		t.Fatalf("read public: %v", err)
	}
	if strings.Contains(result, "Blocked by robots.txt") {
		t.Errorf("public page should be allowed: %q", result)
	}

	// Read blocked URL
	result, err = s.Run(context.Background(),
		json.RawMessage(`{"action":"read","url":"`+srv.URL+`/private/secret"}`))
	if err != nil {
		t.Fatalf("read private: %v", err)
	}
	if !strings.Contains(result, "Blocked by robots.txt") {
		t.Errorf("private page should be blocked: %q", result)
	}
}

// newRobotsTestServer returns a test server with robots.txt blocking /private/
func newRobotsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
			return
		}
		w.Write([]byte("page content"))
	}))
}

func TestClaudeCodeTool(t *testing.T) {
	cc := tools.NewClaudeCode()
	if !cc.NeedsApproval() {
		t.Error("claude_code should need approval")
	}
	if cc.Name() != "claude_code" {
		t.Errorf("name = %q", cc.Name())
	}

	// Test with empty prompt
	_, err := cc.Run(context.Background(), json.RawMessage(`{"prompt":""}`))
	if err == nil {
		t.Error("expected error for empty prompt")
	}
}

func TestReadSkillTool(t *testing.T) {
	sr := skills.NewRegistry()
	sr.Register(&skills.Skill{
		Manifest: skills.Manifest{
			Skill: skills.SkillMeta{Name: "test-skill", Description: "A test skill"},
		},
		Content: "Do the thing when asked.",
	})

	rs := tools.NewReadSkill(sr)
	if rs.Name() != "read_skill" {
		t.Errorf("name = %q", rs.Name())
	}
	if rs.NeedsApproval() {
		t.Error("read_skill should not need approval")
	}

	// Read existing skill
	result, err := rs.Run(context.Background(), json.RawMessage(`{"name":"test-skill"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != "Do the thing when asked." {
		t.Errorf("result = %q", result)
	}

	// Read non-existent skill should list available
	result, err = rs.Run(context.Background(), json.RawMessage(`{"name":"nope"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found' message, got %q", result)
	}
	if !strings.Contains(result, "test-skill") {
		t.Errorf("expected available skills list, got %q", result)
	}

	// Empty name should error
	_, err = rs.Run(context.Background(), json.RawMessage(`{"name":""}`))
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestAllToolsHaveValidSchema(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.NewSearch())
	reg.Register(tools.NewFetch())
	reg.Register(tools.NewBash())
	reg.Register(tools.NewReadFile(""))
	reg.Register(tools.NewWriteFile(""))
	reg.Register(tools.NewListFiles(""))
	reg.Register(tools.NewClaudeCode())

	for _, tool := range reg.All() {
		t.Run(tool.Name(), func(t *testing.T) {
			var schema map[string]any
			if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
				t.Errorf("invalid JSON schema: %v", err)
			}
			if schema["type"] != "object" {
				t.Errorf("schema type = %v, want object", schema["type"])
			}
			if tool.Description() == "" {
				t.Error("description is empty")
			}
		})
	}
}
