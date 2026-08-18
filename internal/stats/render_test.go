package stats_test

import (
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/stats"
)

func TestRenderSessionPrompts(t *testing.T) {
	rows := []stats.SessionRow{
		{ID: "parent-uid", Project: "proj", StartedAt: "2026-07-17T15:29:00.000Z"},
		{ID: "child-uid", Project: "proj", StartedAt: "2026-07-17T15:30:00.000Z",
			AgentType: "general-purpose", ParentID: "parent-uid",
			Prompt: "line one\nline two\x1b[31m end"},
	}
	var b strings.Builder
	stats.RenderSessionPrompts(&b, rows)
	out := b.String()

	if !strings.Contains(out, "child-uid  general-purpose  2026-07-17 15:30") {
		t.Errorf("missing sub-session header, got:\n%s", out)
	}
	if !strings.Contains(out, "line one\nline two") {
		t.Errorf("newlines not preserved, got:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("ANSI escape not neutralized, got: %q", out)
	}
	if strings.Contains(out, "parent-uid  ") {
		t.Errorf("parent row without prompt rendered a block, got:\n%s", out)
	}
}

func TestRenderSessionPromptsEmpty(t *testing.T) {
	rows := []stats.SessionRow{{ID: "parent-uid", Project: "proj", StartedAt: "2026-07-17T15:29:00.000Z"}}
	var b strings.Builder
	stats.RenderSessionPrompts(&b, rows)
	if !strings.Contains(b.String(), "No sub-session prompts recorded") {
		t.Errorf("missing empty notice, got: %q", b.String())
	}
}

func TestRenderSessionsLongSummaryWrapsInFull(t *testing.T) {
	words := make([]string, 40)
	for i := range words {
		words[i] = "word"
	}
	longSummary := strings.Join(words, " ")
	rows := []stats.SessionRow{
		{ID: "sess-1", Project: "proj", StartedAt: "2026-07-17T15:29:00.000Z",
			InputTokens: 100, OutputTokens: 200, Summary: longSummary},
	}
	var b strings.Builder
	stats.RenderSessions(&b, rows, "")
	out := b.String()

	if strings.Contains(out, "...") {
		t.Errorf("summary was truncated, got:\n%s", out)
	}
	for _, w := range strings.Fields(longSummary) {
		if !strings.Contains(out, w) {
			t.Errorf("summary word %q missing from output:\n%s", w, out)
		}
	}
	if strings.Count(out, "word") != len(words) {
		t.Errorf("expected all %d words present exactly once, got:\n%s", len(words), out)
	}
}

func TestRenderSessionsEmptySummaryNoExtraLine(t *testing.T) {
	rows := []stats.SessionRow{
		{ID: "sess-1", Project: "proj", StartedAt: "2026-07-17T15:29:00.000Z",
			InputTokens: 100, OutputTokens: 200, Summary: ""},
	}
	var b strings.Builder
	stats.RenderSessions(&b, rows, "")
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected exactly header + 1 row line for empty summary, got %d lines:\n%s", len(lines), b.String())
	}
}

func TestRenderSessionsColumnAlignmentUnchanged(t *testing.T) {
	rows := []stats.SessionRow{
		{ID: "sess-1", Project: "proj", StartedAt: "2026-07-17T15:29:00.000Z",
			InputTokens: 100, OutputTokens: 200, Summary: ""},
	}
	var b strings.Builder
	stats.RenderSessions(&b, rows, "")
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected header and data line, got:\n%s", b.String())
	}
	header := lines[0]
	data := lines[1]
	if !strings.HasPrefix(header, "SESSION") {
		t.Errorf("unexpected header: %q", header)
	}
	if strings.Contains(header, "SUMMARY") {
		t.Errorf("SUMMARY column should be removed from header: %q", header)
	}
	// The tabwriter aligns columns by start offset; PROJECT/TOKENS columns
	// in the header must start at the same rune offset as their data below.
	for _, pair := range [][2]string{
		{"PROJECT", "proj"},
		{"TOKENS IN", "100"},
		{"TOKENS OUT", "200"},
	} {
		headerIdx := strings.Index(header, pair[0])
		dataIdx := strings.Index(data, pair[1])
		if headerIdx == -1 || dataIdx == -1 || headerIdx != dataIdx {
			t.Errorf("column %q not aligned with %q: header offset %d, data offset %d\nheader: %q\ndata: %q",
				pair[0], pair[1], headerIdx, dataIdx, header, data)
		}
	}
}

func TestRenderSessionsSubagentSummaryRendersUnderRow(t *testing.T) {
	rows := []stats.SessionRow{
		{ID: "parent-uid", Project: "proj", StartedAt: "2026-07-17T15:29:00.000Z",
			InputTokens: 10, OutputTokens: 20, Summary: "parent summary text"},
		{ID: "child-uid", Project: "proj", StartedAt: "2026-07-17T15:30:00.000Z",
			AgentType: "general-purpose", ParentID: "parent-uid",
			InputTokens: 5, OutputTokens: 15, Summary: "subagent did the research and reported back"},
	}
	var b strings.Builder
	stats.RenderSessions(&b, rows, "")
	out := b.String()

	childIdx := strings.Index(out, "child-uid")
	if childIdx == -1 {
		t.Fatalf("child row missing from output:\n%s", out)
	}
	summaryIdx := strings.Index(out, "subagent did the research and reported back")
	if summaryIdx == -1 {
		t.Fatalf("subagent summary missing from output:\n%s", out)
	}
	if summaryIdx < childIdx {
		t.Errorf("subagent summary should appear after its row, got:\n%s", out)
	}
}
