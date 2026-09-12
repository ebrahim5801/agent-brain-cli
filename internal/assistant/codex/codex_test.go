package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

const binPath = "/usr/local/bin/agent-brain"

func setupHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	return dir
}

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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ourHooks returns the agent-brain hook entries registered for one event.
func ourHooks(t *testing.T, root map[string]any, event string) []map[string]any {
	t.Helper()
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return nil
	}
	groups, ok := hooks[event].([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := group["hooks"].([]any)
		for _, h := range inner {
			if hook, ok := h.(map[string]any); ok && isOursHook(hook) {
				out = append(out, hook)
			}
		}
	}
	return out
}

func TestDetected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(dir, "does-not-exist"))
	if Detected() {
		t.Error("expected not detected for a missing dir")
	}
	t.Setenv("CODEX_HOME", dir)
	if !Detected() {
		t.Error("expected detected for an existing dir")
	}
}

func TestAdapterIdentity(t *testing.T) {
	a := Adapter{}
	if a.Name() != "codex" {
		t.Errorf("Name() = %q", a.Name())
	}
	if a.DisplayName() != "Codex CLI" {
		t.Errorf("DisplayName() = %q", a.DisplayName())
	}
	// The external session id is "<name>:<session_key>", so a ':' in the name
	// would corrupt every Codex session's identity.
	if strings.Contains(a.Name(), ":") {
		t.Errorf("Name() must not contain ':': %q", a.Name())
	}
}

func TestFreshInstallWritesBothFiles(t *testing.T) {
	dir := setupHome(t)

	backups, err := Install(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Errorf("fresh install returned backups: %v", backups)
	}

	root := readJSON(t, filepath.Join(dir, "hooks.json"))
	for _, he := range hookEvents {
		entries := ourHooks(t, root, he.Event)
		if len(entries) != 1 {
			t.Fatalf("hooks[%s] = %d agent-brain entries, want 1", he.Event, len(entries))
		}
		e := entries[0]
		wantCmd := binPath + " hook " + he.Sub + " --assistant codex"
		if e["command"] != wantCmd {
			t.Errorf("hooks[%s].command = %v, want %v", he.Event, e["command"], wantCmd)
		}
		if e["type"] != "command" {
			t.Errorf("hooks[%s].type = %v, want command", he.Event, e["type"])
		}
		if e["timeout"] != float64(he.Timeout) {
			t.Errorf("hooks[%s].timeout = %v, want %d", he.Event, e["timeout"], he.Timeout)
		}
		if he.Status == "" {
			if _, present := e["statusMessage"]; present {
				t.Errorf("hooks[%s] carries an unexpected statusMessage", he.Event)
			}
		} else if e["statusMessage"] != he.Status {
			t.Errorf("hooks[%s].statusMessage = %v, want %q", he.Event, e["statusMessage"], he.Status)
		}
		// An explicit matcher is deliberately omitted: absent matches everything.
		groups := root["hooks"].(map[string]any)[he.Event].([]any)
		if _, present := groups[0].(map[string]any)["matcher"]; present {
			t.Errorf("hooks[%s] group carries a matcher; it should be omitted", he.Event)
		}
	}

	cfg := readFile(t, filepath.Join(dir, "config.toml"))
	want := "[mcp_servers.agent-brain-memory]\ncommand = \"" + binPath + "\"\nargs = [\"mcp\"]\n"
	if cfg != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", cfg, want)
	}
}

// The installed command must carry --assistant codex: the hook subcommand's
// flag defaults to claude-code, so without it every Codex session would be
// filed under Claude Code.
func TestInstalledCommandsCarryAssistantFlag(t *testing.T) {
	dir := setupHome(t)
	if _, err := Install(binPath); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, filepath.Join(dir, "hooks.json"))
	if strings.Count(raw, "--assistant codex") != len(hookEvents) {
		t.Errorf("expected %d --assistant codex occurrences in:\n%s", len(hookEvents), raw)
	}
}

func TestInstallMergesWithForeignConfig(t *testing.T) {
	dir := setupHome(t)
	hooksPath := filepath.Join(dir, "hooks.json")
	cfgPath := filepath.Join(dir, "config.toml")

	// A foreign event, and a foreign hook sitting inside one of our own events.
	seedHooks := `{"hooks":{
	  "Notification":[{"hooks":[{"type":"command","command":"echo foreign"}]}],
	  "SessionStart":[{"hooks":[{"type":"command","command":"echo user-start"}]}]
	}}`
	if err := os.WriteFile(hooksPath, []byte(seedHooks), 0o644); err != nil {
		t.Fatal(err)
	}
	seedCfg := "model = \"gpt-6\"\n\n# the user's own server\n[mcp_servers.other]\ncommand = \"other\"\nargs = [\"run\"]\n"
	if err := os.WriteFile(cfgPath, []byte(seedCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	backups, err := Install(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 2 {
		t.Fatalf("backups = %v, want one per changed file", backups)
	}
	for _, b := range backups {
		if _, err := os.Stat(b); err != nil {
			t.Errorf("backup missing on disk: %v", err)
		}
	}

	root := readJSON(t, hooksPath)
	hooks := root["hooks"].(map[string]any)
	if _, ok := hooks["Notification"]; !ok {
		t.Error("foreign event lost")
	}
	starts := hooks["SessionStart"].([]any)
	if len(starts) != 2 {
		t.Fatalf("SessionStart groups = %d, want the user's plus ours", len(starts))
	}
	foundUser := false
	for _, g := range starts {
		inner := g.(map[string]any)["hooks"].([]any)
		for _, h := range inner {
			if h.(map[string]any)["command"] == "echo user-start" {
				foundUser = true
			}
		}
	}
	if !foundUser {
		t.Error("user's SessionStart hook lost")
	}

	cfg := readFile(t, cfgPath)
	if !strings.HasPrefix(cfg, seedCfg) {
		t.Errorf("foreign TOML content not preserved verbatim:\n%q", cfg)
	}
	if !strings.Contains(cfg, "[mcp_servers.agent-brain-memory]") {
		t.Error("our MCP table missing")
	}
}

func TestReinstallIsIdempotent(t *testing.T) {
	dir := setupHome(t)
	if _, err := Install(binPath); err != nil {
		t.Fatal(err)
	}
	hooksBefore := readFile(t, filepath.Join(dir, "hooks.json"))
	cfgBefore := readFile(t, filepath.Join(dir, "config.toml"))

	backups, err := Install(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Errorf("reinstall returned backups: %v", backups)
	}
	if got := readFile(t, filepath.Join(dir, "hooks.json")); got != hooksBefore {
		t.Error("reinstall changed hooks.json bytes")
	}
	if got := readFile(t, filepath.Join(dir, "config.toml")); got != cfgBefore {
		t.Error("reinstall changed config.toml bytes")
	}
}

// Idempotence has to survive a user adding their own hook to an event we also
// use. Removing our entry and appending a fresh one turns [ours, theirs] into
// [theirs, ours] — same meaning, different bytes, so every reinstall would
// rewrite the file and take another backup of it.
func TestReinstallIsIdempotentAlongsideAForeignHookOnOurEvent(t *testing.T) {
	dir := setupHome(t)
	hooksPath := filepath.Join(dir, "hooks.json")
	if _, err := Install(binPath); err != nil {
		t.Fatal(err)
	}

	root := readJSON(t, hooksPath)
	hooks := root["hooks"].(map[string]any)
	hooks["PostToolUse"] = append(hooks["PostToolUse"].([]any),
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo user-tool-use"}}})
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, hooksPath)

	backups, err := Install(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Errorf("reinstall returned backups: %v", backups)
	}
	if got := readFile(t, hooksPath); got != before {
		t.Errorf("reinstall rewrote hooks.json:\n%s\nwant\n%s", got, before)
	}
	if !strings.Contains(before, "echo user-tool-use") {
		t.Fatal("the foreign hook this test is about was not seeded")
	}
}

// An event dropped from the table must still be cleaned up on the next install,
// or a hook we no longer support keeps firing forever.
func TestInstallRemovesOurEntriesFromEventsWeNoLongerUse(t *testing.T) {
	dir := setupHome(t)
	hooksPath := filepath.Join(dir, "hooks.json")
	seed := map[string]any{"hooks": map[string]any{
		"PermissionRequest": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": binPath + " hook permission --assistant codex"},
		}}},
	}}
	data, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(binPath); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, hooksPath); strings.Contains(got, "PermissionRequest") {
		t.Errorf("a hook for an event we no longer install survived:\n%s", got)
	}
}

func TestUninstallRemovesOnlyOurEntries(t *testing.T) {
	dir := setupHome(t)
	hooksPath := filepath.Join(dir, "hooks.json")
	cfgPath := filepath.Join(dir, "config.toml")

	seedCfg := "model = \"gpt-6\"\n\n# the user's own server\n[mcp_servers.other]\ncommand = \"other\"\nargs = [\"run\"]\n"
	if err := os.WriteFile(cfgPath, []byte(seedCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(binPath); err != nil {
		t.Fatal(err)
	}
	// Install legitimately backs up the seeded config.toml; clear those so the
	// assertion at the end is about what Uninstall did, not what Install did.
	installBackups, err := filepath.Glob(filepath.Join(dir, "*agent-brain-backup*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range installBackups {
		if err := os.Remove(b); err != nil {
			t.Fatal(err)
		}
	}

	// Add a foreign hook alongside ours after install.
	root := readJSON(t, hooksPath)
	hooks := root["hooks"].(map[string]any)
	hooks["SessionStart"] = append(hooks["SessionStart"].([]any),
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo user-start"}}})
	data, _ := json.Marshal(root)
	if err := os.WriteFile(hooksPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}

	root = readJSON(t, hooksPath)
	hooks = root["hooks"].(map[string]any)
	if len(ourHooks(t, root, "SessionStart")) != 0 {
		t.Error("our SessionStart hook survived uninstall")
	}
	starts, ok := hooks["SessionStart"].([]any)
	if !ok || len(starts) != 1 {
		t.Fatalf("SessionStart = %v, want only the user's group", hooks["SessionStart"])
	}
	for _, he := range hookEvents {
		if he.Event == "SessionStart" {
			continue
		}
		if _, present := hooks[he.Event]; present {
			t.Errorf("event %s should have been dropped once empty", he.Event)
		}
	}

	if got := readFile(t, cfgPath); got != seedCfg {
		t.Errorf("config.toml did not round-trip:\ngot  %q\nwant %q", got, seedCfg)
	}

	entries, _ := filepath.Glob(filepath.Join(dir, "*agent-brain-backup*"))
	if len(entries) != 0 {
		t.Errorf("uninstall created backups: %v", entries)
	}
}

func TestUninstallMissingFilesIsNoop(t *testing.T) {
	setupHome(t)
	if err := Uninstall(); err != nil {
		t.Errorf("uninstall with no files: %v", err)
	}
}

func TestInstallUnparseableHooksFileErrorsAndLeavesFilesUntouched(t *testing.T) {
	dir := setupHome(t)
	hooksPath := filepath.Join(dir, "hooks.json")
	broken := `{broken`
	if err := os.WriteFile(hooksPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(binPath); err == nil {
		t.Fatal("expected an error installing over an unparseable hooks.json")
	}
	if got := readFile(t, hooksPath); got != broken {
		t.Errorf("hooks.json was modified: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); !os.IsNotExist(err) {
		t.Error("config.toml written despite the hooks.json failure")
	}
}

func TestInstallDuplicateMCPTableErrorsAndLeavesConfigUntouched(t *testing.T) {
	dir := setupHome(t)
	cfgPath := filepath.Join(dir, "config.toml")
	seed := "[mcp_servers.agent-brain-memory]\ncommand = \"/a\"\n\n[mcp_servers.agent-brain-memory]\ncommand = \"/b\"\n"
	if err := os.WriteFile(cfgPath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(binPath); err == nil {
		t.Fatal("expected an error for a duplicated MCP table")
	}
	if got := readFile(t, cfgPath); got != seed {
		t.Errorf("config.toml was modified despite the error: %q", got)
	}
}

func TestStateMatrix(t *testing.T) {
	t.Run("not integrated", func(t *testing.T) {
		setupHome(t)
		s, err := Adapter{}.State()
		if err != nil {
			t.Fatal(err)
		}
		if s.Tier != assistant.TierNotIntegrated {
			t.Errorf("tier = %v, want not-integrated", s.Tier)
		}
		if len(s.Notes) != 0 {
			t.Errorf("notes = %v, want none when nothing is installed", s.Notes)
		}
	})

	t.Run("full", func(t *testing.T) {
		setupHome(t)
		if _, err := Install(binPath); err != nil {
			t.Fatal(err)
		}
		s, err := Adapter{}.State()
		if err != nil {
			t.Fatal(err)
		}
		if s.HookEvents != len(hookEvents) || s.HookEventsWanted != len(hookEvents) || !s.MCPRegistered {
			t.Errorf("state = %+v, want full", s)
		}
		if s.Tier != assistant.TierFull {
			t.Errorf("tier = %v, want full", s.Tier)
		}
	})

	t.Run("mcp only", func(t *testing.T) {
		dir := setupHome(t)
		if _, err := Install(binPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "hooks.json")); err != nil {
			t.Fatal(err)
		}
		s, err := Adapter{}.State()
		if err != nil {
			t.Fatal(err)
		}
		if s.Tier != assistant.TierMCPOnly {
			t.Errorf("tier = %v, want mcp-only", s.Tier)
		}
	})

	t.Run("partial", func(t *testing.T) {
		dir := setupHome(t)
		if _, err := Install(binPath); err != nil {
			t.Fatal(err)
		}
		root := readJSON(t, filepath.Join(dir, "hooks.json"))
		hooks := root["hooks"].(map[string]any)
		delete(hooks, "Stop")
		data, _ := json.Marshal(root)
		if err := os.WriteFile(filepath.Join(dir, "hooks.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := Adapter{}.State()
		if err != nil {
			t.Fatal(err)
		}
		if s.HookEvents != len(hookEvents)-1 {
			t.Errorf("hookEvents = %d, want %d", s.HookEvents, len(hookEvents)-1)
		}
		if s.Tier != assistant.TierPartial {
			t.Errorf("tier = %v, want partial", s.Tier)
		}
	})
}

func TestPostInstallNoticeNamesTheSilentFailure(t *testing.T) {
	var a any = Adapter{}
	n, ok := a.(assistant.PostInstallNotice)
	if !ok {
		t.Fatal("Codex adapter must implement assistant.PostInstallNotice")
	}
	text := n.PostInstallNotice()
	for _, want := range []string{"/hooks", "records nothing", "no error"} {
		if !strings.Contains(text, want) {
			t.Errorf("notice missing %q:\n%s", want, text)
		}
	}
}

func TestExpectedEventCountTracksTheTable(t *testing.T) {
	if ExpectedEventCount() != len(hookEvents) {
		t.Errorf("ExpectedEventCount() = %d, want %d", ExpectedEventCount(), len(hookEvents))
	}
}
