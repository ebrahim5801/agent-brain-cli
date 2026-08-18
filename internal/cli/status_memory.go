package cli

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/entitlement"
	"github.com/ebrahim5801/agent-brain-cli/internal/memsync"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// printMemoryStatus adds the memory line to `agent-brain status`
// (contracts/cli-memory.md). It refreshes the entitlement best-effort with a
// short timeout; a stale cache is shown with its age rather than blocking.
func printMemoryStatus(st *store.Store) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("Memory:           config unreadable: %v\n", err)
		return
	}
	if cfg.SignedIn() {
		if refreshed, err := entitlement.Refresh(&http.Client{}, 2*time.Second); err == nil {
			cfg = refreshed
		}
	}

	now := time.Now()
	var state string
	switch {
	case !cfg.MemoryConsented() && entitlement.Entitled(cfg, now):
		state = "off — enable with `agent-brain memory enable`"
	case !cfg.MemoryConsented():
		state = "off"
	case entitlement.Paused(cfg, now):
		state = fmt.Sprintf("paused (entitlement last verified %s, > 7 days ago) — reconnect to re-verify", cfg.Entitlement.VerifiedAt)
	case !entitlement.Entitled(cfg, now):
		state = "pro required — upgrade to activate"
	default:
		state = fmt.Sprintf("active (pro verified %s)", cfg.Entitlement.VerifiedAt)
	}

	projectID, identity, found, err := currentProject(st)
	if err == nil && found {
		if disabled, derr := st.ProjectMemoryDisabled(projectID); derr == nil && disabled {
			state += " — disabled for this project"
		}
		var count int
		if st.QueryRow(`SELECT COUNT(*) FROM memories WHERE project_id = ?`, projectID).Scan(&count) == nil && count > 0 {
			state += fmt.Sprintf("; %d entries for %s", count, identity)
		}
	}
	fmt.Printf("Memory:           %s\n", state)

	if found {
		printTeamMemoryStatus(st, cfg, projectID)
	}
}

// printTeamMemoryStatus adds the per-project team-sharing line: sharing state,
// cached capability, queued-contribution depth, and cached pool size.
func printTeamMemoryStatus(st *store.Store, cfg *config.Config, projectID int64) {
	link, ok := teamLink(st, cfg, projectID)
	if !ok || (!link.TeamMemory && !link.ShareGranted()) {
		return
	}
	var share string
	switch {
	case link.SharePaused():
		share = "paused"
	case link.ShareGranted():
		share = "on"
	default:
		share = "off (reading only)"
	}
	queued := 0
	if pending, err := st.PendingContributions(projectID); err == nil {
		queued = len(pending)
	}
	cached := 0
	if rows, err := st.ListTeamMemories(projectID); err == nil {
		cached = len(rows)
	}
	capability := "no"
	if link.TeamMemory {
		capability = "yes"
	}
	fmt.Printf("Team memory:      sharing: %s; queued: %d; team entries cached: %d; capability: %s\n", share, queued, cached, capability)
	endpoint := "vendor cloud"
	if link.MemoryEndpoint != "" {
		endpoint = link.MemoryEndpoint
	}
	fmt.Printf("                  memory endpoint: %s\n", endpoint)
	if _, err := memsync.MemoryBaseURL(cfg, link); errors.Is(err, memsync.ErrEndpointInsecure) {
		fmt.Println("                  endpoint refused: not https — memory is neither sent nor pulled over cleartext")
	}
	if withheld, err := st.ShareErrors(projectID); err == nil {
		for _, e := range withheld {
			fmt.Printf("                  entry #%d not shared: %s\n", e.ID, e.Reason)
		}
	}
}
