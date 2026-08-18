package opencode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

func TestParseHookBase(t *testing.T) {
	in := `{"session_id":"oc1","cwd":"/x"}`
	out, err := Adapter{}.ParseHook(assistant.EventSessionStart, strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if out.SessionKey != "oc1" {
		t.Errorf("SessionKey = %q, want oc1", out.SessionKey)
	}
	if out.Cwd != "/x" {
		t.Errorf("Cwd = %q, want /x", out.Cwd)
	}
	if out.Usage != nil {
		t.Errorf("expected nil Usage on non-model-usage event, got %+v", out.Usage)
	}
}

func TestParseHookMissingSessionID(t *testing.T) {
	in := `{"cwd":"/x"}`
	_, err := Adapter{}.ParseHook(assistant.EventSessionStart, strings.NewReader(in))
	if err == nil {
		t.Fatalf("expected error for missing session_id")
	}
}

// TestParseHookModelUsage uses the shape captured live in the spike (see
// plans/spikes/opencode.md and testdata/opencode-events-sample.jsonl): a real
// assistant message carried tokens {input:502, output:3, reasoning:0,
// cache:{read:7424, write:0}}.
func TestParseHookModelUsage(t *testing.T) {
	in := `{"session_id":"oc1","cwd":"/x","model":"big-pickle","usage":{"input":502,"output":3,"reasoning":0,"cache_read":7424,"cache_write":0}}`
	out, err := Adapter{}.ParseHook(assistant.EventModelUsage, strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if out.Model != "big-pickle" {
		t.Errorf("Model = %q, want big-pickle", out.Model)
	}
	if out.Usage == nil {
		t.Fatalf("expected non-nil Usage")
	}
	if out.Usage.Input != 502 || out.Usage.Output != 3 || out.Usage.CacheRead != 7424 || out.Usage.CacheWrite != 0 {
		t.Errorf("Usage = %+v, want {502 3 7424 0}", out.Usage)
	}
}

// TestParseHookModelUsageReasoningFoldsIntoOutput documents the one lossy
// mapping decision: store.Usage has no separate reasoning column, so
// reasoning tokens are added into Output rather than dropped.
func TestParseHookModelUsageReasoningFoldsIntoOutput(t *testing.T) {
	in := `{"session_id":"oc1","cwd":"/x","model":"m","usage":{"input":10,"output":5,"reasoning":7,"cache_read":0,"cache_write":0}}`
	out, err := Adapter{}.ParseHook(assistant.EventModelUsage, strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if out.Usage == nil || out.Usage.Output != 12 {
		t.Errorf("Usage = %+v, want Output 12 (5 output + 7 reasoning)", out.Usage)
	}
}

func TestParseHookModelUsageAbsentUsage(t *testing.T) {
	in := `{"session_id":"oc1","cwd":"/x","model":"big-pickle"}`
	out, err := Adapter{}.ParseHook(assistant.EventModelUsage, strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if out.Usage != nil {
		t.Errorf("expected nil Usage on absent usage, got %+v", out.Usage)
	}
	if out.Model != "big-pickle" {
		t.Errorf("Model = %q, want big-pickle", out.Model)
	}
}

func TestParseHookModelUsageWhitelistOnly(t *testing.T) {
	in := `{
		"session_id": "oc1",
		"cwd": "/x",
		"model": "big-pickle",
		"usage": {"input": 10, "output": 5, "reasoning": 0, "cache_read": 0, "cache_write": 0},
		"prompt": "secret prompt text",
		"parts": [{"type": "text", "text": "sensitive model output"}],
		"message": "leaked message"
	}`
	out, err := Adapter{}.ParseHook(assistant.EventModelUsage, strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if out.Model != "big-pickle" {
		t.Errorf("Model = %q, want big-pickle", out.Model)
	}
	if out.Usage == nil || out.Usage.Input != 10 || out.Usage.Output != 5 {
		t.Errorf("Usage = %+v, want {10 5 0 0}", out.Usage)
	}

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal HookInput: %v", err)
	}
	for _, forbidden := range []string{"sensitive model output", "leaked message", "secret prompt text", "parts"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("HookInput leaked forbidden field %q: %s", forbidden, data)
		}
	}
}

func TestInjectionResponseUnsupported(t *testing.T) {
	if got := (Adapter{}).InjectionResponse("some pack"); got != "" {
		t.Errorf("InjectionResponse = %q, want empty (unsupported)", got)
	}
}

func TestStopResponseUnsupported(t *testing.T) {
	if got := (Adapter{}).StopResponse("distill now"); got != "" {
		t.Errorf("StopResponse = %q, want empty (unsupported)", got)
	}
}
