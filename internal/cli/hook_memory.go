package cli

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/diag"
	"github.com/ebrahim5801/agent-brain-cli/internal/entitlement"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/memsync"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// distillationReason is the exact Stop-hook block prompt from
// contracts/session-injection.md.
const distillationReason = "Before finishing: distill this session's durable context into project memory. " +
	"Save each decision made, convention established, task state left in flight, and non-obvious fact learned " +
	"as a separate agent-brain-memory memory_save call (kind: decision|convention|task_state|fact, origin: auto). " +
	"Supersede any existing memory entry this session contradicted (supersedes:[id]). " +
	"Mark anything that is personal working context rather than shared team knowledge with personal_only: true. " +
	"Do not save secrets, credentials, or session trivia. " +
	"Also call session_summary once with 2-3 sentences covering the whole session so far (the goal, what changed, the outcome), replacing any earlier summary — it stays on this machine. " +
	"If nothing durable happened, save nothing. Then stop."

// checkpointReason is the mid-turn checkpoint prompt injected while the agent
// is still working (before context compaction, or after enough tool activity).
// Unlike distillationReason it saves only what is durable so far, never calls
// session_summary (a whole-session, Stop-only concern), and tells the model to
// resume its task rather than stop.
const checkpointReason = "Checkpoint before you lose context: save any durable context learned so far " +
	"(each decision made, convention established, task state in flight, and non-obvious fact) " +
	"as a separate agent-brain-memory memory_save call (kind: decision|convention|task_state|fact, origin: auto). " +
	"Supersede any existing memory entry this session contradicted (supersedes:[id]). " +
	"Mark anything that is personal working context rather than shared team knowledge with personal_only: true. " +
	"Do not save secrets, credentials, or session trivia, and do not re-save anything already captured this session. " +
	"Do not call session_summary. If nothing durable is worth saving yet, save nothing. " +
	"Then continue with your task."

// checkpointToolThreshold is the number of tool_use events since the last
// checkpoint that arms the tool-use checkpoint trigger. Conservative so mid-turn
// interruptions stay rare. The pre-compact trigger uses threshold 1. A var, not
// a const, so tests can lower it without driving dozens of hooks.
var checkpointToolThreshold = 25

// memoryHookActive evaluates the gate shared by both memory hook stages.
// Any failure reads as inactive: the serve/capture paths must never error a
// session (FR-011).
func memoryHookActive(st *store.Store, logger *diag.Logger, projectID int64) bool {
	cfg, err := config.Load()
	if err != nil {
		logger.Log("memory", "load config: %v", err)
		return false
	}
	disabled, err := st.ProjectMemoryDisabled(projectID)
	if err != nil {
		logger.Log("memory", "project disabled flag: %v", err)
		return false
	}
	now := time.Now()
	if entitlement.MemoryActive(cfg, disabled, now) {
		return true
	}
	// Free-tier members on a paying team's project still capture and serve
	// (clarification Q1): team memory is a membership benefit.
	link, _ := teamLink(st, cfg, projectID)
	return entitlement.TeamMemoryActive(cfg, link, disabled)
}

// teamLink resolves the config link for a local project id via its identity.
func teamLink(st *store.Store, cfg *config.Config, projectID int64) (config.Link, bool) {
	var kind, identity string
	if err := st.QueryRow(`SELECT identity_kind, identity FROM projects WHERE id = ?`, projectID).Scan(&kind, &identity); err != nil {
		return config.Link{}, false
	}
	link, ok := cfg.Links[config.LinkKey(kind, identity)]
	return link, ok
}

// Injection stage budgets (contracts/session-injection.md): the whole stage
// gets 1 s; git subprocesses within it get 500 ms.
const (
	injectionDeadline  = time.Second
	injectionGitBudget = 500 * time.Millisecond
	// injectionPullBudget bounds the best-effort team pull so pack assembly
	// still fits inside injectionDeadline.
	injectionPullBudget = 400 * time.Millisecond
)

// promptWalkMaxSessionEntries caps how many memories one session may be served
// in total (session-start pack, per-prompt walk, and MCP searches all count,
// since all record retrieval). It bounds the dedup-walk, which by construction
// never repeats an entry and would otherwise keep descending the ranking into
// the stalest tail. A var, not a const, so tests can lower it.
var promptWalkMaxSessionEntries = 60

// sessionStartInjection builds the memory pack for SessionStart
// (contracts/session-injection.md). It returns the pack (text plus counts);
// the caller wraps the text in the adapter's context-injection envelope. Any
// failure, gate, empty pack, or deadline overrun yields a zero Pack — the
// session must never be blocked (FR-011). A non-empty pack also records
// retrieval for every entry it serves.
func sessionStartInjection(st *store.Store, logger *diag.Logger, sessionID, projectID int64, cwd string) memory.Pack {
	stageStart := time.Now()
	if !memoryHookActive(st, logger, projectID) {
		return memory.Pack{}
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Log("memory", "load config: %v", err)
		return memory.Pack{}
	}

	// Best-effort team pull within the stage deadline: refresh the cache so the
	// pack is as current as possible, but never block on it (R1). Abandoned on
	// timeout — serving reads whatever the cache holds.
	pullDone := make(chan struct{}, 1)
	go func() {
		if err := memsync.New().PullOne(st, projectID); err != nil {
			logger.Log("memory", "session-start pull: %v", err)
		}
		pullDone <- struct{}{}
	}()
	select {
	case <-pullDone:
	case <-time.After(injectionPullBudget):
	}

	type result struct {
		pack memory.Pack
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		// recover must live in the panicking goroutine: runHook's recover
		// covers only its own. Without this a panic below (git state on a
		// corrupt checkout, say) takes the whole process down with exit 2,
		// which assistants read as a blocking hook error (FR-011).
		defer func() {
			if r := recover(); r != nil {
				logger.Log("memory", "build pack panicked: %v", r)
				ch <- result{}
			}
		}()
		pack, err := memory.BuildPack(st, projectID, cwd, cfg.MemoryPackBudgetTokens, injectionGitBudget, time.Now())
		ch <- result{pack, err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			logger.Log("memory", "build pack: %v", res.err)
			return memory.Pack{}
		}
		recordPackRetrieval(st, logger, sessionID, projectID, res.pack)
		return res.pack
	case <-time.After(injectionDeadline - time.Since(stageStart)):
		// The 1 s deadline covers the whole stage — the pull wait above spends
		// part of it, so pack assembly only gets the remainder.
		logger.Log("memory", "injection deadline exceeded, serving nothing")
		return memory.Pack{}
	}
}

// promptInjection builds the per-prompt memory pack: the highest-ranked entries
// this session has not already been shown (dedup-walk), so every prompt (or the
// first tool-use of a turn, for the tool-use fallback) refreshes the model's
// memory before it acts. The prompt text is never needed — relevance is the
// existing rank plus cwd/git freshness — so nothing crosses the ParseHook
// privacy boundary (constitution II). Unlike sessionStartInjection it does no
// team pull (session-start already refreshed the cache) and serves from cache
// to stay fast. Any failure, gate, empty pack, or deadline overrun yields a
// zero Pack (FR-011).
//
// It does NOT record retrieval: the caller records only once it commits to
// emitting the pack, so a pack built but then discarded (a lost claim race)
// does not mark entries seen and hide them from every later injection.
func promptInjection(st *store.Store, logger *diag.Logger, sessionID, projectID int64, cwd string) memory.Pack {
	stageStart := time.Now()
	if !memoryHookActive(st, logger, projectID) {
		return memory.Pack{}
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Log("memory", "load config: %v", err)
		return memory.Pack{}
	}
	seenPersonal, seenTeam := retrievedSets(st, logger, sessionID)
	// The walk excludes what was already served, so without a session-wide
	// ceiling a long session would march down the ranking and eventually inject
	// the entire project's memory — the per-injection token budget alone bounds
	// nothing cumulatively. Past the cap the model keeps what it has and reaches
	// for memory_search instead.
	if len(seenPersonal)+len(seenTeam) >= promptWalkMaxSessionEntries {
		return memory.Pack{}
	}

	type result struct {
		pack memory.Pack
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Log("memory", "build prompt pack panicked: %v", r)
				ch <- result{}
			}
		}()
		pack, err := memory.BuildPackExcluding(st, projectID, cwd, cfg.MemoryPackBudgetTokens, injectionGitBudget, time.Now(), seenPersonal, seenTeam)
		ch <- result{pack, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			logger.Log("memory", "build prompt pack: %v", res.err)
			return memory.Pack{}
		}
		return res.pack
	case <-time.After(injectionDeadline - time.Since(stageStart)):
		logger.Log("memory", "prompt injection deadline exceeded, serving nothing")
		return memory.Pack{}
	}
}

// retrievedSets reads which memories this session has already been served, so
// per-prompt injection can walk past them. Best-effort: on error it returns
// empty sets, which at worst re-serves an already-seen entry — never a block.
func retrievedSets(st *store.Store, logger *diag.Logger, sessionID int64) (map[int64]bool, map[string]bool) {
	personal := map[int64]bool{}
	team := map[string]bool{}
	refs, err := st.SessionRetrievedRefs(sessionID)
	if err != nil {
		logger.Log("memory", "session retrieved refs: %v", err)
		return personal, team
	}
	for _, r := range refs {
		switch r.Scope {
		case "personal":
			personal[r.MemoryID] = true
		case "team":
			team[r.TeamUID] = true
		}
	}
	return personal, team
}

// recordPackRetrieval records retrieval for every entry a served pack actually
// carried — session-start, per-prompt walk, or tool-use fallback. The
// dedup-walk depends on it: an unrecorded entry is served again next prompt.
// Best-effort (FR-011): a failure is logged to diagnostics
// and never blocks the session. sessionID == 0 (no session row yet) is
// skipped rather than attempted.
func recordPackRetrieval(st *store.Store, logger *diag.Logger, sessionID, projectID int64, pack memory.Pack) {
	if sessionID == 0 {
		return
	}
	at := store.Now()
	for _, id := range pack.PersonalIDs {
		if err := st.RecordRetrieval(sessionID, projectID, "personal", id, "", at); err != nil {
			logger.Log("memory", "record retrieval: %v", err)
		}
	}
	for _, uid := range pack.TeamUIDs {
		if err := st.RecordRetrieval(sessionID, projectID, "team", 0, uid, at); err != nil {
			logger.Log("memory", "record retrieval: %v", err)
		}
	}
}

// injectionNotice renders the user-facing session-start line for adapters
// with a display channel (assistant.InjectionNotifier). It reports what was
// actually served, never the eligible total alone, so the number is proof the
// pack reached the session. Empty packs yield "" — no "loaded 0" noise.
func injectionNotice(p memory.Pack) string {
	if p.Served() == 0 {
		return ""
	}
	noun := "memories"
	if p.Served() == 1 {
		noun = "memory"
	}
	var b strings.Builder
	if p.Active > p.Served() {
		fmt.Fprintf(&b, "agent-brain: loaded %d of %d %s", p.Served(), p.Active, noun)
	} else {
		fmt.Fprintf(&b, "agent-brain: loaded %d %s", p.Served(), noun)
	}
	if p.Team > 0 {
		fmt.Fprintf(&b, " (%d personal, %d team)", p.Personal, p.Team)
	}
	b.WriteString(" into context — inspect with: agent-brain memory list")
	return b.String()
}

// stopDistillation implements the distillation gate
// (contracts/session-injection.md). It returns the distillation prompt to emit
// (the caller wraps it in the adapter's stop-redirect envelope), or "" for
// silent pass-through. The distillation marker is claimed before returning a
// prompt so the post-distillation Stop always passes through; the gate re-arms
// only when a new prompt arrived after the last distillation.
func stopDistillation(st *store.Store, logger *diag.Logger, sessionID, projectID int64) string {
	if !memoryHookActive(st, logger, projectID) {
		return ""
	}
	prompts, toolUses, err := st.SessionActivity(sessionID)
	if err != nil {
		logger.Log("memory", "session activity: %v", err)
		return ""
	}
	if prompts < 1 || toolUses < 1 {
		return ""
	}
	won, err := st.MarkSessionDistilled(sessionID, store.Now())
	if err != nil {
		logger.Log("memory", "mark distilled: %v", err)
		return ""
	}
	if !won {
		return ""
	}
	return distillationReason
}

// recordCitations implements the citation pass (contracts/session-injection.md
// Phase 3): parse the session's transcript once at SessionEnd, intersect
// referenced ids/handles against what this session actually retrieved, and
// mark only the intersection cited. The intersection guard is enforced here,
// not in the regex — an arbitrary `#123` (an issue/PR number, or the pack's
// own instruction example) never gets marked unless it coincides with a
// served id. Best-effort throughout (FR-011): any failure is a silent
// pass-through, never disturbing the session.
func recordCitations(st *store.Store, logger *diag.Logger, adapter assistant.Adapter, sessionID, projectID int64, input assistant.HookInput) {
	if !memoryHookActive(st, logger, projectID) {
		return
	}
	cs, ok := adapter.(assistant.CitationScanner)
	if !ok {
		return
	}
	if input.TranscriptPath == "" {
		var stored sql.NullString
		if err := st.QueryRow(`SELECT transcript_path FROM sessions WHERE id = ?`, sessionID).Scan(&stored); err == nil && stored.Valid {
			input.TranscriptPath = stored.String
		}
	}
	personalIDs, teamHandles, ok := cs.ScanCitations(input)
	if !ok {
		return
	}

	refs, err := st.SessionRetrievedRefs(sessionID)
	if err != nil {
		logger.Log("memory", "session retrieved refs: %v", err)
		return
	}
	retrievedPersonal := map[int64]bool{}
	retrievedTeam := map[string]bool{}
	for _, r := range refs {
		switch r.Scope {
		case "personal":
			retrievedPersonal[r.MemoryID] = true
		case "team":
			retrievedTeam[r.TeamUID] = true
		}
	}

	at := store.Now()
	for _, id := range personalIDs {
		if !retrievedPersonal[id] {
			continue
		}
		if err := st.MarkCited(sessionID, "personal", id, "", at); err != nil {
			logger.Log("memory", "mark cited: %v", err)
		}
	}
	for _, handle := range teamHandles {
		uid, err := st.ResolveTeamHandle(projectID, handle)
		if err != nil {
			continue
		}
		if !retrievedTeam[uid] {
			continue
		}
		if err := st.MarkCited(sessionID, "team", 0, uid, at); err != nil {
			logger.Log("memory", "mark cited: %v", err)
		}
	}
}

// checkpointCapture implements the mid-session checkpoint gate. It mirrors
// stopDistillation: same memory-active gate, then an atomic claim that arms only
// when at least threshold tool_use events have accrued since the last
// checkpoint. It returns the checkpoint prompt to emit (the caller wraps it in
// the adapter's mid-turn envelope), or "" for silent pass-through. Any failure
// reads as pass-through — a checkpoint must never disturb the session (FR-011).
func checkpointCapture(st *store.Store, logger *diag.Logger, sessionID, projectID int64, threshold int) string {
	if !memoryHookActive(st, logger, projectID) {
		return ""
	}
	won, err := st.MarkSessionCheckpointed(sessionID, store.Now(), threshold)
	if err != nil {
		logger.Log("memory", "mark checkpointed: %v", err)
		return ""
	}
	if !won {
		return ""
	}
	return checkpointReason
}
