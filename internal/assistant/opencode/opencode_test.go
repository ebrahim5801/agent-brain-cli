package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

func setHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AGENT_BRAIN_OPENCODE_HOME", dir)
	return dir
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

func TestDetected(t *testing.T) {
	t.Run("dir absent", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist")
		t.Setenv("AGENT_BRAIN_OPENCODE_HOME", dir)
		if Detected() {
			t.Errorf("expected not detected for absent seam dir")
		}
	})

	t.Run("dir present", func(t *testing.T) {
		setHome(t)
		if !Detected() {
			t.Errorf("expected detected for present seam dir")
		}
	})
}

func TestFreshInstall(t *testing.T) {
	dir := setHome(t)

	backups, err := Install("/usr/local/bin/agent-brain")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("expected no backups on fresh install, got %v", backups)
	}

	pluginData, err := os.ReadFile(filepath.Join(dir, pluginFileName))
	if err != nil {
		t.Fatalf("read plugin shim: %v", err)
	}
	if !strings.Contains(string(pluginData), pluginMarker) {
		t.Errorf("plugin shim missing marker %q", pluginMarker)
	}
	if !strings.Contains(string(pluginData), "/usr/local/bin/agent-brain") {
		t.Errorf("plugin shim missing embedded binPath")
	}

	root := readRoot(t, filepath.Join(dir, configFileName))
	plugins, ok := root["plugin"].([]any)
	if !ok || len(plugins) != 1 || plugins[0] != PluginRelPath {
		t.Errorf("plugin array = %#v, want [%q]", root["plugin"], PluginRelPath)
	}
	mcp, ok := root["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp missing or wrong type: %#v", root["mcp"])
	}
	entry, ok := mcp[MCPServerName].(map[string]any)
	if !ok {
		t.Fatalf("missing mcp server entry")
	}
	if entry["type"] != "local" {
		t.Errorf("unexpected type: %v", entry["type"])
	}
	cmd, ok := entry["command"].([]any)
	if !ok || len(cmd) != 2 || cmd[0] != "/usr/local/bin/agent-brain" || cmd[1] != "mcp" {
		t.Errorf("unexpected command: %#v", entry["command"])
	}

	n, err := InstalledEventCount()
	if err != nil {
		t.Fatalf("InstalledEventCount: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 installed events, got %d", n)
	}
}

func TestInstallMergesWithForeignConfig(t *testing.T) {
	dir := setHome(t)

	seed := `{
		"$schema": "https://opencode.ai/config.json",
		"plugin": ["some-other-plugin"],
		"mcp": {"user-server": {"type": "local", "command": ["x"]}}
	}`
	path := filepath.Join(dir, configFileName)
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
	if root["$schema"] != "https://opencode.ai/config.json" {
		t.Errorf("$schema not preserved: %v", root["$schema"])
	}
	plugins := root["plugin"].([]any)
	var foundForeign, foundOurs bool
	for _, p := range plugins {
		if p == "some-other-plugin" {
			foundForeign = true
		}
		if p == PluginRelPath {
			foundOurs = true
		}
	}
	if !foundForeign {
		t.Errorf("foreign plugin entry dropped")
	}
	if !foundOurs {
		t.Errorf("our plugin entry missing")
	}

	mcp := root["mcp"].(map[string]any)
	if _, ok := mcp["user-server"]; !ok {
		t.Errorf("foreign mcp server dropped")
	}
	if _, ok := mcp[MCPServerName]; !ok {
		t.Errorf("our mcp server missing")
	}
}

func TestInstallIdempotent(t *testing.T) {
	dir := setHome(t)

	if _, err := Install("/bin/agent-brain"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	cfgPath := filepath.Join(dir, configFileName)
	pluginPathAbs := filepath.Join(dir, pluginFileName)

	cfgBefore, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config before: %v", err)
	}
	pluginBefore, err := os.ReadFile(pluginPathAbs)
	if err != nil {
		t.Fatalf("read plugin before: %v", err)
	}

	backups, err := Install("/bin/agent-brain")
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("expected zero backups on reinstall, got %v", backups)
	}

	cfgAfter, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config after: %v", err)
	}
	if string(cfgBefore) != string(cfgAfter) {
		t.Errorf("config file changed on idempotent reinstall")
	}
	pluginAfter, err := os.ReadFile(pluginPathAbs)
	if err != nil {
		t.Fatalf("read plugin after: %v", err)
	}
	if string(pluginBefore) != string(pluginAfter) {
		t.Errorf("plugin shim changed on idempotent reinstall")
	}
}

func TestUninstall(t *testing.T) {
	dir := setHome(t)

	seed := `{
		"plugin": ["some-other-plugin"],
		"mcp": {"user-server": {"type": "local", "command": ["x"]}}
	}`
	path := filepath.Join(dir, configFileName)
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if _, err := Install("/bin/agent-brain"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, pluginFileName)); !os.IsNotExist(err) {
		t.Errorf("plugin shim survived uninstall: err=%v", err)
	}

	root := readRoot(t, path)
	plugins, ok := root["plugin"].([]any)
	if !ok {
		t.Fatalf("plugin array dropped entirely")
	}
	var foundForeign, foundOurs bool
	for _, p := range plugins {
		if p == "some-other-plugin" {
			foundForeign = true
		}
		if p == PluginRelPath {
			foundOurs = true
		}
	}
	if !foundForeign {
		t.Errorf("foreign plugin entry dropped by uninstall")
	}
	if foundOurs {
		t.Errorf("our plugin entry survived uninstall")
	}

	mcp, ok := root["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp dropped entirely")
	}
	if _, ok := mcp["user-server"]; !ok {
		t.Errorf("foreign mcp server dropped")
	}
	if _, ok := mcp[MCPServerName]; ok {
		t.Errorf("our mcp server survived uninstall")
	}
}

func TestUninstallNoOpWhenAbsent(t *testing.T) {
	setHome(t)
	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall on absent install: %v", err)
	}
}

func TestInstallUnparseableConfigAborts(t *testing.T) {
	dir := setHome(t)

	path := filepath.Join(dir, configFileName)
	bad := "{not json"
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if _, err := Install("/bin/agent-brain"); err == nil {
		t.Fatalf("expected error installing over unparseable opencode.jsonc")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != bad {
		t.Errorf("unparseable config file was modified: got %q", after)
	}

	if _, err := os.Stat(filepath.Join(dir, pluginFileName)); !os.IsNotExist(err) {
		t.Errorf("plugin shim was written despite aborted install: err=%v", err)
	}
}

func TestBuildPluginJSDedupesUsage(t *testing.T) {
	js := buildPluginJS("/bin/agent-brain")
	if !strings.Contains(js, "reportedUsage") || !strings.Contains(js, "reportedUsage.has(info.id)") {
		t.Errorf("shim missing per-message usage dedupe guard; OpenCode emits repeated finished message.updated events and AddUsage accumulates, so usage would be multiplied")
	}
}

func TestStateMatrix(t *testing.T) {
	t.Run("not integrated", func(t *testing.T) {
		setHome(t)
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
		if s.HookEventsWanted != 3 {
			t.Errorf("expected HookEventsWanted 3, got %d", s.HookEventsWanted)
		}
	})

	t.Run("full", func(t *testing.T) {
		setHome(t)
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
		if s.HookEvents != 3 || !s.MCPRegistered {
			t.Errorf("expected 3 hook events + mcp, got %d events, mcp=%v", s.HookEvents, s.MCPRegistered)
		}
		if len(s.Notes) == 0 {
			t.Errorf("expected an honest capability-gap note (no injection/distillation), got none")
		}
	})

	t.Run("mcp only", func(t *testing.T) {
		dir := setHome(t)
		if _, err := Install("/bin/agent-brain"); err != nil {
			t.Fatalf("Install: %v", err)
		}
		// Remove the plugin shim but leave the config's mcp entry and plugin
		// reference in place: the shim is gone, so no events can honestly
		// fire, but MCP memory serving still works.
		if err := os.Remove(filepath.Join(dir, pluginFileName)); err != nil {
			t.Fatalf("remove plugin shim: %v", err)
		}

		a := Adapter{}
		s, err := a.State()
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if s.HookEvents != 0 {
			t.Errorf("expected 0 hook events with shim removed, got %d", s.HookEvents)
		}
		if !s.MCPRegistered {
			t.Errorf("expected mcp still registered")
		}
		if s.Tier != assistant.TierMCPOnly {
			t.Errorf("expected TierMCPOnly, got %v", s.Tier)
		}
	})

	t.Run("partial: hooks complete, mcp missing", func(t *testing.T) {
		dir := setHome(t)
		if _, err := Install("/bin/agent-brain"); err != nil {
			t.Fatalf("Install: %v", err)
		}
		path := filepath.Join(dir, configFileName)
		root := readRoot(t, path)
		delete(root, "mcp")
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
		if s.HookEvents != 3 {
			t.Errorf("expected 3 hook events, got %d", s.HookEvents)
		}
		if s.MCPRegistered {
			t.Errorf("expected mcp not registered")
		}
		if s.Tier != assistant.TierPartial {
			t.Errorf("expected TierPartial, got %v", s.Tier)
		}
	})
}
