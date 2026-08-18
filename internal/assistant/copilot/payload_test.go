package copilot

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseHookMapsWhitelistedFields(t *testing.T) {
	body := `{
		"sessionId": "e0d99a1b-1234-4a56-9abc-def012345678",
		"cwd": "/home/dev/project",
		"transcriptPath": "/home/dev/.copilot/session-state/e0d99a1b/events.jsonl",
		"toolName": "read_file",
		"prompt": "ignore me",
		"toolInput": {"path": "/etc/passwd"},
		"toolResponse": {"content": "secret file contents"}
	}`

	in, err := (Adapter{}).ParseHook("tool-use", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if in.SessionKey != "e0d99a1b-1234-4a56-9abc-def012345678" {
		t.Errorf("SessionKey = %q", in.SessionKey)
	}
	if in.Cwd != "/home/dev/project" {
		t.Errorf("Cwd = %q", in.Cwd)
	}
	if in.TranscriptPath != "/home/dev/.copilot/session-state/e0d99a1b/events.jsonl" {
		t.Errorf("TranscriptPath = %q", in.TranscriptPath)
	}
	if in.ToolName != "read_file" {
		t.Errorf("ToolName = %q", in.ToolName)
	}
	if in.Model != "" {
		t.Errorf("Model = %q, want empty (never populated by ParseHook)", in.Model)
	}
	if in.Usage != nil {
		t.Errorf("Usage = %+v, want nil (comes from ParseEvents backfill)", in.Usage)
	}
}

func TestParseHookMissingSessionID(t *testing.T) {
	_, err := (Adapter{}).ParseHook("session-start", strings.NewReader(`{"cwd":"/x"}`))
	if err == nil {
		t.Fatal("want error for missing sessionId")
	}
	if err.Error() != "payload missing sessionId" {
		t.Errorf("err = %q", err.Error())
	}
}

func TestParseHookWhitelistOnly(t *testing.T) {
	var p payload
	if err := json.Unmarshal([]byte(`{"sessionId":"s1"}`), &p); err != nil {
		t.Fatal(err)
	}
	// Structural whitelist proof: the payload struct has exactly these four
	// JSON-tagged fields, so anything else in the wire body (prompt text,
	// tool bodies) cannot survive decode into it.
	data, err := json.Marshal(payload{
		SessionID:      "s1",
		Cwd:            "/x",
		TranscriptPath: "/y",
		ToolName:       "z",
	})
	if err != nil {
		t.Fatal(err)
	}
	var round map[string]any
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"sessionId": true, "cwd": true, "transcriptPath": true, "toolName": true}
	if len(round) != len(want) {
		t.Fatalf("payload has %d fields, want %d: %v", len(round), len(want), round)
	}
	for k := range round {
		if !want[k] {
			t.Errorf("unexpected field %q in payload", k)
		}
	}
}

func TestInjectionResponse(t *testing.T) {
	got := (Adapter{}).InjectionResponse("pack contents")
	want := `{"additionalContext":"pack contents"}`
	if got != want {
		t.Errorf("InjectionResponse = %q, want %q", got, want)
	}
}

func TestToolUseInjectionResponse(t *testing.T) {
	got := (Adapter{}).ToolUseInjectionResponse("pack contents")
	want := `{"additionalContext":"pack contents"}`
	if got != want {
		t.Errorf("ToolUseInjectionResponse = %q, want %q", got, want)
	}
}

func TestStopResponse(t *testing.T) {
	got := (Adapter{}).StopResponse("distill now")
	want := `{"decision":"block","reason":"distill now"}`
	if got != want {
		t.Errorf("StopResponse = %q, want %q", got, want)
	}
}
