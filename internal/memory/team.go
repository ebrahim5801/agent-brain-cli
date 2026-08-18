package memory

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// packItem is one candidate line in the merged personal+team pack. Personal and
// team entries rank together under one budget (Constitution V: one channel).
type packItem struct {
	content    string
	kind       string
	origin     string
	capturedAt string
	branch     string
	commit     string

	// Team attribution (isTeam == false for personal entries).
	isTeam       bool
	author       string
	authorFormer bool
	contradicts  bool
	uid          string // team_uid; "" for personal entries
	id           int64  // personal id; 0 for team entries

	freshness gitstate.Freshness
	score     float64
}

// mergeRank ranks the union of active personal entries and active cached team
// entries. Freshness for team entries is evaluated against the READER's
// checkout using the author-recorded branch/commit (US5), exactly as for
// personal entries.
func mergeRank(personal []store.Memory, team []store.TeamMemoryRow, cmp *gitstate.Comparer, now time.Time) []packItem {
	items := make([]packItem, 0, len(personal)+len(team))
	for _, e := range personal {
		items = append(items, personalItem(e, cmp, now))
	}
	for _, e := range team {
		items = append(items, teamItem(e, cmp, now))
	}
	sortItems(items)
	return items
}

func personalItem(e store.Memory, cmp *gitstate.Comparer, now time.Time) packItem {
	f := cmp.Compare(e.Branch.String, e.CommitHash.String)
	return packItem{
		content: e.Content, kind: e.Kind, origin: e.Origin, capturedAt: e.CapturedAt,
		branch: e.Branch.String, commit: e.CommitHash.String, id: e.ID,
		freshness: f, score: originWeight(e.Origin) * freshnessWeight(f.Signal) * recencyDecay(e.CapturedAt, now),
	}
}

func teamItem(e store.TeamMemoryRow, cmp *gitstate.Comparer, now time.Time) packItem {
	f := cmp.Compare(e.Branch, e.CommitHash)
	return packItem{
		content: e.Content, kind: e.Kind, origin: e.Origin, capturedAt: e.CapturedAt,
		branch: e.Branch, commit: e.CommitHash,
		isTeam: true, author: e.Author, authorFormer: e.AuthorFormer, contradicts: e.Contradicts != "", uid: e.UID,
		freshness: f, score: originWeight(e.Origin) * freshnessWeight(f.Signal) * recencyDecay(e.CapturedAt, now),
	}
}

func sortItems(items []packItem) {
	sort.SliceStable(items, func(i, j int) bool { return itemLess(items[i], items[j]) })
}

func itemLess(a, b packItem) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	if a.capturedAt != b.capturedAt {
		return a.capturedAt > b.capturedAt
	}
	// Personal before team on a full tie so the reader's own context is not
	// silently outranked by a same-scored team entry (US2-AC3).
	if a.isTeam != b.isTeam {
		return !a.isTeam
	}
	return a.id > b.id
}

// TeamList renders this project's active cached team entries, score-ordered,
// for the merged MCP list. Empty when the project has no team pool.
func TeamList(st *store.Store, projectID int64, dir string, gitBudget time.Duration, now time.Time) ([]string, error) {
	lines, _, err := teamLines(st, projectID, dir, "", "", 0, gitBudget, now)
	return lines, err
}

// TeamSearch renders active cached team entries matching any query term,
// ranked by BM25 relevance scaled by the existing score boosts, for the
// merged MCP search. The second return is the served entries' uids, in the
// same order as lines, so the caller can record retrieval against them.
func TeamSearch(st *store.Store, projectID int64, dir, query, kind string, limit int, gitBudget time.Duration, now time.Time) ([]string, []string, error) {
	return teamLines(st, projectID, dir, query, kind, limit, gitBudget, now)
}

func teamLines(st *store.Store, projectID int64, dir, query, kind string, limit int, gitBudget time.Duration, now time.Time) ([]string, []string, error) {
	team, err := st.ListTeamMemories(projectID)
	if err != nil {
		return nil, nil, err
	}
	if len(team) == 0 {
		return nil, nil, nil
	}
	terms := queryTerms(query)
	cmp := gitstate.NewComparer(dir, gitBudget)
	var corpus []store.TeamMemoryRow
	var docs [][]string
	for _, e := range team {
		if kind != "" && e.Kind != kind {
			continue
		}
		corpus = append(corpus, e)
		docs = append(docs, tokenize(e.Content))
	}
	var items []packItem
	bm25 := make(map[string]float64, len(corpus))
	if len(terms) > 0 {
		model := newBM25Model(docs)
		for i, e := range corpus {
			if !matchesAny(docs[i], terms) {
				continue
			}
			it := teamItem(e, cmp, now)
			bm25[it.uid] = model.score(docs[i], terms)
			items = append(items, it)
		}
	} else {
		for _, e := range corpus {
			items = append(items, teamItem(e, cmp, now))
		}
	}
	sortItems(items)
	if len(terms) > 0 {
		sort.SliceStable(items, func(i, j int) bool {
			return items[i].score*bm25[items[i].uid] > items[j].score*bm25[items[j].uid]
		})
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	lines := make([]string, 0, len(items))
	uids := make([]string, 0, len(items))
	for _, it := range items {
		lines = append(lines, renderItem(it))
		uids = append(uids, it.uid)
	}
	return lines, uids, nil
}

// renderItem is the serving line for one pack item. Personal entries keep the
// established [#id] form; team entries lead with a stable [team#<handle>] the
// assistant can pass back to memory_save's supersedes_team, followed by
// attribution and state.
func renderItem(it packItem) string {
	if !it.isTeam {
		return renderPersonalLine(it.id, it.kind, it.origin, it.freshness.Render(), it.content)
	}
	var meta strings.Builder
	meta.WriteString("team · ")
	meta.WriteString(it.author)
	if it.authorFormer {
		meta.WriteString(" (former member)")
	}
	meta.WriteString(" · ")
	meta.WriteString(it.freshness.Render())
	if it.contradicts {
		meta.WriteString(" · CONTRADICTS another team entry")
	}
	return fmt.Sprintf("- [team#%s] (%s, %s) %s", TeamHandle(it.uid), it.kind, meta.String(), it.content)
}
