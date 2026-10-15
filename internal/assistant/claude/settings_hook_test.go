package claude

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

func TestShellPath(t *testing.T) {
	cases := []struct{ goos, in, want string }{
		{"linux", "/usr/local/bin/agent-brain", "/usr/local/bin/agent-brain"},
		{"darwin", "/Users/ci/Library/Application Support/agent-brain", "'/Users/ci/Library/Application Support/agent-brain'"},
		{"linux", "/opt/it's/agent-brain", `'/opt/it'\''s/agent-brain'`},
		{"windows", `C:\Users\ci\AppData\Local\Programs\agent-brain\agent-brain.exe`, "C:/Users/ci/AppData/Local/Programs/agent-brain/agent-brain.exe"},
		{"windows", `C:\Users\Jo Smith\AppData\Local\Programs\agent-brain\agent-brain.exe`, "'C:/Users/Jo Smith/AppData/Local/Programs/agent-brain/agent-brain.exe'"},
		{"windows", `C:\Program Files (x86)\agent-brain\agent-brain.exe`, "'C:/Program Files (x86)/agent-brain/agent-brain.exe'"},
	}
	for _, c := range cases {
		if got := shellPath(c.in, c.goos); got != c.want {
			t.Errorf("shellPath(%q, %s) = %q, want %q", c.in, c.goos, got, c.want)
		}
	}
}

// The command Install writes must run the binary under sh, as Claude Code runs
// it, even from a folder whose name a shell would otherwise split.
func TestHookCommandRunsUnderSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("covered on Windows by the CI install check, which runs the hooks through Git Bash")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	dir := filepath.Join(t.TempDir(), "with space", "it's here")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "agent-brain")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + strings.ReplaceAll(out, "'", `'\''`) + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if _, err := Install(bin); err != nil {
		t.Fatal(err)
	}
	path, _ := SettingsPath()
	root := readJSON(t, path)
	groups := root["hooks"].(map[string]any)["SessionStart"].([]any)
	command := groups[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	if !isOurs(command) {
		t.Fatalf("isOurs(%q) = false", command)
	}

	if b, err := exec.Command(sh, "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("sh -c %q: %v\n%s", command, err, b)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hook\nsession-start\n" {
		t.Fatalf("the binary got %q", got)
	}
}

// Hook commands an older release wrote on Windows never run under Git Bash, so
// status must call the integration broken, and a re-install must replace them.
func TestOldWindowsHookCommandsAreBrokenUntilReinstalled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	old := `C:\Users\ci\AppData\Local\Programs\agent-brain\agent-brain.exe`
	hooks := map[string]any{}
	for _, he := range hookEvents {
		hooks[he.Event] = []any{map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": old + " hook " + he.Sub, "timeout": 10,
		}}}}
	}
	data, err := json.Marshal(map[string]any{"hooks": hooks})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RegisterMCP(old); err != nil {
		t.Fatal(err)
	}

	st, err := Adapter{}.State()
	if err != nil {
		t.Fatal(err)
	}
	if st.Tier != assistant.TierPartial || st.HookEvents != 0 {
		t.Fatalf("old Windows hooks: tier %v with %d/%d events, want partial with 0", st.Tier, st.HookEvents, st.HookEventsWanted)
	}

	if _, err := Install("/usr/local/bin/agent-brain"); err != nil {
		t.Fatal(err)
	}
	st, err = Adapter{}.State()
	if err != nil {
		t.Fatal(err)
	}
	if st.Tier != assistant.TierFull || st.HookEvents != st.HookEventsWanted {
		t.Fatalf("after re-install: tier %v with %d/%d events, want full", st.Tier, st.HookEvents, st.HookEventsWanted)
	}
}
