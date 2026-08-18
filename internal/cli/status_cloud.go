package cli

import (
	"fmt"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// printCloudStatus reports the machine's cloud posture; fully offline-safe
// (it only reads local config and the local store).
func printCloudStatus(st *store.Store) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("Cloud:            config unreadable: %v\n", err)
		return
	}
	fmt.Println("Cloud:")
	if cfg.SignedIn() {
		fmt.Printf("  Account:        %s (signed in)\n", cfg.AccountEmail)
	} else {
		fmt.Println("  Account:        not signed in — run `agent-brain login` to sync to the cloud")
	}
	if cfg.TelemetryConsented() {
		fmt.Printf("  Consent:        telemetry granted %s\n", cfg.Consent.Telemetry.GrantedAt)
	} else {
		fmt.Println("  Consent:        not granted — nothing leaves this machine")
	}

	linked := 0
	localOnly := 0
	rows, err := st.Query(`SELECT identity_kind || ':' || identity FROM projects`)
	if err == nil {
		for rows.Next() {
			var identity string
			if rows.Scan(&identity) != nil {
				continue
			}
			if _, ok := cfg.Links[identity]; ok {
				linked++
			} else {
				localOnly++
			}
		}
		rows.Close()
	}
	fmt.Printf("  Projects:       %d linked, %d local-only\n", linked, localOnly)
	printDaemonStatus(cfg, st)
}

// printDaemonStatus is extended by the sync feature; before it ships there is
// no daemon to report on.
var printDaemonStatus = func(cfg *config.Config, st *store.Store) {}
