package cli

import (
	"fmt"
	"os"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/daemon"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/syncer"
)

func init() {
	finishLogin = installDaemonService
	finishLogout = removeDaemonService
	printDaemonStatus = daemonStatusLine
}

func installDaemonService() error {
	if os.Getenv("AGENT_BRAIN_NO_DAEMON") != "" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if err := daemon.Install(exe); err != nil {
		fmt.Printf("Background sync service not installed (%v).\n", err)
		return nil
	}
	_, _ = config.Update(func(c *config.Config) error {
		c.Daemon.Installed = true
		return nil
	})
	fmt.Println("Background sync service installed (syncs every 60s once you link a project and grant consent).")
	return nil
}

func removeDaemonService() error {
	if os.Getenv("AGENT_BRAIN_NO_DAEMON") != "" {
		return nil
	}
	if err := daemon.Remove(); err != nil {
		fmt.Printf("Background sync service could not be removed (%v).\n", err)
	}
	_, _ = config.Update(func(c *config.Config) error {
		c.Daemon.Installed = false
		return nil
	})
	return nil
}

// restartDaemon is daemon.Restart; tests replace it so they never restart the
// service of the machine running them.
var restartDaemon = daemon.Restart

// restartDaemonService restarts a running background service so it uses the
// binary or schema now on disk. Best-effort: a failure is reported, not fatal.
func restartDaemonService(quiet bool) {
	if os.Getenv("AGENT_BRAIN_NO_DAEMON") != "" {
		return
	}
	restarted, err := restartDaemon()
	if quiet || !restarted {
		return
	}
	if err != nil {
		fmt.Printf("The background sync service could not be restarted (%v); restart it or log out and back in.\n", err)
		return
	}
	fmt.Println("Restarted the background sync service.")
}

func daemonStatusLine(cfg *config.Config, st *store.Store) {
	if !cfg.SignedIn() {
		return
	}
	queued := syncer.UnsyncedCount(st, cfg)
	if running, known := daemon.Running(); known {
		state := "stopped"
		if running {
			state = "running"
		}
		fmt.Printf("  Daemon:         %s (%d sessions queued)\n", state, queued)
	} else if cfg.Daemon.Installed {
		fmt.Printf("  Daemon:         installed (%d sessions queued)\n", queued)
	} else {
		fmt.Printf("  Daemon:         not installed (%d sessions queued) — run `agent-brain sync` manually\n", queued)
	}
}
