package memory

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

type ScoreExplanation struct {
	Lexical  float64 `json:"lexical"`
	Metadata float64 `json:"metadata_multiplier"`
	Policy   string  `json:"policy"`
}

type SearchResult struct {
	Explanation *ScoreExplanation `json:"explanation,omitempty"`
	Text        string            `json:"text"`
	PersonalID  int64             `json:"memory_id,omitempty"`
	TeamUID     string            `json:"team_uid,omitempty"`
	Revision    int64             `json:"revision"`
	Score       float64           `json:"score"`
}

// SearchMerged gives authorized local personal/team candidates common corpus
// statistics. Neither source is silently starved by another source's limit.
func SearchMerged(st *store.Store, projectID int64, dir, query string, filter Filter, limit int, gitBudget time.Duration, now time.Time) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	personal, err := loadEntries(st, projectID, dir, true)
	if err != nil {
		return nil, err
	}
	team, err := st.ListTeamMemories(projectID)
	if err != nil {
		return nil, err
	}
	var docs []RetrievalDocument
	lookup := map[int64]store.Memory{}
	items := map[string]packItem{}
	explanations := map[string]*ScoreExplanation{}
	cmp := gitstate.NewComparer(dir, gitBudget)
	for _, m := range personal {
		lookup[m.ID] = m
		docs = append(docs, RetrievalDocument{Reference: "personal:" + strconv.FormatInt(m.ID, 10), Text: m.Content + " " + m.Details.Rationale})
	}
	for _, m := range team {
		ref := "team:" + m.UID
		docs = append(docs, RetrievalDocument{Reference: ref, Text: m.Content})
		if filter.matchesTeam(m) {
			items[ref] = teamItem(m, cmp, now)
		}
	}
	if query == "" {
		for _, m := range personal {
			if m.Status == store.MemoryActive && filter.Matches(m) {
				items["personal:"+strconv.FormatInt(m.ID, 10)] = personalItem(m, cmp, now)
			}
		}
	} else {
		scores := map[string]float64{}
		for _, c := range (BM25Provider{}).Candidates(docs, query) {
			ref := c.Reference
			if len(ref) > 9 && ref[:9] == "personal:" {
				id, _ := strconv.ParseInt(ref[9:], 10, 64)
				m := lookup[id]
				seen := map[int64]bool{}
				for m.Status == store.MemorySuperseded && m.SupersededBy.Valid && !seen[m.ID] {
					seen[m.ID] = true
					next, ok := lookup[m.SupersededBy.Int64]
					if !ok {
						break
					}
					m = next
				}
				if m.Status != store.MemoryActive || !filter.Matches(m) {
					continue
				}
				ref = "personal:" + strconv.FormatInt(m.ID, 10)
				items[ref] = personalItem(m, cmp, now)
			}
			if _, ok := items[ref]; ok && c.Score > scores[ref] {
				scores[ref] = c.Score
			}
		}
		for _, m := range personal {
			ref := "personal:" + strconv.FormatInt(m.ID, 10)
			score := scores[ref]
			if score == 0 {
				continue
			}
			for _, id := range conflictPeers(m) {
				peer, ok := lookup[id]
				if !ok || peer.Status != store.MemoryActive || !filter.Matches(peer) {
					continue
				}
				peerRef := "personal:" + strconv.FormatInt(id, 10)
				if scores[peerRef] == 0 {
					items[peerRef] = personalItem(peer, cmp, now)
					scores[peerRef] = score * 0.95
				}
			}
		}
		for ref, it := range items {
			if scores[ref] == 0 {
				delete(items, ref)
				continue
			}
			meta := it.entry
			if it.isTeam {
				meta = store.Memory{Kind: it.kind, Origin: it.origin, Priority: it.priority, CapturedAt: it.capturedAt}
			}
			if useV2Ranking() {
				adjustment := lexicalAdjustment(meta, now)
				explanations[ref] = &ScoreExplanation{scores[ref], adjustment, "bm25-v2"}
				it.score = scores[ref] * adjustment
			} else {
				explanations[ref] = &ScoreExplanation{scores[ref], it.score, "bm25-legacy-weights"}
				it.score *= scores[ref]
			}
			items[ref] = it
		}
	}
	type named struct {
		ref  string
		item packItem
	}
	var ranked []named
	for ref, item := range items {
		ranked = append(ranked, named{ref, item})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if itemLess(ranked[i].item, ranked[j].item) {
			return true
		}
		if itemLess(ranked[j].item, ranked[i].item) {
			return false
		}
		return ranked[i].ref < ranked[j].ref
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]SearchResult, 0, len(ranked))
	for _, it := range ranked {
		r := SearchResult{Text: renderItem(it.item), Score: it.item.score, Explanation: explanations[it.ref]}
		if it.item.isTeam {
			r.TeamUID = it.item.uid
		} else {
			r.PersonalID = it.item.id
			r.Revision = it.item.entry.Revision
		}
		out = append(out, r)
	}
	return out, nil
}
