package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"smithly.dev/internal/tools"
)

// TestBraveSearchIntegration runs a real search against Brave Search API.
//
//	SMITHLY_INTEGRATION=1 BRAVE_API_KEY=... go test ./internal/tools/ -run TestBraveSearch -v
func TestBraveSearchIntegration(t *testing.T) {
	if os.Getenv("SMITHLY_INTEGRATION") == "" {
		t.Skip("SMITHLY_INTEGRATION not set")
	}
	key := os.Getenv("BRAVE_API_KEY")
	if key == "" {
		t.Skip("BRAVE_API_KEY not set")
	}

	provider := tools.NewBraveSearch(key)
	s := tools.NewSearchWithProvider(provider)

	// Run a search
	result, err := s.Run(context.Background(),
		json.RawMessage(`{"action":"search","query":"Go programming language"}`))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(result, "Search results for") {
		t.Errorf("unexpected result format: %q", result[:min(len(result), 200)])
	}
	if !strings.Contains(result, "http") {
		t.Errorf("expected URLs in results: %q", result[:min(len(result), 200)])
	}
	t.Logf("Search returned %d chars", len(result))
}

// TestBraveSearchBadKey verifies we get a clear error with an invalid key.
func TestBraveSearchBadKey(t *testing.T) {
	provider := tools.NewBraveSearch("invalid-key-12345")
	_, err := provider.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for bad API key")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Errorf("error = %q, expected to mention API key", err.Error())
	}
}

// TestSearchPermissions is a comprehensive test of the search tool's security model
// using a mock provider. It verifies:
// - URLs from search results can be read
// - URLs NOT in search results are denied
// - robots.txt is respected for reads
// - Multiple searches accumulate allowed URLs
func TestSearchPermissions(t *testing.T) {
	// Set up a mock HTTP server that serves pages and robots.txt
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "User-agent: *\nDisallow: /blocked/\nAllow: /\n")
		default:
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "Content of %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	mock := &mockSearchProvider{
		results: &tools.SearchResults{
			Query: "test query",
			Results: []tools.SearchResult{
				{Title: "Allowed Page", URL: srv.URL + "/allowed/page1", Description: "An allowed page"},
				{Title: "Another Page", URL: srv.URL + "/allowed/page2", Description: "Another one"},
				{Title: "Blocked Page", URL: srv.URL + "/blocked/secret", Description: "robots.txt blocks this"},
			},
		},
	}
	s := tools.NewSearchWithProviderAndClient(mock, srv.Client())

	ctx := context.Background()

	// 1. Reading BEFORE searching should fail
	t.Run("read before search denied", func(t *testing.T) {
		_, err := s.Run(ctx, json.RawMessage(`{"action":"read","url":"`+srv.URL+`/allowed/page1"}`))
		if err == nil {
			t.Error("should deny read before any search")
		}
	})

	// 2. Search populates allowed URLs
	t.Run("search succeeds", func(t *testing.T) {
		result, err := s.Run(ctx, json.RawMessage(`{"action":"search","query":"test query"}`))
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if !strings.Contains(result, "Allowed Page") {
			t.Errorf("missing title in results: %q", result)
		}
		if !strings.Contains(result, srv.URL+"/allowed/page1") {
			t.Errorf("missing URL in results: %q", result)
		}
	})

	// 3. Reading a result URL succeeds
	t.Run("read allowed URL", func(t *testing.T) {
		result, err := s.Run(ctx, json.RawMessage(`{"action":"read","url":"`+srv.URL+`/allowed/page1"}`))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !strings.Contains(result, "Content of /allowed/page1") {
			t.Errorf("unexpected content: %q", result)
		}
	})

	// 4. Reading a URL NOT in results fails
	t.Run("read unknown URL denied", func(t *testing.T) {
		_, err := s.Run(ctx, json.RawMessage(`{"action":"read","url":"https://evil.com/steal-data"}`))
		if err == nil {
			t.Error("should deny URL not in search results")
		}
		if !strings.Contains(err.Error(), "not in search results") {
			t.Errorf("error = %q", err.Error())
		}
	})

	// 5. Reading a URL that IS in results but blocked by robots.txt
	t.Run("read robots-blocked URL", func(t *testing.T) {
		result, err := s.Run(ctx, json.RawMessage(`{"action":"read","url":"`+srv.URL+`/blocked/secret"}`))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !strings.Contains(result, "Blocked by robots.txt") {
			t.Errorf("should be blocked by robots.txt: %q", result)
		}
	})

	// 6. Second allowed page also works
	t.Run("read second allowed URL", func(t *testing.T) {
		result, err := s.Run(ctx, json.RawMessage(`{"action":"read","url":"`+srv.URL+`/allowed/page2"}`))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !strings.Contains(result, "Content of /allowed/page2") {
			t.Errorf("unexpected content: %q", result)
		}
	})

	// 7. Second search adds more URLs
	t.Run("second search accumulates URLs", func(t *testing.T) {
		mock.results = &tools.SearchResults{
			Query: "second query",
			Results: []tools.SearchResult{
				{Title: "New Page", URL: srv.URL + "/new/page3", Description: "From second search"},
			},
		}

		_, err := s.Run(ctx, json.RawMessage(`{"action":"search","query":"second query"}`))
		if err != nil {
			t.Fatalf("search: %v", err)
		}

		// New URL should be readable
		result, err := s.Run(ctx, json.RawMessage(`{"action":"read","url":"`+srv.URL+`/new/page3"}`))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !strings.Contains(result, "Content of /new/page3") {
			t.Errorf("unexpected content: %q", result)
		}

		// Old URLs should still be readable
		result, err = s.Run(ctx, json.RawMessage(`{"action":"read","url":"`+srv.URL+`/allowed/page1"}`))
		if err != nil {
			t.Fatalf("read old URL: %v", err)
		}
		if !strings.Contains(result, "Content of /allowed/page1") {
			t.Errorf("old URL should still work: %q", result)
		}
	})

	// 8. Invalid actions
	t.Run("invalid action", func(t *testing.T) {
		_, err := s.Run(ctx, json.RawMessage(`{"action":"delete"}`))
		if err == nil {
			t.Error("should error on unknown action")
		}
	})

	// 9. Empty query
	t.Run("empty query", func(t *testing.T) {
		_, err := s.Run(ctx, json.RawMessage(`{"action":"search","query":""}`))
		if err == nil {
			t.Error("should error on empty query")
		}
	})

	// 10. Empty URL
	t.Run("empty URL", func(t *testing.T) {
		_, err := s.Run(ctx, json.RawMessage(`{"action":"read","url":""}`))
		if err == nil {
			t.Error("should error on empty URL")
		}
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
