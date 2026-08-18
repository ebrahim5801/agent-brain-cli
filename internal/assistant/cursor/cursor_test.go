package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func setupHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AGENT_BRAIN_CURSOR_HOME", dir)
	return dir
}

func TestFreshInstallWritesBothFiles(t *testing.T) {
	dir := setupHome(t)

	backups, err := Install("/usr/local/bin/agent-brain")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Errorf("fresh install returned backups: %v", backups)
	}

	hooksRoot := readJSON(t, filepath.Join(dir, "hooks.json"))
	if hooksRoot["version"] != float64(1) {
		t.Errorf("version = %v, want 1", hooksRoot["version"])
	}
	hooks := hooksRoot["hooks"].(map[string]any)
	for _, he := range hookEvents {
		arr, ok := hooks[he.Event].([]any)
		if !ok || len(arr) != 1 {
			t.Fatalf("hooks[%s] = %v", he.Event, hooks[he.Event])
		}
		entry := arr[0].(map[string]any)
		wantCmd := "/usr/local/bin/agent-brain hook " + he.Sub + " --assistant cursor"
		if entry["command"] != wantCmd {
			t.Errorf("hooks[%s].command = %v, want %v", he.Event, entry["command"], wantCmd)
		}
		if entry["timeout"] != float64(10) {
			t.Errorf("hooks[%s].timeout = %v, want 10", he.Event, entry["timeout"])
		}
	}

	mcpRoot := readJSON(t, filepath.Join(dir, "mcp.json"))
	servers := mcpRoot["mcpServers"].(map[string]any)
	ours := servers[MCPServerName].(map[string]any)
	if ours["command"] != "/usr/local/bin/agent-brain" {
		t.Errorf("mcp command = %v", ours["command"])
	}
	args := ours["args"].([]any)
	if len(args) != 1 || args[0] != "mcp" {
		t.Errorf("mcp args = %v", args)
	}
}

func TestInstallMergesWithExistingUserEntries(t *testing.T) {
	dir := setupHome(t)
	hooksPath := filepath.Join(dir, "hooks.json")
	mcpPath := filepath.Join(dir, "mcp.json")

	seedHooks := `{"version":2,"hooks":{"sessionStart":[{"command":"echo user-hook","timeout":5}]}}`
	if err := os.WriteFile(hooksPath, []byte(seedHooks), 0o644); err != nil {
		t.Fatal(err)
	}
	seedMCP := `{"mcpServers":{"other-server":{"command":"other","args":["run"]}}}`
	if err := os.WriteFile(mcpPath, []byte(seedMCP), 0o644); err != nil {
		t.Fatal(err)
	}

	backups, err := Install("/usr/local/bin/agent-brain")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 2 {
		t.Fatalf("backups = %v, want 2 entries", backups)
	}
	for _, b := range backups {
		if _, err := os.Stat(b); err != nil {
			t.Errorf("backup file missing: %v", err)
		}
	}

	hooksRoot := readJSON(t, hooksPath)
	if hooksRoot["version"] != float64(2) {
		t.Errorf("version = %v, want preserved 2", hooksRoot["version"])
	}
	hooks := hooksRoot["hooks"].(map[string]any)
	sessionStart := hooks["sessionStart"].([]any)
	if len(sessionStart) != 2 {
		t.Fatalf("sessionStart = %v, want user entry + ours", sessionStart)
	}
	foundUser := false
	for _, e := range sessionStart {
		entry := e.(map[string]any)
		if entry["command"] == "echo user-hook" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Error("user hook entry lost")
	}

	mcpRoot := readJSON(t, mcpPath)
	servers := mcpRoot["mcpServers"].(map[string]any)
	if _, ok := servers["other-server"]; !ok {
		t.Error("foreign mcp server lost")
	}
	if _, ok := servers[MCPServerName]; !ok {
		t.Error("our mcp server missing")
	}
}

func TestReinstallIsIdempotent(t *testing.T) {
	dir := setupHome(t)
	if _, err := Install("/usr/local/bin/agent-brain"); err != nil {
		t.Fatal(err)
	}

	hooksPath := filepath.Join(dir, "hooks.json")
	mcpPath := filepath.Join(dir, "mcp.json")
	hooksBefore, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	mcpBefore, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}

	backups, err := Install("/usr/local/bin/agent-brain")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Errorf("reinstall returned backups: %v", backups)
	}

	hooksAfter, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	mcpAfter, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(hooksBefore) != string(hooksAfter) {
		t.Error("reinstall changed hooks.json bytes")
	}
	if string(mcpBefore) != string(mcpAfter) {
		t.Error("reinstall changed mcp.json bytes")
	}
}

func TestUninstallRemovesOnlyOurEntries(t *testing.T) {
	dir := setupHome(t)
	hooksPath := filepath.Join(dir, "hooks.json")
	mcpPath := filepath.Join(dir, "mcp.json")

	if _, err := Install("/usr/local/bin/agent-brain"); err != nil {
		t.Fatal(err)
	}

	hooksRoot := readJSON(t, hooksPath)
	hooks := hooksRoot["hooks"].(map[string]any)
	hooks["sessionStart"] = append(hooks["sessionStart"].([]any), map[string]any{"command": "echo user-hook", "timeout": 5})
	data, _ := json.Marshal(hooksRoot)
	if err := os.WriteFile(hooksPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	mcpRoot := readJSON(t, mcpPath)
	servers := mcpRoot["mcpServers"].(map[string]any)
	servers["other-server"] = map[string]any{"command": "other", "args": []any{"run"}}
	data, _ = json.Marshal(mcpRoot)
	if err := os.WriteFile(mcpPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}

	hooksRoot = readJSON(t, hooksPath)
	hooks = hooksRoot["hooks"].(map[string]any)
	if _, ok := hooks["sessionStart"]; !ok {
		t.Fatal("user sessionStart entries lost")
	}
	sessionStart := hooks["sessionStart"].([]any)
	if len(sessionStart) != 1 {
		t.Fatalf("sessionStart = %v, want only user entry", sessionStart)
	}
	if sessionStart[0].(map[string]any)["command"] != "echo user-hook" {
		t.Error("wrong entry survived uninstall")
	}
	for _, he := range hookEvents {
		if he.Event == "sessionStart" {
			continue
		}
		if _, ok := hooks[he.Event]; ok {
			t.Errorf("event %s should have been dropped", he.Event)
		}
	}

	mcpRoot = readJSON(t, mcpPath)
	servers = mcpRoot["mcpServers"].(map[string]any)
	if _, ok := servers[MCPServerName]; ok {
		t.Error("our mcp entry survived uninstall")
	}
	if _, ok := servers["other-server"]; !ok {
		t.Error("foreign mcp entry lost")
	}

	entries, err := filepath.Glob(filepath.Join(dir, "*backup*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("uninstall created backup files: %v", entries)
	}
}

func TestUninstallMissingFilesIsNoop(t *testing.T) {
	setupHome(t)
	if err := Uninstall(); err != nil {
		t.Errorf("uninstall with no files: %v", err)
	}
}

func TestInstallUnparseableHooksFileErrorsAndLeavesFileUntouched(t *testing.T) {
	dir := setupHome(t)
	hooksPath := filepath.Join(dir, "hooks.json")
	broken := `{broken`
	if err := os.WriteFile(hooksPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install("/usr/local/bin/agent-brain"); err == nil {
		t.Fatal("expected error installing over unparseable hooks.json")
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != broken {
		t.Errorf("hooks.json was modified: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "mcp.json")); !os.IsNotExist(err) {
		t.Error("mcp.json should not have been written after hooks.json failure")
	}
}

func TestStateNotIntegrated(t *testing.T) {
	setupHome(t)
	s, err := Adapter{}.State()
	if err != nil {
		t.Fatal(err)
	}
	if s.HookEvents != 0 || s.MCPRegistered {
		t.Errorf("state = %+v, want nothing integrated", s)
	}
	if s.Tier != assistant.TierNotIntegrated {
		t.Errorf("tier = %v, want not-integrated", s.Tier)
	}
	if len(s.Notes) != 0 {
		t.Errorf("notes = %v, want none when not integrated", s.Notes)
	}
}

func TestStateFullAfterInstall(t *testing.T) {
	setupHome(t)
	if _, err := Install("/usr/local/bin/agent-brain"); err != nil {
		t.Fatal(err)
	}
	s, err := Adapter{}.State()
	if err != nil {
		t.Fatal(err)
	}
	if s.HookEvents != 5 || s.HookEventsWanted != 5 || !s.MCPRegistered {
		t.Errorf("state = %+v, want full", s)
	}
	if s.Tier != assistant.TierFull {
		t.Errorf("tier = %v, want full", s.Tier)
	}
	found := false
	for _, n := range s.Notes {
		if n == "token usage not reported by this assistant" {
			found = true
		}
	}
	if !found {
		t.Errorf("notes = %v, want honesty note", s.Notes)
	}
}

func TestStatePartialWithSomeEventsMissing(t *testing.T) {
	dir := setupHome(t)
	if _, err := Install("/usr/local/bin/agent-brain"); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(dir, "hooks.json")
	root := readJSON(t, hooksPath)
	hooks := root["hooks"].(map[string]any)
	delete(hooks, "postToolUse")
	delete(hooks, "stop")
	data, _ := json.Marshal(root)
	if err := os.WriteFile(hooksPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Adapter{}.State()
	if err != nil {
		t.Fatal(err)
	}
	if s.HookEvents != 3 {
		t.Errorf("hookEvents = %d, want 3", s.HookEvents)
	}
	if s.Tier != assistant.TierPartial {
		t.Errorf("tier = %v, want partial", s.Tier)
	}
}

func TestDetected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_BRAIN_CURSOR_HOME", filepath.Join(dir, "does-not-exist"))
	if Detected() {
		t.Error("expected not detected for missing dir")
	}

	t.Setenv("AGENT_BRAIN_CURSOR_HOME", dir)
	if !Detected() {
		t.Error("expected detected for existing dir")
	}
}

func TestAdapterIdentity(t *testing.T) {
	a := Adapter{}
	if a.Name() != "cursor" {
		t.Errorf("Name() = %q", a.Name())
	}
	if a.DisplayName() != "Cursor" {
		t.Errorf("DisplayName() = %q", a.DisplayName())
	}
}
