package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// seedExistingMemories offers, once on first grant, a reviewable list of the
// project's pre-existing personal memories to contribute selectively. Default
// is none: transferring or sharing never auto-shares prior memory (FR-016, R13).
func seedExistingMemories(linkKey, projectKey string, nonInteractive bool) error {
	st, err := store.Open()
	if err != nil {
		return err
	}
	defer st.Close()

	projectID, ok := localProjectForLink(st, linkKey)
	if !ok {
		return nil
	}
	candidates, err := st.UnsharedMemories(projectID)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}

	// Non-interactive grants never auto-seed (safe default).
	if nonInteractive {
		fmt.Printf("%d existing personal entries were NOT shared. Run `agent-brain memory share` interactively to pick any to contribute.\n", len(candidates))
		return nil
	}
	if fi, serr := os.Stdin.Stat(); serr != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return nil
	}

	fmt.Printf("\nThis project has %d existing personal memories. Choose which (if any) to share:\n", len(candidates))
	for _, m := range candidates {
		fmt.Printf("  %d  %s\n", m.ID, firstLine(m.Content))
	}
	fmt.Print("Enter ids to share (comma-separated), or blank for none: ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.TrimSpace(answer)
	if answer == "" {
		fmt.Println("No existing entries shared.")
		return nil
	}

	valid := map[int64]bool{}
	for _, m := range candidates {
		valid[m.ID] = true
	}
	shared := 0
	for _, tok := range strings.Split(answer, ",") {
		id, perr := parseMemoryID(strings.TrimSpace(tok))
		if perr != nil || !valid[id] {
			fmt.Printf("skipped %q (not an eligible id)\n", strings.TrimSpace(tok))
			continue
		}
		if err := st.MintShareUID(id); err != nil {
			fmt.Printf("skipped %d (%v)\n", id, err)
			continue
		}
		shared++
	}
	fmt.Printf("Queued %d existing entries to contribute on the next sync.\n", shared)
	return nil
}

// localProjectForLink finds the local project id for a config link key.
func localProjectForLink(st *store.Store, linkKey string) (int64, bool) {
	kind, identity, ok := splitLinkKey(linkKey)
	if !ok {
		return 0, false
	}
	var id int64
	if err := st.QueryRow(`SELECT id FROM projects WHERE identity_kind = ? AND identity = ?`, kind, identity).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
}

// splitLinkKey inverts config.LinkKey (kind + ":" + identity).
func splitLinkKey(linkKey string) (kind, identity string, ok bool) {
	return strings.Cut(linkKey, ":")
}
