// Package memory provides hybrid search over conversation history.
package memory

import (
	"context"
	"math"
	"sort"

	"smithly.dev/internal/db"
	"smithly.dev/internal/embedding"
)

// Searcher combines FTS5, vector similarity, and trust weighting for hybrid search.
type Searcher struct {
	store    db.Store
	embedder embedding.Client // nil = FTS-only mode
}

// NewSearcher creates a hybrid searcher. embedder may be nil for FTS-only search.
func NewSearcher(store db.Store, embedder embedding.Client) *Searcher {
	return &Searcher{store: store, embedder: embedder}
}

// HasEmbedder reports whether vector search is available.
func (s *Searcher) HasEmbedder() bool { return s.embedder != nil }

// Result is a scored message from hybrid search.
type Result struct {
	db.Message
	Score float64
}

// Search runs hybrid search for the given query against an agent's history.
// mode: "keyword" (FTS only), "semantic" (vector only), "hybrid" (combined).
func (s *Searcher) Search(ctx context.Context, agentID, query, mode string, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = 20
	}

	switch mode {
	case "semantic":
		if s.embedder == nil {
			// Fall back to keyword when no embedder
			return s.keywordSearch(ctx, agentID, query, limit)
		}
		return s.semanticSearch(ctx, agentID, query, limit)
	case "keyword":
		return s.keywordSearch(ctx, agentID, query, limit)
	default: // "hybrid" or empty
		if s.embedder == nil {
			return s.keywordSearch(ctx, agentID, query, limit)
		}
		return s.hybridSearch(ctx, agentID, query, limit)
	}
}

func (s *Searcher) keywordSearch(ctx context.Context, agentID, query string, limit int) ([]Result, error) {
	ftsResults, err := s.store.SearchMessagesFTS(ctx, agentID, query, limit)
	if err != nil {
		return nil, err
	}

	results := make([]Result, len(ftsResults))
	for i, r := range ftsResults {
		// Normalize BM25: SQLite FTS5 bm25() returns negative values where more negative = more relevant.
		// Convert to 0-1 range where higher = more relevant.
		ftsScore := normalizeBM25(r.Score)
		trustWeight := trustScore(r.Trust)
		results[i] = Result{
			Message: r.Message,
			Score:   0.7*ftsScore + 0.3*trustWeight,
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	return results, nil
}

func (s *Searcher) semanticSearch(ctx context.Context, agentID, query string, limit int) ([]Result, error) {
	queryVec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	stored, err := s.store.GetEmbeddings(ctx, agentID)
	if err != nil {
		return nil, err
	}

	type scored struct {
		memoryID int64
		sim      float64
		trust    string
	}
	var candidates []scored
	for _, e := range stored {
		sim := float64(embedding.CosineSimilarity(queryVec, e.Embedding))
		candidates = append(candidates, scored{memoryID: e.MemoryID, sim: sim, trust: e.Trust})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].sim > candidates[j].sim
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// Collect IDs for batch hydration
	ids := make([]int64, len(candidates))
	for i, c := range candidates {
		ids[i] = c.memoryID
	}

	// Batch fetch only the messages we need
	msgs, err := s.store.GetMessagesByIDs(ctx, agentID, ids)
	if err != nil {
		return nil, err
	}
	msgMap := make(map[int64]*db.Message, len(msgs))
	for _, m := range msgs {
		msgMap[m.ID] = m
	}

	results := make([]Result, 0, len(candidates))
	for _, c := range candidates {
		trustWeight := trustScore(c.trust)
		score := 0.7*c.sim + 0.3*trustWeight
		if m, ok := msgMap[c.memoryID]; ok {
			results = append(results, Result{Message: *m, Score: score})
		}
	}
	return results, nil
}

func (s *Searcher) hybridSearch(ctx context.Context, agentID, query string, limit int) ([]Result, error) {
	// Get FTS results
	ftsResults, err := s.store.SearchMessagesFTS(ctx, agentID, query, limit*2)
	if err != nil {
		return nil, err
	}

	// Get query embedding
	queryVec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		// Fall back to keyword-only if embedding fails
		return s.keywordSearch(ctx, agentID, query, limit)
	}

	// Get all embeddings for scoring
	stored, err := s.store.GetEmbeddings(ctx, agentID)
	if err != nil {
		return nil, err
	}
	embMap := make(map[int64][]float32, len(stored))
	trustMap := make(map[int64]string, len(stored))
	for _, e := range stored {
		embMap[e.MemoryID] = e.Embedding
		trustMap[e.MemoryID] = e.Trust
	}

	// Score all candidates from FTS
	scored := make(map[int64]Result)
	for _, r := range ftsResults {
		ftsScore := normalizeBM25(r.Score)
		var vecScore float64
		if emb, ok := embMap[r.ID]; ok {
			vecScore = float64(embedding.CosineSimilarity(queryVec, emb))
		}
		tw := trustScore(r.Trust)
		score := 0.3*ftsScore + 0.5*vecScore + 0.2*tw
		scored[r.ID] = Result{Message: r.Message, Score: score}
	}

	// Also check top vector matches not in FTS results
	type vecCandidate struct {
		memoryID int64
		sim      float64
	}
	var vecCandidates []vecCandidate
	for _, e := range stored {
		if _, inFTS := scored[e.MemoryID]; !inFTS {
			sim := float64(embedding.CosineSimilarity(queryVec, e.Embedding))
			vecCandidates = append(vecCandidates, vecCandidate{e.MemoryID, sim})
		}
	}
	sort.Slice(vecCandidates, func(i, j int) bool {
		return vecCandidates[i].sim > vecCandidates[j].sim
	})
	// Add top vector-only results
	topN := min(len(vecCandidates), limit)

	// Hydrate vector-only candidates
	if topN > 0 {
		ids := make([]int64, topN)
		for i := 0; i < topN; i++ {
			ids[i] = vecCandidates[i].memoryID
		}
		msgs, err := s.store.GetMessagesByIDs(ctx, agentID, ids)
		if err == nil {
			msgMap := make(map[int64]*db.Message, len(msgs))
			for _, m := range msgs {
				msgMap[m.ID] = m
			}
			for i := 0; i < topN; i++ {
				vc := vecCandidates[i]
				tw := trustScore(trustMap[vc.memoryID])
				score := 0.5*vc.sim + 0.2*tw // no FTS component
				if m, ok := msgMap[vc.memoryID]; ok {
					scored[vc.memoryID] = Result{Message: *m, Score: score}
				}
			}
		}
	}

	// Collect and sort
	results := make([]Result, 0, len(scored))
	for _, r := range scored {
		results = append(results, r)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// normalizeBM25 converts SQLite FTS5 bm25() scores (negative, lower = better)
// to a 0-1 range (higher = better).
func normalizeBM25(score float64) float64 {
	// bm25() returns negative values; negate to make positive
	pos := -score
	// Use sigmoid-like mapping: 1 / (1 + e^(-x))
	// Scale so typical scores (0-20) map well to 0-1
	return 1.0 / (1.0 + math.Exp(-pos))
}

// trustScore maps trust levels to numeric weights.
func trustScore(trust string) float64 {
	switch trust {
	case "trusted":
		return 1.0
	case "semi-trusted":
		return 0.7
	case "untrusted":
		return 0.3
	default:
		return 0.5
	}
}
