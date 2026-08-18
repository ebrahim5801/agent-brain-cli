package claude

import (
	"path/filepath"
	"testing"
)

// T012: transcript usage extraction — exact sums, dedupe by message ID,
// dominant model, no content retention (the returned values are numbers and a
// model ID only, which this test's assertions make structural).
func TestParseTranscript(t *testing.T) {
	usage, model, models, err := ParseTranscript(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Input != 310 {
		t.Errorf("input = %d, want 310 (duplicate msg_01 line must not double-count)", usage.Input)
	}
	if usage.Output != 135 {
		t.Errorf("output = %d, want 135", usage.Output)
	}
	if usage.CacheWrite != 15 {
		t.Errorf("cache write = %d, want 15", usage.CacheWrite)
	}
	if usage.CacheRead != 60 {
		t.Errorf("cache read = %d, want 60", usage.CacheRead)
	}
	if model != "claude-fable-5" {
		t.Errorf("dominant model = %q, want claude-fable-5", model)
	}

	// The session used two models; the breakdown carries one entry each,
	// heaviest first, and each model's tokens sum to the aggregate above.
	if len(models) != 2 {
		t.Fatalf("model breakdown has %d entries, want 2: %+v", len(models), models)
	}
	if models[0].Model != "claude-fable-5" {
		t.Errorf("breakdown[0] model = %q, want claude-fable-5 (heaviest first)", models[0].Model)
	}
	if got := models[0].Usage; got.Input != 300 || got.Output != 130 || got.CacheWrite != 15 || got.CacheRead != 60 {
		t.Errorf("fable-5 usage = %+v, want {300 130 60 15}", got)
	}
	if models[1].Model != "claude-haiku-4-5" {
		t.Errorf("breakdown[1] model = %q, want claude-haiku-4-5", models[1].Model)
	}
	if got := models[1].Usage; got.Input != 10 || got.Output != 5 || got.CacheWrite != 0 || got.CacheRead != 0 {
		t.Errorf("haiku usage = %+v, want {10 5 0 0}", got)
	}
	var sumIn, sumOut int64
	for _, m := range models {
		sumIn += m.Usage.Input
		sumOut += m.Usage.Output
	}
	if sumIn != usage.Input || sumOut != usage.Output {
		t.Errorf("breakdown sums (%d in / %d out) must equal aggregate (%d / %d)", sumIn, sumOut, usage.Input, usage.Output)
	}
}

func TestParseTranscriptMissingFile(t *testing.T) {
	if _, _, _, err := ParseTranscript(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("want error for missing transcript")
	}
}
