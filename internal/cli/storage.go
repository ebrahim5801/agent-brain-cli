package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func newStorageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Inspect and switch the local storage backend (Basic SQLite / Advanced PostgreSQL)",
	}
	cmd.AddCommand(newStorageStatusCmd(), newStorageUseCmd())
	return cmd
}

// restartNotice names the long-running processes that must restart to pick up a
// backend change: they resolve the backend at process start, so a running
// daemon or MCP server keeps using the old store until restarted.
const restartNotice = "Restart any running agent-brain processes to pick up the change:\n" +
	"  - the background daemon (agent-brain daemon), and\n" +
	"  - the memory MCP server your assistant launched (restart the assistant, or its MCP connection)."

func newStorageUseCmd() *cobra.Command {
	var dsn string
	cmd := &cobra.Command{
		Use:       "use <postgres|sqlite>",
		Short:     "Switch the local store to PostgreSQL (Advanced) or back to SQLite (Basic)",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"postgres", "sqlite"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "postgres":
				return runUsePostgres(dsn)
			case "sqlite":
				return runUseSQLite()
			default:
				return fmt.Errorf("unknown backend %q — use \"postgres\" or \"sqlite\"", args[0])
			}
		},
	}
	cmd.Flags().StringVar(&dsn, "dsn", "", "PostgreSQL DSN, e.g. postgres://user:pass@localhost:5432/agentbrain?sslmode=disable")
	return cmd
}

func runUsePostgres(dsn string) error {
	if strings.TrimSpace(dsn) == "" {
		return fmt.Errorf("provide the target database with --dsn (e.g. postgres://user:pass@localhost:5432/agentbrain?sslmode=disable)")
	}

	// The migration source is always the SQLite file. If the store is already
	// on Postgres, running this again would copy the stale SQLite backup into
	// the new target and flip the config — silently abandoning everything
	// written during the Postgres era. Refuse instead.
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Storage != nil && cfg.Storage.Backend == string(store.BackendPostgres) {
		return fmt.Errorf("the local store is already on PostgreSQL (%s) — this command only migrates the SQLite data, so switching between Postgres databases would abandon your current data; move it with pg_dump/pg_restore and update the DSN in the config, or run agent-brain storage use sqlite first if you really want to re-migrate the SQLite backup",
			config.RedactDSN(cfg.Storage.DSN))
	}

	// Source: the current Basic-mode SQLite store. Open it directly (not
	// store.Open(), which may already resolve to Postgres via env/config).
	dbPath, err := store.DBPath()
	if err != nil {
		return err
	}
	src, err := store.OpenAt(dbPath)
	if err != nil {
		return fmt.Errorf("open local SQLite store: %w", err)
	}
	defer src.Close()

	// Target: connect + ping (distinct errors) and create the baseline schema.
	dst, err := store.OpenPostgres(dsn)
	if err != nil {
		return classifyPostgresErr(err)
	}
	defer dst.Close()

	// FR-010: refuse a target that already holds agent-brain data. Unrelated
	// tables in a shared database are ignored — we only inspect our own.
	if has, table, n, err := dst.HasAgentBrainData(); err != nil {
		return err
	} else if has {
		return fmt.Errorf("target database already contains agent-brain data (%d rows in %q) — use an empty database, or drop and recreate it, then retry", n, table)
	}

	// Migrate existing data, if any. A fresh install (empty source) skips
	// straight to the config flip.
	hasData, _, _, err := src.HasAgentBrainData()
	if err != nil {
		return err
	}
	if hasData {
		counts, err := src.TransferTo(dst)
		if err != nil {
			printCounts(counts)
			fmt.Println("The target may be partially populated — drop and recreate it before retrying.")
			return fmt.Errorf("migration failed, config left unchanged: %w", err)
		}
		fmt.Println("Migrated existing data to PostgreSQL:")
		printCounts(counts)
	}

	// Flip the config last: only a fully verified migration switches the backend.
	if _, err := config.Update(func(c *config.Config) error {
		c.Storage = &config.Storage{Backend: string(store.BackendPostgres), DSN: dsn}
		return nil
	}); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Println()
	fmt.Println("Advanced mode is now active: the local store runs on PostgreSQL.")
	fmt.Printf("Your SQLite data is untouched at %s (kept as a backup).\n", dbPath)
	fmt.Println("To revert: agent-brain storage use sqlite (this does not copy Postgres-era data back).")
	fmt.Println()
	fmt.Println(restartNotice)
	return nil
}

func runUseSQLite() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Storage == nil || cfg.Storage.Backend != string(store.BackendPostgres) {
		fmt.Println("Already on Basic mode (SQLite). Nothing to change.")
		return nil
	}
	if _, err := config.Update(func(c *config.Config) error {
		c.Storage = nil
		return nil
	}); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	fmt.Println("Reverted to Basic mode (SQLite).")
	fmt.Println("Note: data written while on PostgreSQL stays in PostgreSQL — this revert does not copy it back.")
	fmt.Println()
	fmt.Println(restartNotice)
	return nil
}

func newStorageStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the active storage backend, location, schema version, and row counts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			st, err := store.Open()
			if err != nil {
				return fmt.Errorf("open local store: %w", err)
			}
			defer st.Close()

			if st.Backend == store.BackendPostgres {
				fmt.Println("Mode:     Advanced (PostgreSQL)")
				fmt.Printf("Source:   %s\n", backendSource(cfg))
				fmt.Printf("Location: %s\n", st.Path) // already redacted by OpenPostgres
			} else {
				fmt.Println("Mode:     Basic (SQLite)")
				fmt.Println("Source:   default")
				fmt.Printf("Location: %s\n", st.Path)
			}

			if v, err := st.SchemaVersion(); err == nil {
				fmt.Printf("Schema:   version %d\n", v)
			}

			fmt.Println("Rows:")
			for _, t := range store.DataTables() {
				if n, err := st.CountRows(t); err == nil {
					fmt.Printf("  %-24s %d\n", t, n)
				}
			}

			if st.Backend == store.BackendSQLite {
				fmt.Println()
				fmt.Println("Advanced mode stores your data in PostgreSQL instead — agent-brain storage use postgres --dsn <dsn>")
			}
			return nil
		},
	}
}

// backendSource reports what selected the active Postgres backend, for status.
func backendSource(cfg *config.Config) string {
	if os.Getenv("AGENT_BRAIN_PG_DSN") != "" {
		return "AGENT_BRAIN_PG_DSN env var"
	}
	if cfg.Storage != nil && cfg.Storage.Backend == string(store.BackendPostgres) {
		return "config (storage section)"
	}
	return "default"
}

// classifyPostgresErr turns a raw connect/migrate error into an actionable
// message distinguishing the common causes.
func classifyPostgresErr(err error) error {
	s := err.Error()
	switch {
	case strings.Contains(s, "password authentication failed"), strings.Contains(s, "authentication"):
		return fmt.Errorf("could not connect: authentication failed — check the username and password in the DSN (%w)", err)
	case strings.Contains(s, "does not exist"):
		return fmt.Errorf("could not connect: the database does not exist — create it first, e.g. createdb agentbrain (%w)", err)
	case strings.Contains(s, "connection refused"), strings.Contains(s, "no such host"), strings.Contains(s, "dial "):
		return fmt.Errorf("could not connect: PostgreSQL is unreachable at that host/port — check it is running and the DSN is correct (%w)", err)
	default:
		return fmt.Errorf("could not connect to PostgreSQL: %w", err)
	}
}

func printCounts(counts []store.TableCount) {
	for _, c := range counts {
		marker := ""
		if c.SQLite != c.Postgres {
			marker = "  <-- MISMATCH"
		}
		fmt.Printf("  %-24s sqlite=%d postgres=%d%s\n", c.Table, c.SQLite, c.Postgres, marker)
	}
}
