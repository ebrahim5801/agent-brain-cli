package cli

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant/claude"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/entitlement"
)

// Personal memory is local and free: `memory enable --yes` on a machine that
// never signed in records consent, registers the MCP server, and turns the
// memory gate on. `memory disable --all` turns it off again.
func TestEnableMemoryWithoutAccount(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("AGENT_BRAIN_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SignedIn() {
		t.Fatal("test config is signed in")
	}
	if err := enableMemory(true); err != nil {
		t.Fatalf("enable without an account: %v", err)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MemoryConsented() {
		t.Error("consent not recorded")
	}
	if !entitlement.MemoryActive(cfg, false, time.Now()) {
		t.Error("memory gate inactive after enable")
	}
	if registered, _, err := claude.MCPRegistered(); err != nil || !registered {
		t.Errorf("MCP server not registered: registered=%v err=%v", registered, err)
	}

	if err := revokeMemoryConsent(); err != nil {
		t.Fatalf("disable --all: %v", err)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if entitlement.MemoryActive(cfg, false, time.Now()) {
		t.Error("memory gate still active after disable --all")
	}
}
