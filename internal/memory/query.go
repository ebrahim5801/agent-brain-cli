package memory

import (
	"sort"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// Filter narrows a search to a closed-vocabulary facet. Both fields are
// optional; an empty field does not filter. Only closed vocabularies belong
// here — a filter that misses returns a confident empty result rather than an
// obviously degraded one, which is safe for kind and priority and is not safe
// for free-form labels.
type Filter struct {
	Kind     string
	Priority string
}

func (f Filter) Matches(e store.Memory) bool {
	if f.Kind != "" && e.Kind != f.Kind {
		return false
	}
	if f.Priority != "" && priorityOf(e.Priority) != f.Priority {
		return false
	}
	return true
}

func (f Filter) matchesTeam(e store.TeamMemoryRow) bool {
	if f.Kind != "" && e.Kind != f.Kind {
		return false
	}
	if f.Priority != "" && priorityOf(e.Priority) != f.Priority {
		return false
	}
	return true
}

// priorityOf reads a stored priority, treating rows written before the column
// existed as the default rather than as unfilterable.
func priorityOf(p string) string {
	if p == "" {
		return PriorityNormal
	}
	return p
}

// Search returns a project's active entries matching any query term
// (case-insensitive), ranked by BM25 term relevance scaled by the existing
// priority/origin/freshness/recency boosts from Rank(). An empty query returns
// the most relevant entries by boosts alone; filter narrows the candidates
// before scoring; limit caps the result.
//
// Priority multiplies alongside BM25 rather than partitioning the result, and
// the BM25 spread across a real corpus is wider than the 2× priority range, so
// a strong term match on a normal entry still outranks a weak match on a
// critical one. Priority governs what survives the pack budget, not what
// answers a query.
func Search(st *store.Store, projectID int64, dir, query string, filter Filter, limit int, gitBudget time.Duration, now time.Time) ([]Ranked, error) {
	entries, err := st.ListMemories(projectID, false)
	if err != nil {
		return nil, err
	}
	terms := queryTerms(query)
	var corpus []store.Memory
	var docs [][]string
	for _, e := range entries {
		if !filter.Matches(e) {
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
