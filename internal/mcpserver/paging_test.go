package mcpserver

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/entitlement"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newListTestServer(t *testing.T, personalCount int) (*Server, *store.Store, int64) {
	t.Helper()
	cfgDir := t.TempDir()
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", cfgDir)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := config.Update(func(c *config.Config) error {
		c.Entitlement = &config.Entitlement{Tier: entitlement.TierPro, VerifiedAt: now}
		c.Consent.Memory = &config.Consent{GrantedAt: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	project := attribution.Resolve(dir)
	projectID, err := st.UpsertProject(store.ProjectIdentity{Kind: project.Kind, Identity: project.Identity, DisplayName: project.DisplayName}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	priorities := []string{store.MemoryPriorityCritical, store.MemoryPriorityNormal, store.MemoryPriorityBackground}
	for i := 1; i <= personalCount; i++ {
		kind := "fact"
		if i%2 == 0 {
			kind = "decision"
		}
		at := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format(store.TimeLayout)
		_, err := st.InsertMemory(store.NewMemory{
			ProjectID: projectID, Content: fmt.Sprintf("personal-%03d", i), Kind: kind, Origin: "auto",
			Priority: priorities[(i-1)%len(priorities)],
		}, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	return New(st, dir, "test"), st, projectID
}

func listText(t *testing.T, s *Server, in listIn) string {
	t.Helper()
	result, _, err := s.list(context.Background(), &mcp.CallToolRequest{}, in)
	if err != nil {
		t.Fatalf("memory_list: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("content count = %d, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T, want text", result.Content[0])
	}
	return text.Text
}

func addListTestTeam(t *testing.T, st *store.Store, projectID int64, n int) {
	t.Helper()
	rows := make([]store.TeamMemoryRow, 0, n)
	for i := 1; i <= n; i++ {
		rows = append(rows, store.TeamMemoryRow{
			UID: fmt.Sprintf("team%08d", i), ProjectID: projectID, Author: "teammate@example.com",
			Content: fmt.Sprintf("team-entry-%03d", i), Kind: "decision", Origin: "auto",
			Priority: store.MemoryPriorityCritical, Status: "active",
			CapturedAt: time.Date(2026, 2, 1, 0, 0, i, 0, time.UTC).Format(store.TimeLayout), UpdatedAt: store.Now(),
		})
	}
	if err := st.ApplyPull(projectID, rows); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryListDefaultPageAndNextFooter(t *testing.T) {
	s, _, _ := newListTestServer(t, 60)
	text := listText(t, s, listIn{})
	if got := strings.Count(text, "[#"); got != 50 {
		t.Fatalf("rendered personal entries = %d, want 50", got)
	}
	if !strings.Contains(text, "\n\nShowing 1-50 of 60. Next page: offset=50.") {
		t.Fatalf("missing exact first-page footer in tail:\n%s", text[len(text)-100:])
	}
	if !strings.HasPrefix(text, "[#") || !strings.Contains(text, "personal-060") || !strings.Contains(text, "personal-011") {
		t.Fatalf("first page did not preserve newest-first order:\n%s", text[:200])
	}
}

func TestMemoryListOffsetLastPageAndPastEnd(t *testing.T) {
	s, _, _ := newListTestServer(t, 60)
	second := listText(t, s, listIn{Limit: 10, Offset: 10})
	if got := strings.Count(second, "[#"); got != 10 || !strings.Contains(second, "personal-050") || !strings.Contains(second, "personal-041") {
		t.Fatalf("offset page is incorrect: count=%d\n%s", got, second)
	}
	last := listText(t, s, listIn{Offset: 50})
	if got := strings.Count(last, "[#"); got != 10 || !strings.HasSuffix(last, "\n\nShowing 51-60 of 60.") {
		t.Fatalf("last page is incorrect: count=%d, tail=%q", got, last[len(last)-min(len(last), 80):])
	}
	past := listText(t, s, listIn{Limit: 10, Offset: 60})
	if past != "no memories on this page (total 60)" {
		t.Errorf("past-end response = %q", past)
	}
}

func TestMemoryListNoFooterWhenEverythingFitsAndEmptyProjectText(t *testing.T) {
	small, _, _ := newListTestServer(t, 3)
	if text := listText(t, small, listIn{}); strings.Contains(text, "Showing ") {
		t.Errorf("small complete page unexpectedly has footer: %q", text)
	}
	empty, _, _ := newListTestServer(t, 0)
	if text := listText(t, empty, listIn{}); text != "no memories for this project" {
		t.Errorf("empty project text = %q", text)
	}
}

func TestMemoryListLimitClampsTo200AndRejectsNegative(t *testing.T) {
	s, _, _ := newListTestServer(t, 240)
	text := listText(t, s, listIn{Limit: 1000})
	if got := strings.Count(text, "[#"); got != 200 {
		t.Errorf("clamped page count = %d, want 200", got)
	}
	if !strings.HasSuffix(text, "\n\nShowing 1-200 of 240. Next page: offset=200.") {
		t.Errorf("clamped footer missing: %q", text[len(text)-min(len(text), 90):])
	}
	if _, _, err := s.list(context.Background(), &mcp.CallToolRequest{}, listIn{Limit: -1}); err == nil || !strings.Contains(err.Error(), "limit must be >= 0") {
		t.Errorf("negative limit error = %v", err)
	}
	if _, _, err := s.list(context.Background(), &mcp.CallToolRequest{}, listIn{Offset: -1}); err == nil || !strings.Contains(err.Error(), "offset must be >= 0") {
		t.Errorf("negative offset error = %v", err)
	}
}

func TestMemoryListPriorityAndKindFilters(t *testing.T) {
	s, st, projectID := newListTestServer(t, 12)
	addListTestTeam(t, st, projectID, 2)
	critical := listText(t, s, listIn{Priority: "critical", Limit: 10})
	if got := strings.Count(critical, "[#"); got != 4 {
		t.Errorf("critical personal count = %d, want 4", got)
	}
	if strings.Contains(critical, "[team#") || !strings.Contains(critical, "personal-010") || strings.Contains(critical, "Showing ") {
		t.Errorf("priority filter included team rows, missed personal matches, or added an unnecessary footer:\n%s", critical)
	}
	decisions := listText(t, s, listIn{Kind: "decision", Limit: 20})
	if got := strings.Count(decisions, "[#"); got != 6 || strings.Contains(decisions, "[team#") {
		t.Errorf("kind filter count = %d or included team rows:\n%s", got, decisions)
	}
	if _, _, err := s.list(context.Background(), &mcp.CallToolRequest{}, listIn{Priority: "urgent"}); err == nil {
		t.Error("invalid priority was accepted")
	}
	if _, _, err := s.list(context.Background(), &mcp.CallToolRequest{}, listIn{Kind: "other"}); err == nil {
		t.Error("invalid kind was accepted")
	}
}

func TestMemoryListPagesCombinedPersonalThenTeamSequence(t *testing.T) {
	s, st, projectID := newListTestServer(t, 3)
	addListTestTeam(t, st, projectID, 3)
	first := listText(t, s, listIn{Limit: 4})
	lines := strings.Split(first, "\n")
	if len(lines) != 6 || !strings.HasPrefix(lines[0], "[#") || !strings.HasPrefix(lines[2], "[#") || !strings.HasPrefix(lines[3], "[team#") {
		t.Fatalf("first page should contain 3 personal then one team entry (with footer):\n%s", first)
	}
	second := listText(t, s, listIn{Limit: 4, Offset: 4})
	if !strings.HasPrefix(second, "[team#") || strings.Count(second, "[team#") != 2 || !strings.HasSuffix(second, "\n\nShowing 5-6 of 6.") {
		t.Fatalf("second page should continue through team entries:\n%s", second)
	}
}

func TestMemoryListIncludeSupersededStillWorks(t *testing.T) {
	s, st, projectID := newListTestServer(t, 2)
	entries, err := st.ListMemories(projectID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SupersedeMemory(entries[1].ID, entries[0].ID, projectID, store.Now()); err != nil {
		t.Fatal(err)
	}
	active := listText(t, s, listIn{})
	all := listText(t, s, listIn{IncludeSuperseded: true})
	if strings.Contains(active, "personal-001") || !strings.Contains(all, "personal-001") {
		t.Errorf("include_superseded did not preserve list behavior; active=%q all=%q", active, all)
	}
}
