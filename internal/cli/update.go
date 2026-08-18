package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/update"
	"github.com/ebrahim5801/agent-brain-cli/internal/version"
)

func newUpdateCmd() *cobra.Command {
	var (
		checkOnly bool
		force     bool
		quiet     bool
		auto      string
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update agent-brain to the latest release",
		Long: "Fetch and atomically replace this binary with the latest release from the\n" +
			"agent-brain cloud. Use --check to only report, --auto to change automatic-update mode.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Clean up a leftover .old image from a prior Windows replace.
			if exe, err := os.Executable(); err == nil {
				update.CleanupStale(exe)
			}
			if cmd.Flags().Changed("auto") {
				return setAutoMode(cmd, auto)
			}
			return runUpdate(cmd.Context(), checkOnly, force, quiet)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "report whether an update is available and exit")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if already on the latest (or a dev) version")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress all output except errors (used by auto-update)")
	cmd.Flags().StringVar(&auto, "auto", "", "set automatic-update mode: on|notify|off")
	return cmd
}

func setAutoMode(cmd *cobra.Command, mode string) error {
	switch mode {
	case config.UpdateAuto, config.UpdateNotify, config.UpdateOff:
	default:
		return fmt.Errorf("invalid mode %q: use on, notify, or off", mode)
	}
	if _, err := config.Update(func(c *config.Config) error {
		if c.Update == nil {
			c.Update = &config.UpdateConfig{}
		}
		c.Update.Auto = mode
		return nil
	}); err != nil {
		return err
	}
	switch mode {
	case config.UpdateAuto:
		fmt.Fprintln(cmd.OutOrStdout(), "Automatic updates on: agent-brain will self-update in the background from interactive commands.")
	case config.UpdateNotify:
		fmt.Fprintln(cmd.OutOrStdout(), "Automatic updates set to notify: agent-brain will tell you when a new version is out but not install it.")
	case config.UpdateOff:
		fmt.Fprintln(cmd.OutOrStdout(), "Automatic updates off: agent-brain will not check for or install updates.")
	}
	return nil
}

func runUpdate(ctx context.Context, checkOnly, force, quiet bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !checkOnly {
		unlock, ok := update.TryLock()
		if !ok {
			if quiet {
				return nil
			}
			return fmt.Errorf("another update is already running")
		}
		defer unlock()
	}
	current := version.Version

	// Progress and diagnostics go to stderr; the final result line is the only
	// thing on stdout, so scripts can capture it.
	logf := func(format string, a ...any) {
		if !quiet {
			fmt.Fprintf(os.Stderr, format+"\n", a...)
		}
	}

	logf("Checking for updates (current version %s)...", current)
	res, err := update.Check(ctx, &http.Client{Timeout: 15 * time.Second}, cfg.Server(), current)
	if err != nil {
		return fmt.Errorf("check for update: %w", err)
	}

	// Record the latest version we saw regardless of action so notify/auto can
	// use it without another round-trip.
	rememberLatest(res.Latest)

	if checkOnly {
		if res.Newer {
			fmt.Printf("agent-brain %s is available (you have %s); run 'agent-brain update'\n", res.Latest, current)
		} else {
			fmt.Printf("agent-brain is up to date (%s)\n", current)
		}
		return nil
	}

	if !res.Newer && !force {
		if current == "dev" {
			return fmt.Errorf("this is a development build; nothing to update to (use --force to install %s anyway)", res.Latest)
		}
		fmt.Printf("agent-brain is already up to date (%s)\n", current)
		return nil
	}
	if !res.HasAsset {
		return fmt.Errorf("no release asset available for this platform")
	}

	logf("Downloading agent-brain %s...", res.Latest)
	target, err := update.Apply(ctx, &http.Client{Timeout: 5 * time.Minute}, cfg.Server(), res.Asset)
	if err != nil {
		var manual *update.ErrManualInstall
		if errors.As(err, &manual) {
			if quiet {
				// Nobody sees the hint in a background run; drop the extracted
				// binary instead of leaking one per failed attempt.
				_ = os.Remove(manual.TempPath)
				return fmt.Errorf("apply update: %w", err)
			}
			fmt.Fprintln(os.Stderr, manual.Hint)
			os.Exit(1)
		}
		return fmt.Errorf("apply update: %w", err)
	}

	if quiet {
		// An auto-update ran: stash the version so the next interactive command
		// announces it once (the user never asked for output this run).
		markAutoApplied(res.Latest)
	}
	fmt.Printf("Updated agent-brain %s -> %s (%s)\n", current, res.Latest, target)
	return nil
}

// rememberLatest caches the newest version seen and stamps the check time so the
// throttle in maybeCheckForUpdate is honored.
func rememberLatest(latest string) {
	_, _ = config.Update(func(c *config.Config) error {
		if c.Update == nil {
			c.Update = &config.UpdateConfig{}
		}
		c.Update.CachedLatest = latest
		c.Update.LastCheck = time.Now().UTC().Format(time.RFC3339)
		return nil
	})
}

// markAutoApplied records that a background auto-update installed version so the
// next interactive command can announce it once.
func markAutoApplied(v string) {
	_, _ = config.Update(func(c *config.Config) error {
		if c.Update == nil {
			c.Update = &config.UpdateConfig{}
		}
		c.Update.LastAutoApply = v
		return nil
	})
}
