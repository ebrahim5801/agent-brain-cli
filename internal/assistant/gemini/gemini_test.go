package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

func settingsPathT(t *testing.T) string {
	t.Helper()
	path, err := SettingsPath()
	if err != nil {
		t.Fatalf("SettingsPath: %v", err)
	}
	return path
}

func readRoot(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return root
}

func TestFreshInstall(t *testing.T) {
	t.Setenv("AGENT_BRAIN_GEMINI_HOME", t.TempDir())

	backups, err := Install("/usr/local/bin/agent-brain")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("expected no backups on fresh install, got %v", backups)
	}

	root := readRoot(t, settingsPathT(t))
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks missing or wrong type: %#v", root["hooks"])
	}
	for _, he := range hookEvents {
		if _, ok := hooks[he.Event]; !ok {
			t.Errorf("missing hook event %s", he.Event)
		}
	}
	if len(hooks) != 6 {
		t.Errorf("expected 6 hook events, got %d", len(hooks))
	}

	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing or wrong type: %#v", root["mcpServers"])
	}
	entry, ok := servers[MCPServerName].(map[string]any)
	if !ok {
		t.Fatalf("missing mcp server entry")
	}
	if entry["command"] != "/usr/local/bin/agent-brain" {
		t.Errorf("unexpected command: %v", entry["command"])
	}

	events, err := InstalledEvents()
	if err != nil {
		t.Fatalf("InstalledEvents: %v", err)
	}
	if len(events) != 6 {
		t.Errorf("expected 6 installed events, got %d: %v", len(events), events)
	}
}

func TestInstallMergesWithForeignConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_BRAIN_GEMINI_HOME", dir)

	seed := `{
		"theme": "dark",
		"mcpServers": {"user": {"command": "x"}},
		"hooks": {"SessionStart": [{"matcher": "*", "hooks": [{"name": "user-hook", "command": "whatever"}]}]}
	}`
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	backups, err := Install("/bin/agent-brain")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected exactly one backup, got %v", backups)
	}
	if _, err := os.Stat(backups[0]); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}

	root := readRoot(t, path)
	if root["theme"] != "dark" {
		t.Errorf("theme not preserved: %v", root["theme"])
	}
	servers := root["mcpServers"].(map[string]any)
	if _, ok := servers["user"]; !ok {
		t.Errorf("foreign mcp server dropped")
	}
	if _, ok := servers[MCPServerName]; !ok {
		t.Errorf("our mcp server missing")
	}

	hooks := root["hooks"].(map[string]any)
	sessionStart := hooks["SessionStart"].([]any)
	var foundUserHook, foundOurs bool
	for _, g := range sessionStart {
		group := g.(map[string]any)
		inner := group["hooks"].([]any)
		for _, h := range inner {
			hook := h.(map[string]any)
			if hook["name"] == "user-hook" {
				foundUserHook = true
			}
			if hook["name"] == "agent-brain-session-start" {
				foundOurs = true
			}
		}
	}
	if !foundUserHook {
		t.Errorf("foreign user-hook dropped")
	}
	if !foundOurs {
		t.Errorf("our session-start hook missing")
	}
}

func TestInstallIdempotent(t *testing.T) {
	t.Setenv("AGENT_BRAIN_GEMINI_HOME", t.TempDir())

	if _, err := Install("/bin/agent-brain"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	path := settingsPathT(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	backups, err := Install("/bin/agent-brain")
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("expected zero backups on reinstall, got %v", backups)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("file changed on idempotent reinstall")
	}
}

func TestUninstall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_BRAIN_GEMINI_HOME", dir)

	seed := `{
		"theme": "dark",
		"mcpServers": {"user": {"command": "x"}},
		"hooks": {"SessionStart": [{"matcher": "*", "hooks": [{"name": "user-hook", "command": "whatever"}]}]}
	}`
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if _, err := Install("/bin/agent-brain"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	root := readRoot(t, path)
	if root["theme"] != "dark" {
		t.Errorf("theme not preserved: %v", root["theme"])
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers dropped entirely")
	}
	if _, ok := servers["user"]; !ok {
		t.Errorf("foreign mcp server dropped")
	}
	if _, ok := servers[MCPServerName]; ok {
		t.Errorf("our mcp server survived uninstall")
	}

	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks dropped entirely")
	}
	sessionStart := hooks["SessionStart"].([]any)
	var foundUserHook, foundOurs bool
	for _, g := range sessionStart {
		group := g.(map[string]any)
		inner := group["hooks"].([]any)
		for _, h := range inner {
			hook := h.(map[string]any)
			if hook["name"] == "user-hook" {
				foundUserHook = true
			}
			if hook["name"] == "agent-brain-session-start" {
				foundOurs = true
			}
		}
	}
	if !foundUserHook {
		t.Errorf("foreign user-hook dropped by uninstall")
	}
	if foundOurs {
		t.Errorf("our session-start hook survived uninstall")
	}
}

func TestUninstallNoOpWhenAbsent(t *testing.T) {
	t.Setenv("AGENT_BRAIN_GEMINI_HOME", t.TempDir())
	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall on absent file: %v", err)
	}
}

func TestInstallUnparseableAborts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_BRAIN_GEMINI_HOME", dir)

	path := filepath.Join(dir, "settings.json")
	bad := "{not json"
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if _, err := Install("/bin/agent-brain"); err == nil {
		t.Fatalf("expected error installing over unparseable settings.json")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != bad {
		t.Errorf("unparseable file was modified: got %q", after)
	}
}

func TestStateMatrix(t *testing.T) {
	t.Run("not integrated", func(t *testing.T) {
		t.Setenv("AGENT_BRAIN_GEMINI_HOME", t.TempDir())
		a := Adapter{}
		s, err := a.State()
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if s.Integrated() {
			t.Errorf("expected not integrated")
		}
		if s.Tier != assistant.TierNotIntegrated {
			t.Errorf("expected TierNotIntegrated, got %v", s.Tier)
		}
		if s.HookEventsWanted != 6 {
			t.Errorf("expected HookEventsWanted 6, got %d", s.HookEventsWanted)
		}
	})

	t.Run("full", func(t *testing.T) {
		t.Setenv("AGENT_BRAIN_GEMINI_HOME", t.TempDir())
		if _, err := Install("/bin/agent-brain"); err != nil {
			t.Fatalf("Install: %v", err)
		}
		a := Adapter{}
		s, err := a.State()
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if s.Tier != assistant.TierFull {
			t.Errorf("expected TierFull, got %v", s.Tier)
		}
		if s.HookEvents != 6 || !s.MCPRegistered {
			t.Errorf("expected 6 hook events + mcp, got %d events, mcp=%v", s.HookEvents, s.MCPRegistered)
		}
		if len(s.Notes) != 0 {
			t.Errorf("expected no notes for gemini, got %v", s.Notes)
		}
	})

	t.Run("partial", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("AGENT_BRAIN_GEMINI_HOME", dir)
		if _, err := Install("/bin/agent-brain"); err != nil {
			t.Fatalf("Install: %v", err)
		}
		path := filepath.Join(dir, "settings.json")
		root := readRoot(t, path)
		hooks := root["hooks"].(map[string]any)
		delete(hooks, "AfterAgent")
		delete(hooks, "SessionEnd")
		data, err := json.MarshalIndent(root, "", "  ")
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		a := Adapter{}
		s, err := a.State()
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if s.HookEvents != 4 {
			t.Errorf("expected 4 hook events, got %d", s.HookEvents)
		}
		if s.Tier != assistant.TierPartial {
			t.Errorf("expected TierPartial, got %v", s.Tier)
		}
	})
}

func TestDetected(t *testing.T) {
	t.Run("dir absent", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist")
		t.Setenv("AGENT_BRAIN_GEMINI_HOME", dir)
		if Detected() {
			t.Errorf("expected not detected for absent seam dir")
		}
	})

	t.Run("dir present", func(t *testing.T) {
		t.Setenv("AGENT_BRAIN_GEMINI_HOME", t.TempDir())
		if !Detected() {
			t.Errorf("expected detected for present seam dir")
		}
	})
}
