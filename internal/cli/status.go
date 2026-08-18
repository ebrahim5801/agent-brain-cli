package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Report integration health and collection activity",
		PostRun: updatePostRun,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open()
			if err != nil {
				return fmt.Errorf("open local store: %w", err)
			}
			defer st.Close()

			broken := printAssistantStatus()

			var size int64
			if fi, err := os.Stat(st.Path); err == nil {
				size = fi.Size()
			}
			fmt.Printf("Database:         %s (%d KB)\n", st.Path, size/1024)

			var projects, sessions, open int
			var lastEvent string
			_ = st.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&projects)
			_ = st.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions)
			_ = st.QueryRow(`SELECT COUNT(*) FROM sessions WHERE ended_at IS NULL`).Scan(&open)
			_ = st.QueryRow(`SELECT COALESCE(MAX(occurred_at), '') FROM events`).Scan(&lastEvent)
			fmt.Printf("Projects:         %d\n", projects)
			fmt.Printf("Sessions:         %d (%d open)\n", sessions, open)
			if lastEvent == "" {
				fmt.Println("Last event:       none yet — run a session in an integrated assistant and check again")
			} else {
				fmt.Printf("Last event:       %s\n", lastEvent)
			}

			printCloudStatus(st)
			printMemoryStatus(st)

			rows, err := st.Query(`SELECT occurred_at, component, message FROM diagnostics ORDER BY id DESC LIMIT 5`)
			if err == nil {
				printed := false
				for rows.Next() {
					var at, component, message string
					if rows.Scan(&at, &component, &message) == nil {
						if !printed {
							fmt.Println("Recent diagnostics:")
							printed = true
						}
						fmt.Printf("  %s %s: %s\n", at, component, message)
					}
				}
				rows.Close()
			}

			if broken {
				return errors.New("integration broken")
			}
			return nil
		},
	}
}

// printAssistantStatus writes one line per registry adapter and reports whether
// any integrated adapter is broken (partial hooks or unreadable settings) —
// the caller turns that into a nonzero exit. Not-detected, not-integrated, and
// unsupported states are informational and never break the exit code.
func printAssistantStatus() (broken bool) {
	for _, a := range assistant.Registry() {
		label := fmt.Sprintf("%-16s", a.DisplayName()+":")
		st, err := a.State()
		if err != nil {
			fmt.Printf("%s settings unreadable: %v\n", label, err)
			broken = true
			continue
		}
		if !st.Detected {
			fmt.Printf("%s not detected\n", label)
			continue
		}
		switch st.Tier {
		case assistant.TierNotIntegrated:
			fmt.Printf("%s detected, not integrated — run `agent-brain install`\n", label)
		case assistant.TierFull:
			fmt.Printf("%s hooked (%d/%d events), memory MCP registered — full%s\n",
				label, st.HookEvents, st.HookEventsWanted, noteSuffix(st.Notes))
		case assistant.TierMCPOnly:
			fmt.Printf("%s memory MCP registered — MCP-only (memory serving and explicit saves; no automatic telemetry or distillation)%s\n",
				label, noteSuffix(st.Notes))
		case assistant.TierPartial:
			fmt.Printf("%s partially hooked (%d/%d events) — run `agent-brain install` to repair\n",
				label, st.HookEvents, st.HookEventsWanted)
			broken = true
		case assistant.TierUnsupported:
			fmt.Printf("%s detected — unsupported (no usable integration surface)\n", label)
		}
	}
	return broken
}

func noteSuffix(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return " (" + strings.Join(notes, "; ") + ")"
}
