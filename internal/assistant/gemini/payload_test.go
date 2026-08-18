package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

func TestParseHookBase(t *testing.T) {
	in := `{"session_id":"g1","cwd":"/x"}`
	out, err := Adapter{}.ParseHook(assistant.EventSessionStart, strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if out.SessionKey != "g1" {
		t.Errorf("SessionKey = %q, want g1", out.SessionKey)
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

func TestParseHookModelUsage(t *testing.T) {
	in := `{"session_id":"g1","cwd":"/x","llm_request":{"model":"gemini-2.5-pro"},"llm_response":{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":40,"cachedContentTokenCount":25}}}`
	out, err := Adapter{}.ParseHook(assistant.EventModelUsage, strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if out.Model != "gemini-2.5-pro" {
		t.Errorf("Model = %q, want gemini-2.5-pro", out.Model)
	}
	if out.Usage == nil {
		t.Fatalf("expected non-nil Usage")
	}
	if out.Usage.Input != 100 || out.Usage.Output != 40 || out.Usage.CacheRead != 25 || out.Usage.CacheWrite != 0 {
		t.Errorf("Usage = %+v, want {100 40 25 0}", out.Usage)
	}
}

func TestParseHookModelUsageAbsentUsageMetadata(t *testing.T) {
	in := `{"session_id":"g1","cwd":"/x","llm_request":{"model":"gemini-2.5-pro"},"llm_response":{}}`
	out, err := Adapter{}.ParseHook(assistant.EventModelUsage, strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if out.Usage != nil {
		t.Errorf("expected nil Usage on absent usageMetadata, got %+v", out.Usage)
	}
	if out.Model != "gemini-2.5-pro" {
		t.Errorf("Model = %q, want gemini-2.5-pro", out.Model)
	}
}

func TestParseHookModelUsageWhitelistOnly(t *testing.T) {
	in := `{
		"session_id": "g1",
		"cwd": "/x",
		"llm_request": {"model": "gemini-2.5-pro", "prompt": "secret prompt text"},
		"llm_response": {
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "cachedContentTokenCount": 0},
			"candidates": [{"content": {"parts": [{"text": "sensitive model output"}]}}],
			"content": "leaked content",
			"message": "leaked message"
		}
	}`
	out, err := Adapter{}.ParseHook(assistant.EventModelUsage, strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if out.Model != "gemini-2.5-pro" {
		t.Errorf("Model = %q, want gemini-2.5-pro", out.Model)
	}
	if out.Usage == nil || out.Usage.Input != 10 || out.Usage.Output != 5 || out.Usage.CacheRead != 0 {
		t.Errorf("Usage = %+v, want {10 5 0 0}", out.Usage)
	}

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal HookInput: %v", err)
	}
	for _, forbidden := range []string{"sensitive model output", "leaked content", "leaked message", "secret prompt text", "candidates"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("HookInput leaked forbidden field %q: %s", forbidden, data)
		}
	}
}

func TestInjectionResponse(t *testing.T) {
	got := Adapter{}.InjectionResponse("some pack")
	want := `{"hookSpecificOutput":{"additionalContext":"some pack","hookEventName":"SessionStart"}}`
	assertJSONEqual(t, got, want)
}

func TestPromptInjectionResponse(t *testing.T) {
	got := Adapter{}.PromptInjectionResponse("some pack")
	want := `{"hookSpecificOutput":{"additionalContext":"some pack","hookEventName":"BeforeAgent"}}`
	assertJSONEqual(t, got, want)
}

func TestStopResponse(t *testing.T) {
	got := Adapter{}.StopResponse("distill now")
	want := `{"decision":"block","reason":"distill now"}`
	assertJSONEqual(t, got, want)
}

func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got not valid json: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want not valid json: %v", err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("got %s, want %s", got, want)
	}
}
