package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/diag"
	"github.com/ebrahim5801/agent-brain-cli/internal/stats"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func newStatsCmd() *cobra.Command {
	var daily, weekly, asJSON, sessions, prompts bool
	var project, since, sessionID string
	cmd := &cobra.Command{
		Use:     "stats",
		Short:   "Show your AI usage: sessions, time, and tokens per project",
		PostRun: updatePostRun,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A session-id filter targets one exact session, so default to
			// searching all time rather than just the current week.
			if since == "" && sessionID != "" {
				// leave since empty: no lower bound
			} else if since == "" {
				since = currentWeekStart()
			} else if _, err := time.Parse("2006-01-02", since); err != nil {
				return fmt.Errorf("--since must be YYYY-MM-DD: %w", err)
			}

			st, err := store.Open()
			if err != nil {
				return fmt.Errorf("open local store: %w", err)
			}
			defer st.Close()
			logPath, _ := store.DiagLogPath()
			reconcile(st, &diag.Logger{DB: st.DB, Rebind: st.Rebind, FallbackPath: logPath})

			filter := stats.Filter{Since: since, Project: project, SessionID: sessionID}
			if sessions || sessionID != "" || prompts {
				rows, err := stats.Sessions(st, filter)
				if err != nil {
					return err
				}
				if asJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(rows)
				}
				stats.RenderSessions(os.Stdout, rows, since)
				if prompts {
					stats.RenderSessionPrompts(os.Stdout, rows)
				}
				return nil
			}
			if daily {
				filter.Group = "daily"
			} else if weekly {
				filter.Group = "weekly"
			}
			rows, err := stats.Query(st, filter)
			if err != nil {
				return err
			}
			if asJSON {
				return stats.RenderJSON(os.Stdout, rows, since)
			}
			stats.RenderTable(os.Stdout, rows, since, filter.Group != "")
			return nil
		},
	}
	cmd.Flags().BoolVar(&daily, "daily", false, "group by day")
	cmd.Flags().BoolVar(&weekly, "weekly", false, "group by week")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	cmd.Flags().BoolVar(&sessions, "sessions", false, "list individual sessions with their summaries")
	cmd.Flags().StringVar(&sessionID, "session", "", "list only the session with this id (accepts the short id prefix)")
	cmd.Flags().BoolVar(&prompts, "prompts", false, "also print each sub-session's full spawn prompt (stored locally, never synced)")
	cmd.Flags().StringVar(&project, "project", "", "filter to one project by name")
	cmd.Flags().StringVar(&since, "since", "", "start date YYYY-MM-DD (default: start of the current week)")
	cmd.MarkFlagsMutuallyExclusive("daily", "weekly", "sessions")
	cmd.MarkFlagsMutuallyExclusive("daily", "prompts")
	cmd.MarkFlagsMutuallyExclusive("weekly", "prompts")
	return cmd
}

func currentWeekStart() string {
	now := time.Now().UTC()
	daysSinceMonday := (int(now.Weekday()) + 6) % 7
	return now.AddDate(0, 0, -daysSinceMonday).Format("2006-01-02")
}
