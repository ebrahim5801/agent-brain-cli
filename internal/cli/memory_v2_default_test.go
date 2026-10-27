package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// isolateMemoryHome points agent-brain and Claude Code at a scratch home and
// replaces the service restart, so a test never restarts this machine's daemon.
func isolateMemoryHome(t *testing.T) (dataDir string, restarts *int) {
	t.Helper()
	root := t.TempDir()
	dataDir = filepath.Join(root, "data")
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("AGENT_BRAIN_DATA_DIR", dataDir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	count := 0
	previous := restartDaemon
	restartDaemon = func() (bool, error) { count++; return true, nil }
	t.Cleanup(func() { restartDaemon = previous })
	return dataDir, &count
}

func createV1Store(t *testing.T) {
	t.Helper()
	t.Setenv("AGENT_BRAIN_MEMORY_V2_DEFAULT", "off")
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	if st.MemoryV2 {
		t.Fatal("setup: store is already V2")
	}
	st.Close()
	t.Setenv("AGENT_BRAIN_MEMORY_V2_DEFAULT", "")
}

func storeIsV2(t *testing.T) bool {
	t.Helper()
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	return st.MemoryV2
}

func backups(t *testing.T, dataDir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dataDir, "*.pre-v2-*.bak"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// `memory enable` on a machine whose store predates Memory V2 backs it up and
// upgrades it, so session capture and summaries work at once.
func TestEnableMemoryUpgradesOlderStore(t *testing.T) {
	dataDir, restarts := isolateMemoryHome(t)
	createV1Store(t)

	out := capture(t, func() {
		if err := enableMemory(true); err != nil {
			t.Fatalf("enable: %v", err)
		}
	})
	if !storeIsV2(t) {
		t.Fatal("store still pre-V2 after enable")
	}
	if got := backups(t, dataDir); len(got) != 1 {
		t.Fatalf("backups = %v, want one", got)
	}
	if !strings.Contains(out, "Memory V2 is active") || !strings.Contains(out, "Restarted the background sync service") {
		t.Errorf("enable output does not report the upgrade and restart:\n%s", out)
	}
	if *restarts != 1 {
		t.Errorf("daemon restarts = %d, want 1", *restarts)
	}

	// Enabling again changes nothing and makes no second copy.
	capture(t, func() {
		if err := enableMemory(true); err != nil {
			t.Fatalf("second enable: %v", err)
		}
	})
	if got := backups(t, dataDir); len(got) != 1 || *restarts != 1 {
		t.Errorf("second enable: backups %v, restarts %d; want unchanged", got, *restarts)
	}
}

func TestUpgradeV2ApplyBacksUpALocalStore(t *testing.T) {
	dataDir, restarts := isolateMemoryHome(t)
	createV1Store(t)

	out, err := runMemoryCmd(t, newMemoryUpgradeV2Cmd, "--apply")
	if err != nil {
		t.Fatalf("upgrade-v2 --apply: %v", err)
	}
	if !storeIsV2(t) || len(backups(t, dataDir)) != 1 || *restarts != 1 {
		t.Fatalf("after --apply: v2=%v backups=%v restarts=%d", storeIsV2(t), backups(t, dataDir), *restarts)
	}
	if !strings.Contains(out, "A copy of the store from before the upgrade is at") {
		t.Errorf("--apply output does not name the backup:\n%s", out)
	}

	out, err = runMemoryCmd(t, newMemoryUpgradeV2Cmd, "--apply")
	if err != nil || !strings.Contains(out, "already active") || len(backups(t, dataDir)) != 1 {
		t.Errorf("second --apply = %q, %v; want a no-op", out, err)
	}
}

// A fresh machine gets a V2 store without any upgrade step or backup.
func TestEnableMemoryOnFreshMachineNeedsNoUpgrade(t *testing.T) {
	dataDir, restarts := isolateMemoryHome(t)
	t.Setenv("AGENT_BRAIN_MEMORY_V2_DEFAULT", "")

	capture(t, func() {
		if err := enableMemory(true); err != nil {
			t.Fatalf("enable: %v", err)
		}
	})
	if !storeIsV2(t) || len(backups(t, dataDir)) != 0 || *restarts != 0 {
		t.Fatalf("fresh enable: v2=%v backups=%v restarts=%d; want V2, no backup, no restart", storeIsV2(t), backups(t, dataDir), *restarts)
	}
}
