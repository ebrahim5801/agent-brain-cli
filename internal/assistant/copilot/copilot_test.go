package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func setHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("COPILOT_HOME", dir)
	return dir
}

func TestDetected(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "copilot-home")
	t.Setenv("COPILOT_HOME", dir)
	if Detected() {
		t.Error("nonexistent COPILOT_HOME dir should not be detected")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !Detected() {
		t.Error("existing COPILOT_HOME dir should be detected")
	}
}

func TestInstallFresh(t *testing.T) {
	dir := setHome(t)
	backups, err := Install("/usr/local/bin/agent-brain")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Errorf("fresh install backups = %v, want none", backups)
	}

	hPath := filepath.Join(dir, "hooks", "agent-brain.json")
	var hf hooksFile
	data, err := os.ReadFile(hPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &hf); err != nil {
		t.Fatal(err)
	}
	if hf.Version != 1 {
		t.Errorf("version = %d, want 1", hf.Version)
	}
	if len(hf.Hooks) != 5 {
		t.Fatalf("hooks = %d events, want 5", len(hf.Hooks))
	}
	for _, he := range hookEvents {
		cmds, ok := hf.Hooks[he.Event]
		if !ok || len(cmds) != 1 {
			t.Fatalf("event %s missing or malformed: %v", he.Event, cmds)
		}
		c := cmds[0]
		wantBash := "/usr/local/bin/agent-brain hook " + he.Sub + " --assistant copilot-cli"
		wantPS := "& '/usr/local/bin/agent-brain' hook " + he.Sub + " --assistant copilot-cli"
		if c.Bash != wantBash {
			t.Errorf("event %s bash = %q, want %q", he.Event, c.Bash, wantBash)
		}
		if c.PowerShell != wantPS {
			t.Errorf("event %s powershell = %q, want %q", he.Event, c.PowerShell, wantPS)
		}
		if c.TimeoutSec != 10 {
			t.Errorf("event %s timeoutSec = %d, want 10", he.Event, c.TimeoutSec)
		}
		if c.Type != "command" {
			t.Errorf("event %s type = %q, want command", he.Event, c.Type)
		}
	}

	mPath := filepath.Join(dir, "mcp-config.json")
	root := readJSON(t, mPath)
	servers := root["mcpServers"].(map[string]any)
	entry := servers[MCPServerName].(map[string]any)
	if entry["type"] != "local" || entry["command"] != "/usr/local/bin/agent-brain" {
		t.Errorf("mcp entry = %v", entry)
	}
	args := entry["args"].([]any)
	if len(args) != 1 || args[0] != "mcp" {
		t.Errorf("mcp args = %v", args)
	}
}

func TestInstallMergePreservesForeignServer(t *testing.T) {
	dir := setHome(t)
	mPath := filepath.Join(dir, "mcp-config.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"mcpServers":{"other-server":{"type":"local","command":"other"}}}`
	if err := os.WriteFile(mPath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	backups, err := Install("/bin/agent-brain")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want exactly one", backups)
	}
	if _, err := os.Stat(backups[0]); err != nil {
		t.Errorf("backup file missing: %v", err)
	}

	root := readJSON(t, mPath)
	servers := root["mcpServers"].(map[string]any)
	if _, ok := servers["other-server"]; !ok {
		t.Error("foreign server lost")
	}
	if _, ok := servers[MCPServerName]; !ok {
		t.Error("our server not registered")
	}
}

func TestInstallIdempotent(t *testing.T) {
	setHome(t)
	if _, err := Install("/bin/agent-brain"); err != nil {
		t.Fatal(err)
	}

	hPath, _ := hooksPath()
	mPath, _ := mcpConfigPath()
	hBefore, err := os.ReadFile(hPath)
	if err != nil {
		t.Fatal(err)
	}
	mBefore, err := os.ReadFile(mPath)
	if err != nil {
		t.Fatal(err)
	}

	backups, err := Install("/bin/agent-brain")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Errorf("re-install backups = %v, want none", backups)
	}

	hAfter, err := os.ReadFile(hPath)
	if err != nil {
		t.Fatal(err)
	}
	mAfter, err := os.ReadFile(mPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(hBefore) != string(hAfter) {
		t.Error("owned hooks file bytes changed on re-install")
	}
	if string(mBefore) != string(mAfter) {
		t.Error("mcp-config.json bytes changed on re-install")
	}
}

func TestUninstall(t *testing.T) {
	dir := setHome(t)
	mPath := filepath.Join(dir, "mcp-config.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"mcpServers":{"other-server":{"type":"local","command":"other"}}}`
	if err := os.WriteFile(mPath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install("/bin/agent-brain"); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}

	hPath, _ := hooksPath()
	if _, err := os.Stat(hPath); !os.IsNotExist(err) {
		t.Errorf("owned hooks file survived uninstall: %v", err)
	}

	root := readJSON(t, mPath)
	servers := root["mcpServers"].(map[string]any)
	if _, ok := servers[MCPServerName]; ok {
		t.Error("our mcp entry survived uninstall")
	}
	if _, ok := servers["other-server"]; !ok {
		t.Error("foreign mcp entry lost")
	}
}

func TestUninstallNeverIntegratedIsNoop(t *testing.T) {
	setHome(t)
	if err := Uninstall(); err != nil {
		t.Fatalf("never-integrated uninstall should succeed, got %v", err)
	}
}

func TestInstallUnparseableMCPConfigAborts(t *testing.T) {
	dir := setHome(t)
	mPath := filepath.Join(dir, "mcp-config.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := `{not valid json`
	if err := os.WriteFile(mPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install("/bin/agent-brain"); err == nil {
		t.Fatal("want error for unparseable mcp-config.json")
	}

	data, err := os.ReadFile(mPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != bad {
		t.Error("unparseable mcp-config.json was modified")
	}
}

func TestStateNotIntegrated(t *testing.T) {
	setHome(t)
	s, err := State()
	if err != nil {
		t.Fatal(err)
	}
	if s.HookEvents != 0 || s.MCPRegistered {
		t.Errorf("state = %+v, want nothing integrated", s)
	}
	if s.HookEventsWanted != 5 {
		t.Errorf("wanted = %d, want 5", s.HookEventsWanted)
	}
	if s.Tier != "not-integrated" {
		t.Errorf("tier = %q, want not-integrated", s.Tier)
	}
	if len(s.Notes) != 0 {
		t.Errorf("notes = %v, want none when not integrated", s.Notes)
	}
}

func TestStateFull(t *testing.T) {
	setHome(t)
	if _, err := Install("/bin/agent-brain"); err != nil {
		t.Fatal(err)
	}
	s, err := State()
	if err != nil {
		t.Fatal(err)
	}
	if s.HookEvents != 5 || !s.MCPRegistered {
		t.Errorf("state = %+v, want 5/5 + mcp", s)
	}
	if s.Tier != "full" {
		t.Errorf("tier = %q, want full", s.Tier)
	}
	found := false
	for _, n := range s.Notes {
		if strings.Contains(n, "best-effort") {
			found = true
		}
	}
	if !found {
		t.Errorf("notes = %v, want best-effort token note", s.Notes)
	}
}

func TestStatePartial(t *testing.T) {
	dir := setHome(t)
	hPath := filepath.Join(dir, "hooks", "agent-brain.json")
	if err := os.MkdirAll(filepath.Dir(hPath), 0o755); err != nil {
		t.Fatal(err)
	}
	partial := `{
  "version": 1,
  "hooks": {
    "sessionStart": [{"type":"command","bash":"/bin/agent-brain hook session-start --assistant copilot-cli","powershell":"x","timeoutSec":10}],
    "userPromptSubmitted": [{"type":"command","bash":"/bin/agent-brain hook prompt --assistant copilot-cli","powershell":"x","timeoutSec":10}],
    "postToolUse": [{"type":"command","bash":"/bin/agent-brain hook tool-use --assistant copilot-cli","powershell":"x","timeoutSec":10}]
  }
}`
	if err := os.WriteFile(hPath, []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := State()
	if err != nil {
		t.Fatal(err)
	}
	if s.HookEvents != 3 {
		t.Errorf("hook events = %d, want 3", s.HookEvents)
	}
	if s.MCPRegistered {
		t.Error("mcp should not be registered")
	}
	if s.Tier != "partial" {
		t.Errorf("tier = %q, want partial", s.Tier)
	}
}
