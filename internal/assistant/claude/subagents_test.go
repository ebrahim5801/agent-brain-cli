package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/wire"
)

const agentJSONL = `{"parentUuid":null,"isSidechain":true,"agentId":"abc123","type":"user","message":{"role":"user","content":"Review the client code for bugs"},"timestamp":"2026-07-15T14:18:31.580Z"}
{"type":"assistant","timestamp":"2026-07-15T14:20:00.000Z","requestId":"req_1","message":{"id":"msg_1","model":"claude-fable-5","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"Working on it."}],"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":30,"cache_read_input_tokens":40}}}
{"type":"assistant","timestamp":"2026-07-15T14:20:00.000Z","requestId":"req_1","message":{"id":"msg_1","model":"claude-fable-5","content":[{"type":"text","text":"Working on it."}],"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":30,"cache_read_input_tokens":40}}}
{"type":"assistant","timestamp":"2026-07-15T14:35:13.122Z","requestId":"req_2","message":{"id":"msg_2","model":"claude-fable-5","content":[{"type":"text","text":"Found 2 bugs: A and B."}],"usage":{"input_tokens":5,"output_tokens":15,"cache_creation_input_tokens":0,"cache_read_input_tokens":100}}}
`

func writeSubagentFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	main := filepath.Join(dir, "session-1.jsonl")
	if err := os.WriteFile(main, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "session-1", "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-abc123.jsonl"), []byte(agentJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := `{"agentType":"general-purpose","description":"Review client code","toolUseId":"toolu_1","spawnDepth":1}`
	if err := os.WriteFile(filepath.Join(sub, "agent-abc123.meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	return main
}

func TestScanSubagents(t *testing.T) {
	main := writeSubagentFixture(t)
	agents := ScanSubagents(main)
	if len(agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(agents))
	}
	sa := agents[0]
	if sa.AgentID != "abc123" {
		t.Errorf("agent id = %q", sa.AgentID)
	}
	if sa.AgentType != "general-purpose" || sa.Description != "Review client code" {
		t.Errorf("meta = %q / %q", sa.AgentType, sa.Description)
	}
	// Duplicate usage lines for msg_1 must count once: 10+5, 20+15, 40+100, 30+0.
	if sa.Usage.Input != 15 || sa.Usage.Output != 35 || sa.Usage.CacheRead != 140 || sa.Usage.CacheWrite != 30 {
		t.Errorf("usage = %+v", sa.Usage)
	}
	if sa.Model != "claude-fable-5" {
		t.Errorf("model = %q", sa.Model)
	}
	if sa.StartedAt != "2026-07-15T14:18:31.580Z" || sa.EndedAt != "2026-07-15T14:35:13.122Z" {
		t.Errorf("times = %q .. %q", sa.StartedAt, sa.EndedAt)
	}
	if sa.Prompt != "Review the client code for bugs" {
		t.Errorf("prompt = %q", sa.Prompt)
	}
	if sa.FinalReport != "Found 2 bugs: A and B." {
		t.Errorf("final report = %q", sa.FinalReport)
	}
}

func TestScanSubagentsMissingDirAndMeta(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(main, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ScanSubagents(main); got != nil {
		t.Errorf("no subagents dir: got %v", got)
	}

	// An agent transcript without its meta.json still yields usage.
	sub := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-x.jsonl"), []byte(agentJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	agents := ScanSubagents(main)
	if len(agents) != 1 || agents[0].AgentType != "" || agents[0].Usage.Input != 15 {
		t.Errorf("meta-less scan = %+v", agents)
	}
}

func TestScanSubagentsHostileMeta(t *testing.T) {
	main := writeSubagentFixture(t)
	sub := filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents")
	meta := `{"agentType":"evil\u001b]0;x\u0007` + strings.Repeat("A", 100) + `","description":"` + strings.Repeat("d", 5000) + `"}`
	if err := os.WriteFile(filepath.Join(sub, "agent-abc123.meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	agents := ScanSubagents(main)
	if len(agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(agents))
	}
	sa := agents[0]
	if !wire.ValidAgentType(sa.AgentType) {
		t.Errorf("agent type not sanitized to a valid slug: %q", sa.AgentType)
	}
	if strings.ContainsAny(sa.AgentType, "\x1b\x07") {
		t.Errorf("agent type keeps control bytes: %q", sa.AgentType)
	}
	if len(sa.Description) != maxReportBytes {
		t.Errorf("description len = %d, want capped at %d", len(sa.Description), maxReportBytes)
	}
}

func TestScanSubagentsOversizedMeta(t *testing.T) {
	main := writeSubagentFixture(t)
	sub := filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents")
	huge := `{"agentType":"legit","description":"` + strings.Repeat("d", maxMetaBytes) + `"}`
	if err := os.WriteFile(filepath.Join(sub, "agent-abc123.meta.json"), []byte(huge), 0o644); err != nil {
		t.Fatal(err)
	}
	agents := ScanSubagents(main)
	if len(agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(agents))
	}
	// The capped read truncates the JSON, so the meta is ignored rather than
	// a multi-megabyte value being kept.
	if agents[0].AgentType != "" || agents[0].Description != "" {
		t.Errorf("oversized meta parsed: %q / %d bytes", agents[0].AgentType, len(agents[0].Description))
	}
}

func TestAdapterScanSubagentsSummary(t *testing.T) {
	main := writeSubagentFixture(t)
	agents := Adapter{}.ScanSubagents(assistant.HookInput{TranscriptPath: main})
	if len(agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(agents))
	}
	if want := "Review client code\n\nFound 2 bugs: A and B."; agents[0].Summary != want {
		t.Errorf("summary = %q, want %q", agents[0].Summary, want)
	}
	if agents[0].Prompt != "Review the client code for bugs" {
		t.Errorf("prompt = %q", agents[0].Prompt)
	}
	if !strings.HasSuffix(agents[0].TranscriptPath, "agent-abc123.jsonl") {
		t.Errorf("transcript = %q", agents[0].TranscriptPath)
	}
}
