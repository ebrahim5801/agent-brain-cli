package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

const fixture = "testdata/rollout.jsonl"

// The fixture's two turns, as the parser should read them: turn-a ran on
// gpt-6-astra across two records, turn-b on gpt-6-mini across two more, and
// every usage object in the file is cumulative rather than incremental.
var (
	wantAstra = store.Usage{Input: 300, Output: 30, CacheRead: 120, CacheWrite: 15}
	wantMini  = store.Usage{Input: 200, Output: 27, CacheRead: 60, CacheWrite: 10}
	wantTotal = store.Usage{Input: 500, Output: 57, CacheRead: 180, CacheWrite: 25}
)

func TestParseRolloutAbsoluteTotals(t *testing.T) {
	usage, model, _, ok := ParseRollout(fixture)
	if !ok {
		t.Fatal("ParseRollout(fixture) not ok")
	}
	if usage != wantTotal {
		t.Errorf("usage = %+v, want %+v", usage, wantTotal)
	}
	if model != "gpt-6-astra" {
		t.Errorf("dominant model = %q, want gpt-6-astra", model)
	}
}

// The per-turn totals are cumulative, so the breakdown must take the last
// record of each turn rather than summing every record. Summing turn-a's two
// records would report 400 input tokens for a turn that used 300.
func TestParseRolloutPerModelBreakdownTakesLastRecordPerTurn(t *testing.T) {
	_, _, models, ok := ParseRollout(fixture)
	if !ok {
		t.Fatal("ParseRollout(fixture) not ok")
	}
	want := []store.ModelUsage{
		{Model: "gpt-6-astra", Usage: wantAstra},
		{Model: "gpt-6-mini", Usage: wantMini},
	}
	if len(models) != len(want) {
		t.Fatalf("breakdown = %+v, want %d rows", models, len(want))
	}
	for i := range want {
		if models[i] != want[i] {
			t.Errorf("breakdown[%d] = %+v, want %+v", i, models[i], want[i])
		}
	}
}

// The scalar total comes from thread_token_usage and the breakdown from the
// per-turn records — two independent paths through the file. On a session
// whose whole thread is in one rollout they must agree, which is the cheapest
// check that neither is double-counting.
func TestParseRolloutBreakdownReconcilesWithTotal(t *testing.T) {
	usage, _, models, ok := ParseRollout(fixture)
	if !ok {
		t.Fatal("ParseRollout(fixture) not ok")
	}
	var sum store.Usage
	for _, m := range models {
		sum.Input += m.Usage.Input
		sum.Output += m.Usage.Output
		sum.CacheRead += m.Usage.CacheRead
		sum.CacheWrite += m.Usage.CacheWrite
	}
	if sum != usage {
		t.Errorf("breakdown sums to %+v, session total is %+v", sum, usage)
	}
}

// Codex reports reasoning tokens separately and store.Usage has no column for
// them, so they are folded into Output. The fold is lossy; this pins it so it
// changes deliberately. turn-b's last record is output 20 + reasoning 7.
func TestParseRolloutFoldsReasoningIntoOutput(t *testing.T) {
	_, _, models, ok := ParseRollout(fixture)
	if !ok {
		t.Fatal("ParseRollout(fixture) not ok")
	}
	for _, m := range models {
		if m.Model != "gpt-6-mini" {
			continue
		}
		if m.Usage.Output != 27 {
			t.Errorf("gpt-6-mini output = %d, want 27 (20 output + 7 reasoning)", m.Usage.Output)
		}
		return
	}
	t.Fatal("gpt-6-mini missing from the breakdown")
}

func TestParseRolloutToleratesMalformedLines(t *testing.T) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	malformed := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if json.Unmarshal([]byte(line), &struct{}{}) != nil {
			malformed++
		}
	}
	if malformed == 0 {
		t.Fatal("fixture has no malformed line; the tolerance it guards is untested")
	}
	usage, _, _, ok := ParseRollout(fixture)
	if !ok || usage != wantTotal {
		t.Errorf("a malformed line changed the parse: ok=%v usage=%+v", ok, usage)
	}
}

func TestParseRolloutHonestAbsence(t *testing.T) {
	dir := t.TempDir()
	noCounts := filepath.Join(dir, "no-counts.jsonl")
	if err := os.WriteFile(noCounts, []byte(`{"type":"event_msg","payload":{"type":"agent_message"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "missing.jsonl"), noCounts} {
		usage, model, models, ok := ParseRollout(path)
		if ok {
			t.Errorf("ParseRollout(%s) ok, want honest absence", filepath.Base(path))
		}
		if usage != (store.Usage{}) || model != "" || models != nil {
			t.Errorf("ParseRollout(%s) = %+v %q %+v, want zero values", filepath.Base(path), usage, model, models)
		}
	}
}

// The rollout carries the whole conversation, every tool call with its output,
// and the entire system prompt. None of it may survive the parse: the structs
// simply have nowhere to put it.
func TestParseRolloutWhitelistOnly(t *testing.T) {
	usage, model, models, ok := ParseRollout(fixture)
	if !ok {
		t.Fatal("ParseRollout(fixture) not ok")
	}
	ids, handles, ok := ScanMemoryCitations(fixture)
	if !ok {
		t.Fatal("ScanMemoryCitations(fixture) not ok")
	}
	blob, err := json.Marshal(struct {
		Usage   store.Usage
		Model   string
		Models  []store.ModelUsage
		IDs     []int64
		Handles []string
	}{usage, model, models, ids, handles})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"SYSTEM-PROMPT-BODY",
		"USER-PROMPT-BODY",
		"DEVELOPER-BODY",
		"TOOL-OUTPUT-BODY",
		"REASONING-BODY",
		"hunter2",
		".env",
		"private-project",
		"example.com",
	} {
		if strings.Contains(string(blob), forbidden) {
			t.Errorf("parse result leaked %q: %s", forbidden, blob)
		}
	}
}

func TestScanMemoryCitationsReadsAssistantRepliesOnly(t *testing.T) {
	ids, handles, ok := ScanMemoryCitations(fixture)
	if !ok {
		t.Fatal("ScanMemoryCitations(fixture) not ok")
	}
	wantIDs := []int64{12, 34}
	if len(ids) != len(wantIDs) {
		t.Fatalf("ids = %v, want %v", ids, wantIDs)
	}
	for i := range wantIDs {
		if ids[i] != wantIDs[i] {
			t.Errorf("ids = %v, want %v", ids, wantIDs)
			break
		}
	}
	if len(handles) != 1 || handles[0] != "abc12345" {
		t.Errorf("handles = %v, want [abc12345]", handles)
	}
	// #999 is in the user's prompt and #777 in a developer message. Neither is
	// the assistant recalling anything, so neither is a citation.
	for _, id := range ids {
		if id == 999 || id == 777 {
			t.Errorf("id %d came from a non-assistant message", id)
		}
	}
}

func TestScanMemoryCitationsMissingFile(t *testing.T) {
	ids, handles, ok := ScanMemoryCitations(filepath.Join(t.TempDir(), "missing.jsonl"))
	if ok || ids != nil || handles != nil {
		t.Errorf("ScanMemoryCitations(missing) = %v %v %v, want nil nil false", ids, handles, ok)
	}
}

func hookInputWithPath(path string) assistant.HookInput {
	return assistant.HookInput{SessionKey: "01a09148", TranscriptPath: path}
}

func TestBackfillUsageAndScanCitationsNeedATranscriptPath(t *testing.T) {
	if _, _, _, ok := (Adapter{}).BackfillUsage(hookInputWithPath("")); ok {
		t.Error("BackfillUsage with no transcript path reported success")
	}
	if _, _, ok := (Adapter{}).ScanCitations(hookInputWithPath("")); ok {
		t.Error("ScanCitations with no transcript path reported success")
	}
	if _, _, _, ok := (Adapter{}).BackfillUsage(hookInputWithPath(fixture)); !ok {
		t.Error("BackfillUsage with the fixture path failed")
	}
	if _, _, ok := (Adapter{}).ScanCitations(hookInputWithPath(fixture)); !ok {
		t.Error("ScanCitations with the fixture path failed")
	}
}

// BenchmarkParseRollout is the SessionEnd budget gate. Codex clamps that hook
// to 3 seconds, so a long session's rollout has to parse in well under it;
// 50 MB across 200 turns is a deliberately pessimistic stand-in for one.
func BenchmarkParseRollout(b *testing.B) {
	path := filepath.Join(b.TempDir(), "big.jsonl")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	// response_item lines dominate a real rollout's bytes, which is what the
	// parser's prefix filter exists to skip.
	noise := strings.Repeat("x", 4096)
	const turns = 200
	var written int64
	for turn := 0; turn < turns; turn++ {
		fmt.Fprintf(f, `{"type":"turn_context","payload":{"turn_id":"turn-%d","model":"gpt-6-astra"}}`+"\n", turn)
		for written < int64(turn+1)*(50<<20)/turns {
			n, err := fmt.Fprintf(f, `{"type":"response_item","payload":{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":%q}]}}`+"\n", noise)
			if err != nil {
				b.Fatal(err)
			}
			written += int64(n)
		}
		n, err := fmt.Fprintf(f, `{"type":"token_usage_record","payload":{"turn_id":"turn-%d","turn_token_usage":{"input_tokens":10,"output_tokens":1},"thread_token_usage":{"input_tokens":%d,"output_tokens":%d}}}`+"\n", turn, (turn+1)*10, turn+1)
		if err != nil {
			b.Fatal(err)
		}
		written += int64(n)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, ok := ParseRollout(path); !ok {
			b.Fatal("ParseRollout failed")
		}
	}
}
