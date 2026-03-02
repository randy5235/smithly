package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"smithly.dev/internal/db"
)

// mockStore implements the subset of db.Store needed by Searcher.
type mockStore struct {
	db.Store // embed to satisfy interface; panics on unimplemented methods
	ftsResults []*db.SearchResult
	embeddings []db.MemoryEmbedding
	messages   []*db.Message
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

func (m *mockStore) GetMessagesByIDs(_ context.Context, _ string, ids []int64) ([]*db.Message, error) {
	idSet := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	var result []*db.Message
	for _, msg := range m.messages {
		if _, ok := idSet[msg.ID]; ok {
			result = append(result, msg)
		}
	}
	return result, nil
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

// errEmbedder returns an error from Embed to simulate embedding failures.
type errEmbedder struct {
	err error
}

func (e *errEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, e.err
}

func (e *errEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	return nil, e.err
}

func (e *errEmbedder) Dimensions() int { return 3 }

func TestSemanticSearchWithEmbedder(t *testing.T) {
	store := &mockStore{
		embeddings: []db.MemoryEmbedding{
			{MemoryID: 1, Embedding: []float32{1, 0, 0}, Trust: "trusted"},
			{MemoryID: 2, Embedding: []float32{0, 1, 0}, Trust: "trusted"},
			{MemoryID: 3, Embedding: []float32{0.9, 0.1, 0}, Trust: "semi-trusted"},
		},
		messages: []*db.Message{
			{ID: 1, Content: "closest match", Trust: "trusted"},
			{ID: 2, Content: "orthogonal", Trust: "trusted"},
			{ID: 3, Content: "near match", Trust: "semi-trusted"},
		},
	}

	// Query vector is {1,0,0} — closest to embedding 1, then 3, then 2.
	embedder := &mockEmbedder{vec: []float32{1, 0, 0}}
	s := NewSearcher(store, embedder)

	results, err := s.Search(context.Background(), "agent1", "anything", "semantic", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from semantic search")
	}
	// First result should be the exact cosine match (ID=1, embedding {1,0,0}).
	if results[0].ID != 1 {
		t.Errorf("first result ID = %d, want 1 (exact cosine match)", results[0].ID)
	}
	// All results should have positive scores.
	for i, r := range results {
		if r.Score <= 0 {
			t.Errorf("result[%d].Score = %f, want > 0", i, r.Score)
		}
	}
}

func TestNormalizeBM25EdgeCases(t *testing.T) {
	// score 0 → normalizeBM25(0) = 1/(1+e^0) = 0.5
	got := normalizeBM25(0)
	if math.Abs(got-0.5) > 1e-9 {
		t.Errorf("normalizeBM25(0) = %f, want 0.5", got)
	}

	// Very negative score (e.g. -20) → pos = 20 → sigmoid(20) ≈ 1.0
	got = normalizeBM25(-20)
	if got < 0.99 {
		t.Errorf("normalizeBM25(-20) = %f, want ≈ 1.0", got)
	}

	// Positive score (e.g. 5) → pos = -5 → sigmoid(-5) ≈ 0.0067
	got = normalizeBM25(5)
	if got > 0.01 {
		t.Errorf("normalizeBM25(5) = %f, want ≈ 0.0", got)
	}
}

func TestSearchLimitEnforcement(t *testing.T) {
	const totalMessages = 35
	fts := make([]*db.SearchResult, totalMessages)
	for i := range totalMessages {
		fts[i] = &db.SearchResult{
			Message: db.Message{
				ID:      int64(i + 1),
				Content: fmt.Sprintf("message %d about search topic", i+1),
				Trust:   "trusted",
			},
			Score: -float64(i + 1), // varied BM25 scores
		}
	}

	store := &mockStore{ftsResults: fts}
	s := NewSearcher(store, nil)

	results, err := s.Search(context.Background(), "agent1", "search topic", "keyword", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// keywordSearch returns all FTS results (store returns them all) but we need to
	// verify the store was called with the correct limit. Since mockStore ignores
	// the limit parameter and returns all 35, the keyword path returns all of them.
	// However, for hybrid mode the final truncation enforces limit.
	// Let's also verify hybrid mode enforces the limit strictly.
	if len(results) != totalMessages {
		// keyword mode trusts the store to enforce limits; our mock returns all
		t.Logf("keyword mode returned %d results (store mock returns all)", len(results))
	}

	// Use hybrid mode with an embedder to test the explicit limit truncation in hybridSearch.
	embedder := &mockEmbedder{vec: []float32{1, 0, 0}}
	embs := make([]db.MemoryEmbedding, totalMessages)
	msgs := make([]*db.Message, totalMessages)
	for i := range totalMessages {
		embs[i] = db.MemoryEmbedding{
			MemoryID:  int64(i + 1),
			Embedding: []float32{1, 0, 0},
			Trust:     "trusted",
		}
		msgs[i] = &db.Message{
			ID:      int64(i + 1),
			Content: fmt.Sprintf("message %d about search topic", i+1),
			Trust:   "trusted",
		}
	}
	storeHybrid := &mockStore{ftsResults: fts, embeddings: embs, messages: msgs}
	sh := NewSearcher(storeHybrid, embedder)

	results, err = sh.Search(context.Background(), "agent1", "search topic", "hybrid", 5)
	if err != nil {
		t.Fatalf("hybrid Search: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("hybrid results = %d, want 5", len(results))
	}
}

func TestSearchEmptyResults(t *testing.T) {
	store := &mockStore{
		ftsResults: nil,
		embeddings: nil,
		messages:   nil,
	}

	// FTS-only mode with no matches.
	s := NewSearcher(store, nil)
	results, err := s.Search(context.Background(), "agent1", "nonexistent query xyz", "keyword", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}

	// Hybrid mode with no embedder also yields empty.
	results, err = s.Search(context.Background(), "agent1", "nonexistent query xyz", "hybrid", 10)
	if err != nil {
		t.Fatalf("Search hybrid: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty hybrid results, got %d", len(results))
	}
}

func TestHybridSearchEmbedderError(t *testing.T) {
	ftsResults := []*db.SearchResult{
		{Message: db.Message{ID: 1, Content: "keyword match", Trust: "trusted"}, Score: -5.0},
		{Message: db.Message{ID: 2, Content: "another match", Trust: "semi-trusted"}, Score: -3.0},
	}
	store := &mockStore{ftsResults: ftsResults}

	embedder := &errEmbedder{err: errors.New("embedding service unavailable")}
	s := NewSearcher(store, embedder)

	// hybrid mode: embedder fails, should fall back to keyword-only results.
	results, err := s.Search(context.Background(), "agent1", "keyword", "hybrid", 10)
	if err != nil {
		t.Fatalf("Search should not error when embedder fails in hybrid mode: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 fallback results, got %d", len(results))
	}
	// Results should be sorted by score (keyword scoring: 0.7*fts + 0.3*trust).
	if results[0].Score < results[1].Score {
		t.Errorf("results not sorted: first score %f < second score %f", results[0].Score, results[1].Score)
	}
}
