package claude

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

func TestInjectionResponseEnvelope(t *testing.T) {
	got := Adapter{}.InjectionResponse("pack contents")
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("envelope not JSON: %v (%s)", err, got)
	}
	if _, ok := m["systemMessage"]; ok {
		t.Error("plain InjectionResponse must not carry systemMessage")
	}
	hso, ok := m["hookSpecificOutput"].(map[string]any)
	if !ok || hso["additionalContext"] != "pack contents" || hso["hookEventName"] != "SessionStart" {
		t.Errorf("unexpected envelope: %s", got)
	}
}

func TestPromptInjectionResponseEnvelope(t *testing.T) {
	got := Adapter{}.PromptInjectionResponse("pack contents")
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("envelope not JSON: %v (%s)", err, got)
	}
	hso, ok := m["hookSpecificOutput"].(map[string]any)
	if !ok || hso["additionalContext"] != "pack contents" || hso["hookEventName"] != "UserPromptSubmit" {
		t.Errorf("unexpected prompt-injection envelope: %s", got)
	}
}

func TestInjectionResponseWithNotice(t *testing.T) {
	got := Adapter{}.InjectionResponseWithNotice("pack contents", "agent-brain: loaded 3 memories")
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("envelope not JSON: %v (%s)", err, got)
	}
	if m["systemMessage"] != "agent-brain: loaded 3 memories" {
		t.Errorf("systemMessage = %v", m["systemMessage"])
	}
	hso, ok := m["hookSpecificOutput"].(map[string]any)
	if !ok || hso["additionalContext"] != "pack contents" {
		t.Errorf("pack lost from envelope: %s", got)
	}

	if plain, withEmpty := (Adapter{}).InjectionResponse("p"), (Adapter{}).InjectionResponseWithNotice("p", ""); plain != withEmpty {
		t.Errorf("empty notice should match plain response: %q vs %q", plain, withEmpty)
	}
}

func TestParseHookFromSubagent(t *testing.T) {
	for _, tc := range []struct {
		name, event, payload string
		want                 bool
	}{
		{"main conversation", assistant.EventToolUse, `{"session_id":"s","tool_name":"Edit"}`, false},
		{"inside a subagent", assistant.EventToolUse, `{"session_id":"s","tool_name":"Edit","agent_id":"a1"}`, true},
		{"empty agent_id", assistant.EventToolUse, `{"session_id":"s","tool_name":"Edit","agent_id":""}`, false},
		{"subagent-stop names the child", assistant.EventSubagentStop, `{"session_id":"s","agent_id":"a1"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := Adapter{}.ParseHook(tc.event, strings.NewReader(tc.payload))
			if err != nil {
				t.Fatal(err)
			}
			if in.FromSubagent != tc.want {
				t.Errorf("FromSubagent = %v, want %v", in.FromSubagent, tc.want)
			}
		})
	}
}

func TestParseHookUsesTheProjectDirNotTheShellDir(t *testing.T) {
	project := t.TempDir()
	payload := `{"session_id":"s","cwd":` + strconv.Quote(project+"/apps/api") + `}`

	t.Setenv("CLAUDE_PROJECT_DIR", project)
	in, err := Adapter{}.ParseHook(assistant.EventToolUse, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if in.Cwd != project {
		t.Errorf("Cwd = %q, want the project dir %q", in.Cwd, project)
	}

	t.Setenv("CLAUDE_PROJECT_DIR", "")
	in, err = Adapter{}.ParseHook(assistant.EventToolUse, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if in.Cwd != project+"/apps/api" {
		t.Errorf("without CLAUDE_PROJECT_DIR, Cwd = %q, want the payload cwd", in.Cwd)
	}
}
