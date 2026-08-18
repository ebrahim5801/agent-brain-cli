package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/mcpserver"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/version"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "mcp",
		Short:  "Run the memory MCP server over stdio (spawned by the assistant)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
				fmt.Fprintln(os.Stderr, "agent-brain mcp speaks MCP over stdio; it is registered with your assistant by `agent-brain install` and not meant to be run interactively")
				os.Exit(2)
			}
			dir, err := os.Getwd()
			if err != nil {
				dir = "."
			}
			// A broken store must not break the session: the server still
			// completes the handshake and tools answer "temporarily
			// unavailable" (contracts/mcp-memory.md).
			st, err := store.Open()
			if err != nil {
				fallbackLogger().Log("mcp", "open store: %v", err)
				st = nil
			} else {
				defer st.Close()
			}
			return mcpserver.New(st, dir, version.Version).Run(cmd.Context())
		},
	}
}
