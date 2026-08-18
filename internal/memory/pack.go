package memory

import (
	"fmt"
	"strings"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// DefaultPackBudgetTokens bounds the session-start memory pack; the token
// estimate is the standard chars/4 proxy (clarification #4).
const DefaultPackBudgetTokens = 2000

const packHeader = `## Project memory (agent-brain)

Memory from your previous sessions in this project, most relevant first.
This is the authoritative project memory: before acting on any task, call
memory_search with the task's keywords — do not rely on other memory files
or re-derive from code what these entries already answer.
Trust entries marked current; verify entries marked moved-on/different-branch/unverifiable
against the code before relying on them. Use the agent-brain-memory MCP tools:
memory_search to retrieve more, memory_save to record new durable context
(pass supersedes:[id] when a personal entry below is outdated; to replace or
contradict a team entry, pass its team#handle to supersedes_team).
The user cannot see this pack: when you rely on an entry, restate its gist
alongside the id (e.g. "memory #12 (use the staging DB for tests)"), never
the bare id; they can inspect one with "agent-brain memory show <id>".

`

const promptPackHeader = `## Project memory (agent-brain) — more relevant entries

Additional memory from your previous sessions in this project, most relevant
first, that was not already shown this session. Check it before acting on the
current request. Trust entries marked current; verify moved-on/different-branch
ones against the code. Use the agent-brain-memory MCP tools (memory_search,
memory_save). When you rely on an entry, restate its gist alongside the id
(e.g. "memory #12 (...)"), never the bare id.

`

// RenderEntry is the standard serving format shared by the pack, search, and
// list (contracts/mcp-memory.md): [#<id>] (<kind>, <origin>, <freshness>) <content>
func RenderEntry(r Ranked) string {
	return renderPersonalLine(r.Entry.ID, r.Entry.Kind, r.Entry.Origin, r.Freshness.Render(), r.Entry.Content)
}

// renderPersonalLine is the single definition of the personal serving line,
// used by both the MCP results and the merged pack.
func renderPersonalLine(id int64, kind, origin, freshness, content string) string {
	return fmt.Sprintf("[#%d] (%s, %s, %s) %s", id, kind, origin, freshness, content)
}

// TeamHandleLen is how many leading uid characters form a team entry's handle.
// team_uids are uuids, so 8 hex characters are ample to stay unique within a
// project pool while keeping the rendered handle short.
const TeamHandleLen = 8

// TeamHandle is the stable, uid-derived handle rendered for a cached team entry
// (e.g. "abc12345" → shown as "[team#abc12345]"). It is derived from the uid, so
// it survives re-ranking between sessions; the store resolves it back to the
// full team_uid by prefix (Store.ResolveTeamHandle).
func TeamHandle(uid string) string {
	if len(uid) > TeamHandleLen {
		return uid[:TeamHandleLen]
	}
	return uid
}

// Pack is a rendered session-start memory pack plus the honest counts behind
// it: Personal and Team are the entries actually served in Text, Active is how
// many were eligible before the budget cut. The counts feed the user-facing
// session-start notice.
type Pack struct {
	Text        string
	Personal    int
	Team        int
	Active      int
	PersonalIDs []int64
	TeamUIDs    []string
}

// Served is the number of entries actually rendered into Text.
func (p Pack) Served() int { return p.Personal + p.Team }

// BuildPack ranks the union of a project's active personal entries and its
// active cached team entries, then fills the token budget greedily. An empty
// pack returns a zero Pack — the caller emits nothing at all. budgetTokens ≤ 0
// means the default. Team entries render with author attribution and are
// freshness-evaluated against the reader's own checkout (Constitution V, US5).
func BuildPack(st *store.Store, projectID int64, dir string, budgetTokens int, gitBudget time.Duration, now time.Time) (Pack, error) {
	personal, err := st.ListMemories(projectID, false)
	if err != nil {
		return Pack{}, err
	}
	team, err := st.ListTeamMemories(projectID)
	if err != nil {
		return Pack{}, err
	}
	return assemblePack(packHeader, personal, team, dir, budgetTokens, gitBudget, now), nil
}

// BuildPackExcluding builds a pack like BuildPack but omits entries already
// served this session — seenPersonal keyed by personal id, seenTeam by team
// uid. It backs the per-prompt dedup-walk: each prompt surfaces the next batch
// of highest-ranked memory the model has not seen yet this session, under the
// same ranking and budget rules as BuildPack. It renders promptPackHeader so
// the model reads it as a refresh, not a duplicate of the session-start pack.
func BuildPackExcluding(st *store.Store, projectID int64, dir string, budgetTokens int, gitBudget time.Duration, now time.Time, seenPersonal map[int64]bool, seenTeam map[string]bool) (Pack, error) {
	personal, err := st.ListMemories(projectID, false)
	if err != nil {
		return Pack{}, err
	}
	team, err := st.ListTeamMemories(projectID)
	if err != nil {
		return Pack{}, err
	}
	if len(seenPersonal) > 0 {
		kept := personal[:0:0]
		for _, e := range personal {
			if !seenPersonal[e.ID] {
				kept = append(kept, e)
			}
		}
		personal = kept
	}
	if len(seenTeam) > 0 {
		kept := team[:0:0]
		for _, e := range team {
			if !seenTeam[e.UID] {
				kept = append(kept, e)
			}
		}
		team = kept
	}
	return assemblePack(promptPackHeader, personal, team, dir, budgetTokens, gitBudget, now), nil
}

// assemblePack ranks the personal+team union and greedily fills the token
// budget under the given header. An empty result (no entries, or nothing fits)
// is a zero Pack so the caller emits nothing.
func assemblePack(header string, personal []store.Memory, team []store.TeamMemoryRow, dir string, budgetTokens int, gitBudget time.Duration, now time.Time) Pack {
	if len(personal) == 0 && len(team) == 0 {
		return Pack{}
	}
	if budgetTokens <= 0 {
		budgetTokens = DefaultPackBudgetTokens
	}
	budgetChars := budgetTokens * 4

	cmp := gitstate.NewComparer(dir, gitBudget)
	items := mergeRank(personal, team, cmp, now)

	var b strings.Builder
	b.WriteString(header)
	pack := Pack{Active: len(items)}
	count := func(it packItem) {
		if it.isTeam {
			pack.Team++
			pack.TeamUIDs = append(pack.TeamUIDs, it.uid)
		} else {
			pack.Personal++
			pack.PersonalIDs = append(pack.PersonalIDs, it.id)
		}
	}
	for _, it := range items {
		line := renderItem(it) + "\n"
		if b.Len()+len(line) > budgetChars {
			if pack.Served() == 0 {
				// Header + first entry overflow: serve the first entry
				// anyway rather than an empty pack with a header.
				b.WriteString(line)
				count(it)
			}
			break
		}
		b.WriteString(line)
		count(it)
	}
	if pack.Served() == 0 {
		return Pack{}
	}
	pack.Text = b.String()
	return pack
}
