package memory

import (
	"context"
	"testing"

	"smithly.dev/internal/db"
)

// mockStore implements the subset of db.Store needed by Searcher.
type mockStore struct {
	db.Store // embed to satisfy interface; panics on unimplemented methods
	ftsResults  []*db.SearchResult
	embeddings  []db.MemoryEmbedding
	messages    []*db.Message
}

func (m *mockStore) SearchMessagesFTS(_ context.Context, _, _ string, _ int) ([]*db.SearchResult, error) {
	return m.ftsResults, nil
}

func (m *mockStore) GetEmbeddings(_ context.Context, _ string) ([]db.MemoryEmbedding, error) {
	return m.embeddings, nil
}

func (m *mockStore) GetMessages(_ context.Context, _ string, _ int) ([]*db.Message, error) {
	return m.messages, nil
}

// mockEmbedder returns a fixed vector for any input.
type mockEmbedder struct {
	vec []float32
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return m.vec, nil
}

func (m *mockEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = m.vec
	}
	return out, nil
}

func (m *mockEmbedder) Dimensions() int { return len(m.vec) }

func TestKeywordSearch(t *testing.T) {
	store := &mockStore{
		ftsResults: []*db.SearchResult{
			{Message: db.Message{ID: 1, Content: "hello world", Trust: "trusted"}, Score: -5.0},
			{Message: db.Message{ID: 2, Content: "hello there", Trust: "untrusted"}, Score: -3.0},
		},
	}

	s := NewSearcher(store, nil)
	if s.HasEmbedder() {
		t.Error("HasEmbedder should be false")
	}

	results, err := s.Search(context.Background(), "agent1", "hello", "keyword", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
	// Trusted message with better BM25 should score higher
	if results[0].ID != 1 {
		t.Errorf("first result ID = %d, want 1 (trusted + better BM25)", results[0].ID)
	}
}

func TestHybridSearch(t *testing.T) {
	store := &mockStore{
		ftsResults: []*db.SearchResult{
			{Message: db.Message{ID: 1, Content: "golang programming", Trust: "trusted"}, Score: -5.0},
		},
		embeddings: []db.MemoryEmbedding{
			{MemoryID: 1, Embedding: []float32{1, 0, 0}, Trust: "trusted"},
			{MemoryID: 2, Embedding: []float32{0.9, 0.1, 0}, Trust: "trusted"},
		},
		messages: []*db.Message{
			{ID: 2, Content: "Go is a great language", Trust: "trusted"},
		},
	}

	embedder := &mockEmbedder{vec: []float32{1, 0, 0}}
	s := NewSearcher(store, embedder)
	if !s.HasEmbedder() {
		t.Error("HasEmbedder should be true")
	}

	results, err := s.Search(context.Background(), "agent1", "golang", "hybrid", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) < 1 {
		t.Fatal("expected at least 1 result")
	}
	// All results should have non-zero scores
	for i, r := range results {
		if r.Score <= 0 {
			t.Errorf("result[%d].Score = %f, want > 0", i, r.Score)
		}
	}
}

func TestSemanticFallsBackWhenNoEmbedder(t *testing.T) {
	store := &mockStore{
		ftsResults: []*db.SearchResult{
			{Message: db.Message{ID: 1, Content: "test", Trust: "trusted"}, Score: -5.0},
		},
	}

	s := NewSearcher(store, nil)
	results, err := s.Search(context.Background(), "agent1", "test", "semantic", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1 (should fall back to keyword)", len(results))
	}
}

func TestTrustScoring(t *testing.T) {
	if trustScore("trusted") != 1.0 {
		t.Error("trusted should be 1.0")
	}
	if trustScore("semi-trusted") != 0.7 {
		t.Error("semi-trusted should be 0.7")
	}
	if trustScore("untrusted") != 0.3 {
		t.Error("untrusted should be 0.3")
	}
}
