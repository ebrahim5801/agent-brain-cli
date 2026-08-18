package claude

import (
	"encoding/json"
	"testing"
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
