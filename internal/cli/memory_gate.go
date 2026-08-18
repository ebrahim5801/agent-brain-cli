package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

const memoryConsentExplainer = `Personal memory captures durable context from your assistant sessions
(decisions, conventions, task state, key facts), distilled by the assistant
itself at session end, plus anything you explicitly ask it to remember.

  - Personal memory stays ONLY on this machine, in your local agent-brain
    database. It is never transmitted anywhere. (Projects owned by an
    organization can additionally share memory with the team, but only after
    a SEPARATE, explicit per-project gate — 'agent-brain memory share'.)
  - Memory is separate from telemetry: enabling it changes nothing about
    what telemetry consent covers, and vice versa.
  - Inspect with 'agent-brain memory list/show', correct with 'edit',
    remove with 'delete'/'wipe', and turn off per project with
    'agent-brain memory disable' — deletions are permanent and immediate.
  - Recognized secrets are redacted before anything is stored.`

func newMemoryEnableCmd() *cobra.Command {
	var project, yes bool
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Turn on personal memory (Pro), or re-enable it for this project",
		RunE: func(cmd *cobra.Command, args []string) error {
			if project {
				return setProjectMemoryDisabled(false)
			}
			return enableMemory(yes)
		},
	}
	cmd.Flags().BoolVar(&project, "project", false, "re-enable memory for the current project only")
	cmd.Flags().BoolVar(&yes, "yes", false, "accept the memory consent without the interactive prompt")
	return cmd
}

func newMemoryDisableCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Turn off memory for this project, or everywhere with --all",
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				return revokeMemoryConsent()
			}
			return setProjectMemoryDisabled(true)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "revoke memory consent machine-wide (stored entries are kept)")
	return cmd
}

// setProjectMemoryDisabled flips the per-project flag, creating the project
// row if this directory has never been seen (disabling before first use must
// stick).
func setProjectMemoryDisabled(disabled bool) error {
	st, err := store.Open()
	if err != nil {
		return err
	}
	defer st.Close()
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	proj := attribution.Resolve(dir)
	projectID, err := st.UpsertProject(store.ProjectIdentity{
		Kind: proj.Kind, Identity: proj.Identity, DisplayName: proj.DisplayName,
	}, store.Now())
	if err != nil {
		return err
	}
	if err := st.SetProjectMemoryDisabled(projectID, disabled); err != nil {
		return err
	}
	if disabled {
		fmt.Printf("Memory disabled for %s. Nothing will be captured or served here; other projects are unaffected.\n", proj.Identity)
	} else {
		fmt.Printf("Memory re-enabled for %s.\n", proj.Identity)
	}
	return nil
}

func revokeMemoryConsent() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.MemoryConsented() {
		fmt.Println("Memory was not enabled; nothing to disable.")
		return nil
	}
	if _, err := config.Update(func(c *config.Config) error {
		c.Consent.Memory = nil
		return nil
	}); err != nil {
		return err
	}
	fmt.Println("Memory disabled everywhere. Stored entries are kept on this machine;")
	fmt.Println("inspect or remove them with `agent-brain memory list/delete/wipe`.")
	return nil
}
