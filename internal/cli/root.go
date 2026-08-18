package cli

import (
	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/version"
)

func Execute() int {
	root := &cobra.Command{
		Use:          "agent-brain",
		Short:        "Local collector and stats for AI coding assistant usage",
		Version:      version.Version,
		SilenceUsage: true,
	}
	root.AddCommand(newInstallCmd(), newUninstallCmd(), newHookCmd(), newStatusCmd(), newStatsCmd(),
		newLoginCmd(), newLogoutCmd(), newLinkCmd(), newUnlinkCmd(), newConsentCmd(),
		newSyncCmd(), newDaemonCmd(), newMCPCmd(), newMemoryCmd(), newStorageCmd(), newUpdateCmd())
	if err := root.Execute(); err != nil {
		return 1
	}
	return 0
}
