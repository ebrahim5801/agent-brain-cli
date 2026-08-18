package memory

import (
	"sort"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// Search returns a project's active entries matching any query term
// (case-insensitive), ranked by BM25 term relevance scaled by the existing
// origin/freshness/recency boosts from Rank(). An empty query returns the
// most relevant entries by boosts alone. kind narrows to one kind; limit
// caps the result.
func Search(st *store.Store, projectID int64, dir, query, kind string, limit int, gitBudget time.Duration, now time.Time) ([]Ranked, error) {
	entries, err := st.ListMemories(projectID, false)
	if err != nil {
		return nil, err
	}
	terms := queryTerms(query)
	var corpus []store.Memory
	var docs [][]string
	for _, e := range entries {
		if kind != "" && e.Kind != kind {
			continue
		}
		corpus = append(corpus, e)
		docs = append(docs, tokenize(e.Content))
	}
	var filtered []store.Memory
	bm25 := make(map[int64]float64, len(corpus))
	if len(terms) > 0 {
		model := newBM25Model(docs)
		for i, e := range corpus {
			if !matchesAny(docs[i], terms) {
				continue
			}
			filtered = append(filtered, e)
			bm25[e.ID] = model.score(docs[i], terms)
		}
	} else {
		filtered = corpus
	}
	ranked := Rank(filtered, gitstate.NewComparer(dir, gitBudget), now)
	if len(terms) > 0 {
		for i := range ranked {
			ranked[i].Score *= bm25[ranked[i].Entry.ID]
		}
		sort.SliceStable(ranked, func(i, j int) bool {
			return ranked[i].Score > ranked[j].Score
		})
	}
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked, nil
}

// List returns all entries for a project (active first, newest first —
// store ordering), each with computed freshness.
func List(st *store.Store, projectID int64, dir string, includeSuperseded bool, gitBudget time.Duration) ([]Ranked, error) {
	entries, err := st.ListMemories(projectID, includeSuperseded)
	if err != nil {
		return nil, err
	}
	cmp := gitstate.NewComparer(dir, gitBudget)
	out := make([]Ranked, 0, len(entries))
	for _, e := range entries {
		out = append(out, Ranked{Entry: e, Freshness: cmp.Compare(e.Branch.String, e.CommitHash.String)})
	}
	return out, nil
}
