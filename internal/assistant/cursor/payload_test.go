package cursor

import (
	"strings"
	"testing"
)

func TestParseHookMapsWhitelistedFields(t *testing.T) {
	body := `{
		"conversation_id": "conv-123",
		"workspace_roots": ["/home/user/project", "/other"],
		"transcript_path": "/tmp/transcript.jsonl",
		"model": "gpt-5",
		"tool_name": "read_file",
		"prompt": "ignore me, this must never surface",
		"tool_input": {"path": "/etc/passwd"},
		"message": "also must not surface"
	}`

	in, err := Adapter{}.ParseHook("prompt", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if in.SessionKey != "conv-123" {
		t.Errorf("SessionKey = %q", in.SessionKey)
	}
	if in.Cwd != "/home/user/project" {
		t.Errorf("Cwd = %q", in.Cwd)
	}
	if in.TranscriptPath != "/tmp/transcript.jsonl" {
		t.Errorf("TranscriptPath = %q", in.TranscriptPath)
	}
	if in.Model != "gpt-5" {
		t.Errorf("Model = %q", in.Model)
	}
	if in.ToolName != "read_file" {
		t.Errorf("ToolName = %q", in.ToolName)
	}
	if in.Usage != nil {
		t.Errorf("Usage = %v, want nil (cursor reports no usage)", in.Usage)
	}
}

func TestParseHookMissingConversationIDErrors(t *testing.T) {
	body := `{"workspace_roots": ["/home/user/project"]}`
	if _, err := (Adapter{}).ParseHook("session-start", strings.NewReader(body)); err == nil {
		t.Fatal("expected error for missing conversation_id")
	}
}

func TestParseHookEmptyWorkspaceRootsLeavesCwdEmpty(t *testing.T) {
	body := `{"conversation_id": "conv-1"}`
	in, err := Adapter{}.ParseHook("session-end", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if in.Cwd != "" {
		t.Errorf("Cwd = %q, want empty", in.Cwd)
	}
}

func TestInjectionResponseEnvelope(t *testing.T) {
	got := Adapter{}.InjectionResponse("pack contents")
	want := `{"additional_context":"pack contents"}`
	if got != want {
		t.Errorf("InjectionResponse = %s, want %s", got, want)
	}
}

func TestToolUseInjectionResponseEnvelope(t *testing.T) {
	got := Adapter{}.ToolUseInjectionResponse("pack contents")
	want := `{"additional_context":"pack contents"}`
	if got != want {
		t.Errorf("ToolUseInjectionResponse = %s, want %s", got, want)
	}
}

func TestStopResponseEnvelope(t *testing.T) {
	got := Adapter{}.StopResponse("please distill")
	want := `{"followup_message":"please distill"}`
	if got != want {
		t.Errorf("StopResponse = %s, want %s", got, want)
	}
}
