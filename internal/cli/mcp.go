package cli

import (
	"fmt"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/mcpserver"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/version"
)

func newMCPCmd() *cobra.Command {
	var requiredData, requiredConfig string
	c := &cobra.Command{
		Use:    "mcp",
		Short:  "Run the memory MCP server over stdio (spawned by the assistant)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Hosts may sanitize child environments. Fail before opening any database
			// when a caller explicitly requires isolated paths; never use a fallback.
			if requiredConfig != "" {
				actual, err := config.Dir()
				if err != nil {
					return err
				}
				if store.CanonicalDir(actual) != store.CanonicalDir(requiredConfig) {
					return fmt.Errorf("refusing MCP startup: required configuration directory was not forwarded")
				}
			}
			if requiredData != "" {
				actual := os.Getenv("AGENT_BRAIN_DATA_DIR")
				if actual == "" || store.CanonicalDir(actual) != store.CanonicalDir(requiredData) {
					return fmt.Errorf("refusing MCP startup: required data directory was not forwarded")
				}
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				if os.Getenv("AGENT_BRAIN_PG_DSN") != "" || (cfg.Storage != nil && cfg.Storage.Backend == "postgres") {
					return fmt.Errorf("refusing MCP startup: required SQLite data directory conflicts with PostgreSQL selection")
				}
				if !filepath.IsAbs(requiredData) {
					return fmt.Errorf("required data directory must be absolute")
				}
			}

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
	c.Flags().StringVar(&requiredData, "require-data-dir", "", "fail before opening a store unless this exact SQLite test data directory is selected")
	c.Flags().StringVar(&requiredConfig, "require-config-dir", "", "fail before opening a store unless this exact configuration directory is selected")
	return c

}
