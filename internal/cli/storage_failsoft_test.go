package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// TestHookFailsSoftWhenPostgresUnreachable covers FR-008: with the config
// pointing the store at a closed Postgres port, a session-start hook must not
// break the assistant session — it exits 0 (runHook never returns an error),
// emits no injection (empty or valid-JSON stdout, never a crash), and records
// the failure in the diagnostics file (which works without a live DB).
func TestHookFailsSoftWhenPostgresUnreachable(t *testing.T) {
	root := sandbox(t)
	proj := t.TempDir()

	// Port 1 is never a Postgres — connection is refused immediately.
	if _, err := config.Update(func(c *config.Config) error {
		c.Storage = &config.Storage{Backend: string(store.BackendPostgres), DSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	env := hookWith(t, "session-start", "claude-code", `{"session_id":"s1","cwd":"`+proj+`"}`)

	// Exit 0 is guaranteed by runHook returning a string, not an error. Output
	// must be empty (no injection) or, if present, syntactically valid JSON.
	if env != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(env), &m); err != nil {
			t.Fatalf("hook stdout is neither empty nor valid JSON: %q", env)
		}
	}

	// The failure was logged to the diagnostics file (no DB required).
	logPath := filepath.Join(root, "data", "diagnostics.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("diagnostics log not written: %v", err)
	}
	if !strings.Contains(string(data), "open store") {
		t.Errorf("diagnostics log missing the open-store failure:\n%s", data)
	}
}
