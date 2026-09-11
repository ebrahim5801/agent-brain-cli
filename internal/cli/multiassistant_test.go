package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

// binPath contains "agent-brain" so the adapters' isOurs command match
// recognizes the installed hook entries (the real binary is named agent-brain).
const testBinPath = "/opt/agent-brain/agent-brain"

// sandbox points every config/data location at temp dirs and creates each
// assistant's home dir so all four register as detected. Returns the root.
func sandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	set := func(k, v string) {
		if err := os.MkdirAll(v, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(k, v)
	}
	set("AGENT_BRAIN_DATA_DIR", filepath.Join(root, "data"))
	set("AGENT_BRAIN_CONFIG_DIR", filepath.Join(root, "cfg"))
	set("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	set("COPILOT_HOME", filepath.Join(root, "copilot"))
	set("AGENT_BRAIN_CURSOR_HOME", filepath.Join(root, "cursor"))
	set("AGENT_BRAIN_GEMINI_HOME", filepath.Join(root, "gemini"))
	set("AGENT_BRAIN_OPENCODE_HOME", filepath.Join(root, "opencode"))
	set("CODEX_HOME", filepath.Join(root, "codex"))
	return root
}

// installAll runs every registered adapter's Install with the test binary path.
func installAll(t *testing.T) {
	t.Helper()
	for _, a := range assistant.Registry() {
		if _, err := a.Install(testBinPath); err != nil {
			t.Fatalf("%s install: %v", a.Name(), err)
		}
	}
}

func TestInstallIntegratesEveryDetectedAssistantToFullTier(t *testing.T) {
	sandbox(t)
	installAll(t)

	for _, a := range assistant.Registry() {
		st, err := a.State()
		if err != nil {
			t.Fatalf("%s state: %v", a.Name(), err)
		}
		if st.Tier != assistant.TierFull {
			t.Errorf("%s tier = %q, want full (events %d/%d, mcp %v)",
				a.Name(), st.Tier, st.HookEvents, st.HookEventsWanted, st.MCPRegistered)
		}
	}
}

// fileBytes snapshots every settings file the adapters may write, so a second
// install can be proven a no-op byte-for-byte.
func settingsSnapshot(t *testing.T, root string) map[string][]byte {
	t.Helper()
	snap := map[string][]byte{}
	_ = filepath.Walk(filepath.Join(root), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		// Only assistant config trees, not the data dir / config dir.
		for _, sub := range []string{"claude", "copilot", "cursor", "gemini", "opencode", "codex"} {
			if bytes.Contains([]byte(path), []byte(string(os.PathSeparator)+sub+string(os.PathSeparator))) {
				b, _ := os.ReadFile(path)
				snap[path] = b
			}
		}
		return nil
	})
	return snap
}

func TestInstallIsIdempotentAcrossAllAssistants(t *testing.T) {
	root := sandbox(t)
	installAll(t)
	before := settingsSnapshot(t, root)

	for _, a := range assistant.Registry() {
		backups, err := a.Install(testBinPath)
		if err != nil {
			t.Fatalf("%s re-install: %v", a.Name(), err)
		}
		if len(backups) != 0 {
			t.Errorf("%s re-install created backups: %v", a.Name(), backups)
		}
	}

	after := settingsSnapshot(t, root)
	if len(before) == 0 {
		t.Fatal("no settings files were written")
	}
	for path, b := range before {
		if !bytes.Equal(b, after[path]) {
			t.Errorf("re-install changed %s", path)
		}
	}
	// No stray backup files anywhere.
	for path := range after {
		if bytes.Contains([]byte(path), []byte(".agent-brain-backup-")) {
			t.Errorf("idempotent install left a backup: %s", path)
		}
	}
}

func TestUninstallIsolationLeavesOtherAssistantsIntact(t *testing.T) {
	sandbox(t)
	installAll(t)

	cursor, _ := assistant.ByName("cursor")
	if err := cursor.Uninstall(); err != nil {
		t.Fatalf("cursor uninstall: %v", err)
	}

	st, _ := cursor.State()
	if st.Integrated() {
		t.Errorf("cursor still integrated after uninstall: %+v", st)
	}
	for _, name := range []string{"claude-code", "copilot-cli", "gemini-cli", "opencode", "codex"} {
		a, _ := assistant.ByName(name)
		st, _ := a.State()
		if st.Tier != assistant.TierFull {
			t.Errorf("%s no longer full after cursor uninstall: %q", name, st.Tier)
		}
	}
}

func TestInstallTargetsSelectsExplicitAssistants(t *testing.T) {
	sandbox(t)
	targets, detected, failed := installTargets([]string{"cursor", "gemini-cli"})
	if failed {
		t.Fatal("unexpected failure selecting detected assistants")
	}
	if !detected {
		t.Error("expected detectedAny for detected assistants")
	}
	got := map[string]bool{}
	for _, a := range targets {
		got[a.Name()] = true
	}
	if len(got) != 2 || !got["cursor"] || !got["gemini-cli"] {
		t.Errorf("targets = %v, want cursor+gemini-cli", got)
	}
}

func TestInstallTargetsNamedButUndetectedFails(t *testing.T) {
	root := sandbox(t)
	// Remove cursor's home so it is not detected.
	if err := os.RemoveAll(filepath.Join(root, "cursor")); err != nil {
		t.Fatal(err)
	}
	targets, _, failed := installTargets([]string{"cursor"})
	if !failed {
		t.Error("expected failure for a named-but-undetected assistant")
	}
	if len(targets) != 0 {
		t.Errorf("undetected assistant should not be a target: %v", targets)
	}
}

func TestInstallUnknownAssistantNameErrors(t *testing.T) {
	sandbox(t)
	cmd := newInstallCmd()
	cmd.SetArgs([]string{"--assistant", "bogus"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an unknown assistant name")
	}
}

func TestStatusReportsPerAssistantTiers(t *testing.T) {
	root := sandbox(t)
	installAll(t)
	// Break cursor to a partial install: remove two of its hook entries by
	// uninstalling then re-installing only a subset is awkward; instead delete
	// the mcp file so it drops below full.
	if err := os.Remove(filepath.Join(root, "cursor", "mcp.json")); err != nil {
		t.Fatal(err)
	}

	broken := printAssistantStatus()
	if !broken {
		t.Error("expected broken exit: cursor is missing its MCP registration")
	}
}
