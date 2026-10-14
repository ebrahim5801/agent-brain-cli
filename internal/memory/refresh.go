package memory

import (
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// BuildRevisionRefresh does not walk into increasingly irrelevant history.
// Task-keyword recall is explicit MCP search; automatic refresh carries newly
// accepted lifecycle changes and corrections, even beyond the old exposure cap.
func BuildRevisionRefresh(st *store.Store, projectID, sessionID int64, dir string, budgetTokens int, gitBudget time.Duration, now time.Time) (Pack, error) {
	entries, err := loadEntries(st, projectID, dir, true)
	if err != nil {
		return Pack{}, err
	}
	seen, err := st.SessionSeenRevisions(sessionID, projectID)
	if err != nil {
		return Pack{}, err
	}
	var changed []store.Memory
	for _, m := range entries {
		if m.Revision == 0 && !m.Conflicted {
			continue
		}
		rev, ok := seen[m.ID]
		if !ok || rev < m.Revision {
			changed = append(changed, m)
		}
	}
	return assemblePack(promptPackHeader, changed, nil, dir, budgetTokens, gitBudget, now), nil
}
