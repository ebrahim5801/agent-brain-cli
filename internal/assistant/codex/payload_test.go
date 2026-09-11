package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

// Payloads captured verbatim from codex-cli 0.154.0 on 2026-09-11, trimmed only
// of whitespace. Keeping them literal is the point: if Codex changes a field
// name, these fail rather than silently degrading to empty capture.
const (
	sessionStartPayload = `{"session_id":"01a09161-007c-7cd3-99ab-d0390feb1328",` +
		`"transcript_path":"/home/u/.codex/sessions/2026/09/11/rollout-2026-09-11T19-50-48-01a09161.jsonl",` +
		`"cwd":"/tmp/cx-probe/work","hook_event_name":"SessionStart","model":"gpt-6-astra",` +
		`"permission_mode":"bypassPermissions","source":"startup"}`

	userPromptSubmitPayload = `{"session_id":"01a09161-007c-7cd3-99ab-d0390feb1328",` +
		`"turn_id":"01a09161-00ec-7af1-823a-7b14ba87f69e",` +
		`"transcript_path":"/home/u/.codex/sessions/2026/09/11/rollout.jsonl",` +
		`"cwd":"/tmp/cx-probe/work","hook_event_name":"UserPromptSubmit","model":"gpt-6-astra",` +
		`"permission_mode":"bypassPermissions",` +
		`"prompt":"Run the shell command 'cat README.md' and tell me the contents"}`

	postToolUsePayload = `{"session_id":"01a09161-007c-7cd3-99ab-d0390feb1328",` +
		`"turn_id":"01a09161-00ec-7af1-823a-7b14ba87f69e",` +
		`"transcript_path":"/home/u/.codex/sessions/2026/09/11/rollout.jsonl",` +
		`"cwd":"/tmp/cx-probe/work","hook_event_name":"PostToolUse","model":"gpt-6-astra",` +
		`"permission_mode":"bypassPermissions","tool_name":"Bash",` +
		`"tool_input":{"command":"cat /etc/passwd"},` +
		`"tool_response":"root:x:0:0:root:/root:/bin/bash\n",` +
		`"tool_use_id":"exec-930f06ea-870d-4c71-b70c-52291fa56b52"}`

	stopPayload = `{"session_id":"01a09161-007c-7cd3-99ab-d0390feb1328",` +
		`"turn_id":"01a09161-00ec-7af1-823a-7b14ba87f69e",` +
		`"transcript_path":"/home/u/.codex/sessions/2026/09/11/rollout.jsonl",` +
		`"cwd":"/tmp/cx-probe/work","hook_event_name":"Stop","model":"gpt-6-astra",` +
		`"permission_mode":"bypassPermissions","stop_hook_active":false,` +
		`"last_assistant_message":"I could not read README.md because the command failed"}`
)

func TestParseHookMapsWhitelistedFields(t *testing.T) {
	for _, tc := range []struct {
		name, event, payload string
		wantTool             string
	}{
		{"session-start", assistant.EventSessionStart, sessionStartPayload, ""},
		{"prompt", assistant.EventPrompt, userPromptSubmitPayload, ""},
		{"tool-use", assistant.EventToolUse, postToolUsePayload, "Bash"},
		{"stop", assistant.EventStop, stopPayload, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := Adapter{}.ParseHook(tc.event, strings.NewReader(tc.payload))
			if err != nil {
				t.Fatal(err)
			}
			if in.SessionKey != "01a09161-007c-7cd3-99ab-d0390feb1328" {
				t.Errorf("SessionKey = %q", in.SessionKey)
			}
			if in.Cwd != "/tmp/cx-probe/work" {
				t.Errorf("Cwd = %q", in.Cwd)
			}
			if !strings.HasSuffix(in.TranscriptPath, ".jsonl") {
				t.Errorf("TranscriptPath = %q", in.TranscriptPath)
			}
			// Codex sends model on every event, unlike Claude Code, so the
			// session row carries it from session-start onward.
			if in.Model != "gpt-6-astra" {
				t.Errorf("Model = %q, want gpt-6-astra", in.Model)
			}
			if in.ToolName != tc.wantTool {
				t.Errorf("ToolName = %q, want %q", in.ToolName, tc.wantTool)
			}
			if in.Usage != nil {
				t.Errorf("Usage = %+v, want nil (Codex reports no usage on hooks)", in.Usage)
			}
		})
	}
}

// The privacy regression guard every adapter carries: marshal the neutral
// HookInput and prove none of the payload's sensitive fields can appear in it.
// tool_input and tool_response are the Codex-specific additions — they carry the
// full shell command and its full output.
func TestParseHookWhitelistOnly(t *testing.T) {
	forbidden := []string{
		"Run the shell command",   // prompt text
		"cat /etc/passwd",         // tool_input
		"root:x:0:0",              // tool_response
		"I could not read README", // last_assistant_message
		"bypassPermissions",       // permission_mode
		"startup",                 // source
		"exec-930f06ea",           // tool_use_id
		"01a09161-00ec",           // turn_id
	}
	for _, payload := range []string{sessionStartPayload, userPromptSubmitPayload, postToolUsePayload, stopPayload} {
		in, err := Adapter{}.ParseHook(assistant.EventToolUse, strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range forbidden {
			if strings.Contains(string(data), f) {
				t.Errorf("HookInput leaked %q: %s", f, data)
			}
		}
	}
}

func TestParseHookMissingSessionIDErrors(t *testing.T) {
	if _, err := (Adapter{}).ParseHook(assistant.EventSessionStart, strings.NewReader(`{"cwd":"/x"}`)); err == nil {
		t.Fatal("expected an error for a payload with no session_id")
	}
}

func TestParseHookMalformedJSONErrors(t *testing.T) {
	if _, err := (Adapter{}).ParseHook(assistant.EventSessionStart, strings.NewReader(`{broken`)); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

// envelopeField digs one level into hookSpecificOutput.
func envelopeField(t *testing.T, envelope, key string) string {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal([]byte(envelope), &root); err != nil {
		t.Fatalf("envelope is not valid JSON (%v): %s", err, envelope)
	}
	hso, ok := root["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("no hookSpecificOutput in %s", envelope)
	}
	v, _ := hso[key].(string)
	return v
}

func TestInjectionEnvelopes(t *testing.T) {
	a := Adapter{}

	if got := envelopeField(t, a.InjectionResponse("pack"), "hookEventName"); got != "SessionStart" {
		t.Errorf("session-start hookEventName = %q", got)
	}
	if got := envelopeField(t, a.InjectionResponse("pack"), "additionalContext"); got != "pack" {
		t.Errorf("additionalContext = %q", got)
	}
	if got := envelopeField(t, a.PromptInjectionResponse("pack"), "hookEventName"); got != "UserPromptSubmit" {
		t.Errorf("prompt hookEventName = %q", got)
	}

	// An empty notice must be byte-identical to the plain response, so the
	// notifier path never changes what the model sees.
	if a.InjectionResponseWithNotice("pack", "") != a.InjectionResponse("pack") {
		t.Error("an empty notice changed the envelope")
	}
	withNotice := a.InjectionResponseWithNotice("pack", "loaded 3 memories")
	var root map[string]any
	if err := json.Unmarshal([]byte(withNotice), &root); err != nil {
		t.Fatal(err)
	}
	if root["systemMessage"] != "loaded 3 memories" {
		t.Errorf("systemMessage = %v", root["systemMessage"])
	}
}

// A pack containing newlines must survive as escaped JSON. Codex rejects hook
// stdout that is not valid JSON (it reports the hook as Failed and injects
// nothing), and a memory pack is always multi-line.
func TestInjectionEnvelopeEscapesNewlines(t *testing.T) {
	pack := "## Project memory\n\n[#1] a fact\n"
	envelope := Adapter{}.InjectionResponse(pack)
	if strings.Contains(envelope, "\n") {
		t.Errorf("envelope contains a raw newline, which Codex rejects as invalid JSON: %q", envelope)
	}
	if got := envelopeField(t, envelope, "additionalContext"); got != pack {
		t.Errorf("pack did not round-trip: %q", got)
	}
}

func TestStopResponse(t *testing.T) {
	var root map[string]string
	if err := json.Unmarshal([]byte((Adapter{}).StopResponse("save your memories")), &root); err != nil {
		t.Fatal(err)
	}
	if root["decision"] != "block" {
		t.Errorf("decision = %q, want block", root["decision"])
	}
	if root["reason"] != "save your memories" {
		t.Errorf("reason = %q", root["reason"])
	}
}

func TestAdapterImplementsExpectedOptionalInterfaces(t *testing.T) {
	var a any = Adapter{}
	if _, ok := a.(assistant.PromptInjector); !ok {
		t.Error("want PromptInjector: UserPromptSubmit accepts additionalContext")
	}
	if _, ok := a.(assistant.InjectionNotifier); !ok {
		t.Error("want InjectionNotifier: systemMessage is a top-level output field")
	}
	// Deferred until probed: blocking on Codex's PostToolUse replaces the tool
	// result rather than prompting the model, which could corrupt it.
	if _, ok := a.(assistant.MidTurnResponder); ok {
		t.Error("MidTurnResponder must stay unimplemented until PostToolUse block semantics are verified")
	}
	if _, ok := a.(assistant.UsageBackfiller); !ok {
		t.Error("want UsageBackfiller: usage is backfilled from the rollout JSONL")
	}
	if _, ok := a.(assistant.CitationScanner); !ok {
		t.Error("want CitationScanner: the rollout JSONL carries the assistant's replies")
	}
	// Phase 3, and only if a probe shows SubagentStop fires at all.
	if _, ok := a.(assistant.SubagentScanner); ok {
		t.Error("SubagentScanner belongs to Phase 3")
	}
}
