package copilot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func TestParseEventsFixture(t *testing.T) {
	usage, model, ok := ParseEvents(filepath.Join("testdata", "events.jsonl"))
	if !ok {
		t.Fatal("want ok=true for fixture with a session.shutdown event")
	}
	if usage.Input != 1234 {
		t.Errorf("Input = %d, want 1234", usage.Input)
	}
	if usage.Output != 567 {
		t.Errorf("Output = %d, want 567", usage.Output)
	}
	if usage.CacheRead != 89 {
		t.Errorf("CacheRead = %d, want 89", usage.CacheRead)
	}
	if usage.CacheWrite != 0 {
		t.Errorf("CacheWrite = %d, want 0", usage.CacheWrite)
	}
	if model != "gpt-5-copilot" {
		t.Errorf("model = %q, want gpt-5-copilot", model)
	}
}

func TestParseEventsNoShutdownEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := "{\"type\":\"session.start\"}\n{\"type\":\"tool.call\",\"toolName\":\"read_file\"}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	usage, model, ok := ParseEvents(path)
	if ok {
		t.Errorf("want ok=false with no shutdown event, got usage=%+v model=%q", usage, model)
	}
	if usage != (store.Usage{}) {
		t.Errorf("usage = %+v, want zero value", usage)
	}
}

func TestParseEventsMalformedLinesTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := "{not json\n" +
		"{\"type\":\"session.shutdown\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"cached_tokens\":1},\"model\":\"m1\"}\n" +
		"also not json{{{\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	usage, model, ok := ParseEvents(path)
	if !ok {
		t.Fatal("want ok=true; the one well-formed shutdown line should still parse")
	}
	if usage.Input != 10 || usage.Output != 5 || usage.CacheRead != 1 {
		t.Errorf("usage = %+v", usage)
	}
	if model != "m1" {
		t.Errorf("model = %q, want m1", model)
	}
}

func TestParseEventsMissingFile(t *testing.T) {
	_, _, ok := ParseEvents(filepath.Join(t.TempDir(), "nope.jsonl"))
	if ok {
		t.Error("want ok=false for missing file")
	}
}
