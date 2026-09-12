package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// The SubagentStop payload codex-cli 0.154.0 sends, verbatim. transcript_path
// is the PARENT's rollout and agent_transcript_path the child's — the whole
// reason the child needs a field of its own.
func subagentStopPayload(agentTranscript string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id":             "01a09161-parent",
		"turn_id":                "turn-a",
		"transcript_path":        "/codex/sessions/2026/09/12/rollout-parent.jsonl",
		"agent_transcript_path":  agentTranscript,
		"cwd":                    "/home/dev/private-project",
		"hook_event_name":        "SubagentStop",
		"model":                  "gpt-6-astra",
		"permission_mode":        "bypassPermissions",
		"stop_hook_active":       false,
		"agent_id":               "01a09161-child",
		"agent_type":             "default",
		"last_assistant_message": "CHILD-FINAL-ANSWER: pelican",
	})
	return string(b)
}

// A child rollout is an ordinary rollout JSONL, so the Phase 2 parser reads it
// unchanged — that is what made this phase cheap.
func writeChildRollout(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout-2026-09-12T10-00-00-01a09161-child.jsonl")
	lines := []string{
		`{"timestamp":"2026-09-12T10:00:01.000Z","type":"turn_context","payload":{"turn_id":"c1","model":"gpt-6-mini"}}`,
		`{"timestamp":"2026-09-12T10:00:02.000Z","type":"response_item","payload":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"CHILD-REPLY-BODY: pelican"}]}}`,
		`{"timestamp":"2026-09-12T10:00:09.000Z","type":"token_usage_record","payload":{"turn_id":"c1","turn_token_usage":{"input_tokens":80,"cached_input_tokens":20,"cache_write_input_tokens":4,"output_tokens":9,"reasoning_output_tokens":3,"total_tokens":89},"thread_token_usage":{"input_tokens":80,"cached_input_tokens":20,"cache_write_input_tokens":4,"output_tokens":9,"reasoning_output_tokens":3,"total_tokens":89}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseSubagentStop(t *testing.T, payload string) assistant.HookInput {
	t.Helper()
	in, err := (Adapter{}).ParseHook("subagent-stop", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestParseHookCarriesTheChildRolloutSeparatelyFromTheParent(t *testing.T) {
	in := parseSubagentStop(t, subagentStopPayload("/codex/sessions/2026/09/12/rollout-child.jsonl"))
	if in.TranscriptPath != "/codex/sessions/2026/09/12/rollout-parent.jsonl" {
		t.Errorf("TranscriptPath = %q, want the parent's rollout", in.TranscriptPath)
	}
	if in.Subagent == nil {
		t.Fatal("no subagent ref parsed from a SubagentStop payload")
	}
	if in.Subagent.AgentID != "01a09161-child" {
		t.Errorf("AgentID = %q", in.Subagent.AgentID)
	}
	if in.Subagent.AgentType != "default" {
		t.Errorf("AgentType = %q", in.Subagent.AgentType)
	}
	if in.Subagent.TranscriptPath != "/codex/sessions/2026/09/12/rollout-child.jsonl" {
		t.Errorf("child TranscriptPath = %q", in.Subagent.TranscriptPath)
	}
}

// Every other event must leave the ref nil, or the stop and session-end
// re-scans would re-report whatever the last subagent-stop happened to name.
func TestParseHookLeavesTheSubagentRefNilOnOtherEvents(t *testing.T) {
	in, err := (Adapter{}).ParseHook("stop", strings.NewReader(`{"session_id":"s","cwd":"/w","model":"gpt-6-astra"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Subagent != nil {
		t.Errorf("Subagent = %+v, want nil", in.Subagent)
	}
}

func TestScanSubagentsReadsTheChildRollout(t *testing.T) {
	child := writeChildRollout(t)
	got := (Adapter{}).ScanSubagents(parseSubagentStop(t, subagentStopPayload(child)))
	if len(got) != 1 {
		t.Fatalf("ScanSubagents returned %d runs, want 1", len(got))
	}
	sa := got[0]
	if sa.AgentID != "01a09161-child" || sa.AgentType != "default" {
		t.Errorf("identity = %q/%q", sa.AgentID, sa.AgentType)
	}
	if sa.TranscriptPath != child {
		t.Errorf("TranscriptPath = %q, want the child's rollout", sa.TranscriptPath)
	}
	if sa.Model != "gpt-6-mini" {
		t.Errorf("model = %q, want the child's own model, not the parent's", sa.Model)
	}
	// output is 9, not 12: the 3 reasoning tokens are already inside it. Child
	// rollouts run through the same arithmetic as the parent's.
	want := store.Usage{Input: 80, Output: 9, CacheRead: 20, CacheWrite: 4}
	if sa.Usage != want {
		t.Errorf("usage = %+v, want %+v", sa.Usage, want)
	}
	if sa.StartedAt != "2026-09-12T10:00:01.000Z" || sa.EndedAt != "2026-09-12T10:00:09.000Z" {
		t.Errorf("run bounds = %q..%q", sa.StartedAt, sa.EndedAt)
	}
}

// The child's final answer and its reply text are content. Claude Code's
// scanner keeps them; this one deliberately does not, because Codex's rollout
// parser is structurally closed to message bodies and opening it here would be
// the one hole in that.
func TestScanSubagentsKeepsNoContent(t *testing.T) {
	child := writeChildRollout(t)
	got := (Adapter{}).ScanSubagents(parseSubagentStop(t, subagentStopPayload(child)))
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"CHILD-FINAL-ANSWER", "CHILD-REPLY-BODY", "pelican", "bypassPermissions"} {
		if strings.Contains(string(blob), forbidden) {
			t.Errorf("subagent scan leaked %q: %s", forbidden, blob)
		}
	}
}

// The payload is proof the subagent ran. An unreadable rollout costs its token
// counts, not the whole run.
func TestScanSubagentsRecordsTheRunEvenWithoutItsRollout(t *testing.T) {
	got := (Adapter{}).ScanSubagents(parseSubagentStop(t, subagentStopPayload(filepath.Join(t.TempDir(), "missing.jsonl"))))
	if len(got) != 1 {
		t.Fatalf("ScanSubagents returned %d runs, want the run to survive a missing rollout", len(got))
	}
	if got[0].Usage != (store.Usage{}) {
		t.Errorf("usage = %+v, want zeros rather than invented counts", got[0].Usage)
	}
	if got[0].EndedAt == "" {
		t.Error("EndedAt empty: the run would read as still open")
	}
}

// A payload naming no child yields nothing. This is what makes the stop and
// session-end re-scans harmless for Codex: only SubagentStop carries the ref.
func TestScanSubagentsNeedsARef(t *testing.T) {
	if got := (Adapter{}).ScanSubagents(assistant.HookInput{SessionKey: "s"}); got != nil {
		t.Errorf("ScanSubagents without a ref = %+v, want nil", got)
	}
}

// The citation scan skips lines that cannot hold a citation, which is what
// keeps it inside the 3-second SessionEnd clamp. The saving is worthless if the
// filter drops a real one, so every form has to survive it.
func TestCitationPrefilterKeepsEveryCitationForm(t *testing.T) {
	for _, text := range []string{
		"per memory #12", "memory#12", "see [#12]", "plain #12 here", "team#abc12345",
	} {
		if !hasCitationShape([]byte(text)) {
			t.Errorf("prefilter rejected %q, which holds a citation", text)
		}
	}
	for _, text := range []string{
		"## heading", "#!/bin/sh", "# a comment", "issue # 42", "no hash at all", "#",
	} {
		if hasCitationShape([]byte(text)) {
			t.Errorf("prefilter accepted %q, which holds no citation", text)
		}
	}
}
