package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/memsync"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/syncer"
)

func newSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "sync",
		Short:   "Sync queued sessions to the cloud now",
		PostRun: updatePostRun,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open()
			if err != nil {
				return fmt.Errorf("open local store: %w", err)
			}
			defer st.Close()

			// Memory phase first: a strictly separate pipeline with its own
			// per-project consent, it runs even without telemetry consent.
			memRes, memErr := memsync.New().Run(st)
			if memErr != nil && !errors.Is(memErr, memsync.ErrNotSignedIn) {
				fmt.Printf("Team memory sync incomplete (%v); queued entries stay local.\n", memErr)
			} else if memRes.Contributed > 0 || memRes.Pulled > 0 || memRes.Rejected > 0 || memRes.Skipped > 0 || memRes.Held > 0 {
				fmt.Printf("Team memory: contributed %d, pulled %d.\n", memRes.Contributed, memRes.Pulled)
				if memRes.Rejected > 0 {
					fmt.Printf("%d memory entries were rejected by the server and will not be shared; they remain local.\n", memRes.Rejected)
				}
				if memRes.Held > 0 {
					fmt.Printf("%d memory entries are waiting on team pool space (the pool is at its plan limit); they retry automatically — free space in the shared pool or upgrade the plan.\n", memRes.Held)
				}
				if memRes.Skipped > 0 {
					fmt.Printf("%d memory entries were withheld from sharing; run `agent-brain status` for the reasons.\n", memRes.Skipped)
				}
			}

			engine := syncer.New()
			res, err := engine.Run(st)
			switch {
			case errors.Is(err, syncer.ErrNotSignedIn), errors.Is(err, syncer.ErrNoConsent), errors.Is(err, syncer.ErrReauth):
				// Telemetry gate unchanged (SC-004): the memory phase above is a
				// separate pipeline and has already run on its own consent.
				return err
			case err != nil:
				if res.Synced > 0 {
					fmt.Printf("Synced %d sessions before the connection failed; the rest stays queued.\n", res.Synced)
				}
				return fmt.Errorf("sync incomplete (data remains queued locally): %w", err)
			}
			if res.Synced == 0 && res.Rejected == 0 {
				fmt.Println("Up to date.")
				return nil
			}
			fmt.Printf("Synced %d sessions (%d projects). Up to date.\n", res.Synced, len(res.Projects))
			if res.Rejected > 0 {
				fmt.Printf("%d records were rejected; see `agent-brain status` diagnostics.\n", res.Rejected)
			}
			return nil
		},
	}
}
