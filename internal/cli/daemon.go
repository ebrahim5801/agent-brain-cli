package cli

import (
	"errors"
	"time"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/diag"
	"github.com/ebrahim5801/agent-brain-cli/internal/entitlement"
	"github.com/ebrahim5801/agent-brain-cli/internal/memsync"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/syncer"
	"github.com/ebrahim5801/agent-brain-cli/internal/version"
)

const (
	daemonInterval   = 60 * time.Second
	daemonMaxBackoff = 15 * time.Minute
)

// newDaemonCmd is the background service entry point: a 60s loop of sync +
// heartbeat with jittered exponential backoff on failure. Same silence
// contract as the hook path: no stdout, diagnostics only.
func newDaemonCmd() *cobra.Command {
	var once bool
	cmd := &cobra.Command{
		Use:    "daemon",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runDaemon(once)
			return nil
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "run a single sync+heartbeat cycle and exit (used by tests)")
	return cmd
}

func runDaemon(once bool) {
	logger := fallbackLogger()
	backoff := time.Second
	for {
		ok := daemonCycle(logger)
		if once {
			return
		}
		if ok {
			backoff = time.Second
			time.Sleep(daemonInterval)
			continue
		}
		time.Sleep(jitter(backoff))
		backoff *= 2
		if backoff > daemonMaxBackoff {
			backoff = daemonMaxBackoff
		}
	}
}

// daemonCycle runs one sync+heartbeat round; false means back off.
func daemonCycle(logger *diag.Logger) bool {
	defer func() { _ = recover() }()

	cfg, err := config.Load()
	if err != nil {
		logger.Log("daemon", "load config: %v", err)
		return false
	}
	if !cfg.SignedIn() {
		// Nothing to do; not an error. Stay quiet and check again next tick.
		return true
	}

	// Entitlement refresh rides the daemon loop and needs only a signed-in
	// account — memory consent is independent of telemetry consent. Failures
	// never back off the loop; the cached grace window absorbs them.
	if _, err := entitlement.Refresh(nil, 10*time.Second); err != nil {
		logger.Log("daemon", "entitlement refresh: %v", err)
	}

	st, err := store.Open()
	if err != nil {
		logger.Log("daemon", "open store: %v", err)
		return false
	}
	defer st.Close()
	dbLogger := storeLogger(st)

	// Memory sync rides the same loop but is a strictly separate pipeline: it
	// needs only a signed-in account (its own per-project consent gates
	// contribution) and runs even when telemetry consent is absent
	// (Constitution II). Best-effort — never backs off the loop.
	if _, err := memsync.New().Run(st); err != nil && !errors.Is(err, memsync.ErrReauth) && !errors.Is(err, memsync.ErrNotSignedIn) {
		dbLogger.Log("daemon", "memory sync: %v", err)
	}

	if !cfg.TelemetryConsented() {
		return true
	}

	engine := syncer.New()
	if _, err := engine.Run(st); err != nil {
		switch {
		case errors.Is(err, syncer.ErrReauth):
			dbLogger.Log("daemon", "sync paused: re-authentication required")
			return true // reauth is not a transient failure; do not hot-loop
		case errors.Is(err, syncer.ErrNoConsent), errors.Is(err, syncer.ErrNotSignedIn):
			return true
		default:
			dbLogger.Log("daemon", "sync: %v", err)
			return false
		}
	}

	requested, err := engine.Heartbeat(st, cfg, version.Version)
	if err != nil {
		if !errors.Is(err, syncer.ErrReauth) {
			dbLogger.Log("daemon", "heartbeat: %v", err)
		}
		return false
	}
	if requested {
		if _, err := engine.Run(st); err != nil {
			dbLogger.Log("daemon", "requested sync: %v", err)
			return false
		}
	}
	return true
}

func storeLogger(st *store.Store) *diag.Logger {
	logPath, _ := store.DiagLogPath()
	return &diag.Logger{DB: st.DB, Rebind: st.Rebind, FallbackPath: logPath}
}

func jitter(d time.Duration) time.Duration {
	return d + time.Duration(time.Now().UnixNano()%int64(d/4+1))
}
