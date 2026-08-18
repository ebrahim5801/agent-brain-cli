package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/update"
	"github.com/ebrahim5801/agent-brain-cli/internal/version"
)

const updateCheckInterval = 24 * time.Hour

// updatePostRun is the cobra PostRun hook wired onto human-facing commands. It
// runs only after RunE succeeds (never on error, never on hook/mcp/daemon).
func updatePostRun(*cobra.Command, []string) {
	maybeCheckForUpdate()
}

// maybeCheckForUpdate runs a throttled, best-effort update check at the END of
// human-facing commands (stats, status, sync, link, login). It must NEVER be
// called on hook, mcp, or daemon paths: hook stdout is machine-parsed JSON and
// those paths are latency-sensitive. Every failure is swallowed — an unreachable
// or slow server must never break or delay the command that just finished.
func maybeCheckForUpdate() {
	defer func() { _ = recover() }()

	cfg, err := config.Load()
	if err != nil {
		return
	}
	up := cfg.Update
	if up == nil {
		up = &config.UpdateConfig{}
	}

	// Announce a completed background auto-update once, now that we are running
	// the new binary.
	announceAutoApply(up)

	mode := up.Mode()
	if mode == config.UpdateOff {
		return
	}
	if !throttleElapsed(up.LastCheck) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := update.Check(ctx, &http.Client{Timeout: 2 * time.Second}, cfg.Server(), version.Version)
	if err != nil {
		return
	}
	rememberLatest(res.Latest)
	if !res.Newer {
		return
	}

	switch mode {
	case config.UpdateNotify:
		fmt.Fprintf(os.Stderr, "agent-brain %s is available (you have %s); run 'agent-brain update'\n", res.Latest, version.Version)
	case config.UpdateAuto:
		// A root-owned install (e.g. /usr/local/bin) cannot be replaced in the
		// background; degrade to a visible notice instead of failing silently
		// every day.
		if update.CanReplaceTarget() {
			spawnBackgroundUpdate()
		} else {
			fmt.Fprintf(os.Stderr, "agent-brain %s is available (you have %s); run 'agent-brain update' (this install needs sudo)\n", res.Latest, version.Version)
		}
	}
}

// announceAutoApply prints the one-line notice for a background auto-update that
// has taken effect (the running binary now matches the applied version) and
// clears the marker so it prints exactly once.
func announceAutoApply(up *config.UpdateConfig) {
	if up.LastAutoApply == "" || up.LastAutoApply != version.Version {
		return
	}
	fmt.Fprintf(os.Stderr, "updated to %s; disable auto-update with 'agent-brain update --auto off'\n", version.Version)
	_, _ = config.Update(func(c *config.Config) error {
		if c.Update != nil {
			c.Update.LastAutoApply = ""
		}
		return nil
	})
}

// throttleElapsed reports whether at least updateCheckInterval has passed since
// last (RFC3339). An unset or unparseable timestamp means "check now".
func throttleElapsed(last string) bool {
	if last == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	return time.Since(t) >= updateCheckInterval
}

// spawnBackgroundUpdate launches a detached `agent-brain update --quiet` on the
// current executable and returns immediately; the running command finishes on
// the old binary and the NEXT invocation is the new version.
func spawnBackgroundUpdate() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "update", "--quiet")
	// Detach stdio so the background update never writes to the user's terminal.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	_ = cmd.Start()
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
}
