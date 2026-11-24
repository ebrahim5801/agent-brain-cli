package memory

import (
	"fmt"
	"strings"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// MinSessionPrefix is the shortest session ID prefix `--session` accepts.
const MinSessionPrefix = 8

// SessionLabel is the session ID people see: Memory V2 stores a bound
// session as "v2:<handle>", and the handle is what hook messages show.
func SessionLabel(externalID string) string {
	return strings.TrimPrefix(externalID, "v2:")
}

// ResolveSession finds the session ref names among the sessions that saved
// memories: its full ID, or a unique prefix of at least MinSessionPrefix
// characters, with or without the "v2:" a bound session is stored with.
func ResolveSession(sessions []store.MemorySession, ref string) (store.MemorySession, error) {
	ref = SessionLabel(strings.TrimSpace(ref))
	var matches []store.MemorySession
	for _, s := range sessions {
		label := SessionLabel(s.ExternalID)
		if label == ref {
			return s, nil
		}
		if len(ref) >= MinSessionPrefix && strings.HasPrefix(label, ref) {
			matches = append(matches, s)
		}
	}
	switch {
	case len(matches) == 1:
		return matches[0], nil
	case len(matches) > 1:
		return store.MemorySession{}, fmt.Errorf("session %q matches %d sessions; give more of its ID", ref, len(matches))
	case len(ref) < MinSessionPrefix:
		return store.MemorySession{}, fmt.Errorf("give at least %d characters of the session ID; `agent-brain memory show <id>` shows an entry's session", MinSessionPrefix)
	}
	return store.MemorySession{}, fmt.Errorf("no session %q saved memories in this project; `agent-brain memory show <id>` shows an entry's session", ref)
}

// SessionCleanup is what deleting one session's memories changes.
type SessionCleanup struct {
	// Delete holds the session's active entries.
	Delete []store.Memory
	// Restore holds entries from other sessions that the session's entries
	// replaced: they stay hidden while superseded, even by a deleted entry.
	Restore []store.Memory
}

// PlanSessionCleanup picks a session's active entries and the entries from
// elsewhere that they replaced. entries must include superseded ones.
func PlanSessionCleanup(entries []Ranked, sessionID int64) SessionCleanup {
	mine := map[int64]bool{}
	for _, r := range entries {
		if r.Entry.SessionID.Valid && r.Entry.SessionID.Int64 == sessionID {
			mine[r.Entry.ID] = true
		}
	}
	var plan SessionCleanup
	for _, r := range entries {
		e := r.Entry
		switch {
		case mine[e.ID] && e.Status == store.MemoryActive:
			plan.Delete = append(plan.Delete, e)
		case !mine[e.ID] && e.Status == store.MemorySuperseded && e.SupersededBy.Valid && mine[e.SupersededBy.Int64]:
			plan.Restore = append(plan.Restore, e)
		}
	}
	return plan
}
