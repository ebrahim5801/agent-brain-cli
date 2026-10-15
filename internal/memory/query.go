package memory

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
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

// Page describes a slice in a filtered, already ordered list. Limit <= 0 means
// no limit, matching the CLI's explicit --limit 0 behavior.
type Page struct {
	Limit  int
	Offset int
}

// PageResult contains the entries selected from the original list and the total
// number of entries matching before paging. From and To are one-based inclusive
// positions, or zero for an empty page.
type PageResult[T any] struct {
	Items   []T
	Total   int
	From    int
	To      int
	HasMore bool
}

// PageItems slices a filtered ordered sequence. The input must already be
// filtered and ordered; keeping paging in this shared helper ensures the CLI and
// MCP list expose identical page boundaries.
func PageItems[T any](items []T, page Page) (PageResult[T], error) {
	if page.Limit < 0 {
		return PageResult[T]{}, fmt.Errorf("limit must be >= 0")
	}
	if page.Offset < 0 {
		return PageResult[T]{}, fmt.Errorf("offset must be >= 0")
	}
	total := len(items)
	if page.Offset >= total {
		return PageResult[T]{Total: total}, nil
	}
	end := total
	if page.Limit > 0 && page.Limit < total-page.Offset {
		end = page.Offset + page.Limit
	}
	return PageResult[T]{
		Items:   items[page.Offset:end],
		Total:   total,
		From:    page.Offset + 1,
		To:      end,
		HasMore: end < total,
	}, nil
}

// RangeFooter returns a common range footer. The first page needs no footer
// when it contains everything; subsequent pages keep the range even on the last
// page. Callers supply their own next-page hint when HasMore is true.
func (page PageResult[T]) RangeFooter() string {
	if page.Total == 0 || (!page.HasMore && page.From <= 1) {
		return ""
	}
	return fmt.Sprintf("Showing %d-%d of %d.", page.From, page.To, page.Total)
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

// Search applies lifecycle/scope eligibility to BM25 candidates, following
// accepted predecessor terminology and exposing unresolved peers. Metadata
// influence is bounded; matching Git state never proves semantic validity.
func Search(st *store.Store, projectID int64, dir, query string, filter Filter, limit int, gitBudget time.Duration, now time.Time) ([]Ranked, error) {
	entries, err := loadEntries(st, projectID, dir, true)
	if err != nil {
		return nil, err
	}
	terms := queryTerms(query)
	providerDocs := make([]RetrievalDocument, 0, len(entries))
	for _, e := range entries {
		providerDocs = append(providerDocs, RetrievalDocument{Reference: strconv.FormatInt(e.ID, 10), Text: e.Content + " " + e.Details.Rationale})
	}
	providerScores := map[int64]float64{}
	for _, c := range (BM25Provider{}).Candidates(providerDocs, query) {
		id, _ := strconv.ParseInt(c.Reference, 10, 64)
		providerScores[id] = c.Score
	}
	var corpus []store.Memory
	var docs [][]string
	for _, e := range entries {
		if !filter.Matches(e) {
			continue
		}
		corpus = append(corpus, e)
		docs = append(docs, tokenize(e.Content+" "+e.Details.Rationale))
	}
	lookup := map[int64]store.Memory{}
	for _, e := range entries {
		lookup[e.ID] = e
	}
	var filtered []store.Memory
	bm25 := make(map[int64]float64, len(corpus))
	if len(terms) > 0 {
		model := newBM25Model(docs)
		for i, e := range corpus {
			if !matchesAny(docs[i], terms) {
				continue
			}
			latest := e
			seen := map[int64]bool{}
			for latest.Status == store.MemorySuperseded && latest.SupersededBy.Valid && !seen[latest.ID] {
				seen[latest.ID] = true
				next, ok := lookup[latest.SupersededBy.Int64]
				if !ok {
					break
				}
				latest = next
			}
			if latest.Status != store.MemoryActive || !filter.Matches(latest) {
				continue
			}
			score := model.score(docs[i], terms)
			if useV2Ranking() {
				score = providerScores[e.ID]
			}
			if _, ok := bm25[latest.ID]; !ok {
				filtered = append(filtered, latest)
			}
			if score > bm25[latest.ID] {
				bm25[latest.ID] = score
			}

		}
	} else {
		for _, e := range corpus {
			if e.Status == store.MemoryActive {
				filtered = append(filtered, e)
			}
		}
	}
	if len(terms) > 0 {
		initial := append([]store.Memory(nil), filtered...)
		for _, m := range initial {
			for _, peer := range conflictPeers(m) {
				other, ok := lookup[peer]
				if !ok || other.Status != store.MemoryActive || !filter.Matches(other) {
					continue
				}
				if _, exists := bm25[peer]; !exists {
					filtered = append(filtered, other)
					bm25[peer] = bm25[m.ID] * 0.95
				}
			}
		}
	}
	ranked := Rank(filtered, gitstate.NewComparer(dir, gitBudget), now)
	if len(terms) == 0 && useV2Ranking() {
		for i := range ranked {
			ranked[i].Score = contextScore(ranked[i].Entry, ranked[i].Freshness.Signal, now)
		}
		sortRanked(ranked)
	}
	if len(terms) > 0 {
		for i := range ranked {
			if useV2Ranking() {
				ranked[i].Score = bm25[ranked[i].Entry.ID] * lexicalAdjustment(ranked[i].Entry, now)
			} else {
				ranked[i].Score *= bm25[ranked[i].Entry.ID]
			}
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
	entries, err := loadEntries(st, projectID, dir, includeSuperseded)
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

func loadEntries(st *store.Store, projectID int64, dir string, history bool) ([]store.Memory, error) {
	gs := gitstate.Capture(dir)
	scopes := map[string]bool{"": true, "checkout:" + store.CheckoutIdentity(dir): true}
	if gs.Branch != "" {
		scopes["branch:"+gs.Branch] = true
	}
	snapshot, err := st.MemorySnapshot(projectID, scopes)
	if err != nil {
		return nil, err
	}
	// Artifact digests are cheap and bounded. Conversation claims retain their
	// original verification timestamp; use targeted verify_sources to recheck.

	artifactChecks := 0
	byID := map[int64]int{}
	for i, m := range snapshot.Entries {
		byID[m.ID] = i
	}
	for _, ref := range snapshot.Evidence {
		if ref.Type != "artifact" || ref.Status == "unverified" {
			continue
		}
		i, ok := byID[ref.MemoryID]
		if !ok {
			continue
		}
		m := &snapshot.Entries[i]
		status := "potentially_outdated: source review deferred"
		if artifactChecks < 16 {
			artifactChecks++
			if artifactMatches(dir, ref.Locator, ref.Digest) {
				continue
			}
			status = "verification_failed"
		}
		m.Verification = status
	}
	var out []store.Memory
	for _, e := range snapshot.Entries {
		if e.Details.Scope != "" && !scopes[e.Details.Scope] {
			continue
		}
		if history || e.Status == store.MemoryActive {
			out = append(out, e)
		}
	}
	return out, nil
}

func conflictPeers(m store.Memory) []int64 {
	var ids []int64
	for _, ref := range strings.Fields(m.ConflictRefs) {
		if id, err := strconv.ParseInt(strings.TrimPrefix(ref, "#"), 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
