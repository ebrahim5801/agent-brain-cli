package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUsageEmptyStore(t *testing.T) {
	st := storetest.SQLite(t)
	s := New(st, t.TempDir(), "test")

	result, _, err := s.usage(context.Background(), nil, usageIn{SessionID: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	report := decodeUsageReport(t, result)
	if report.Session != nil {
		t.Fatalf("session = %+v, want null", report.Session)
	}
	if report.Today.Sessions != 0 || report.Today.InputTokens != 0 || report.Today.OutputTokens != 0 ||
		report.Today.CacheReadTokens != 0 || report.Today.CacheWriteTokens != 0 {
		t.Errorf("today totals = %+v, want zero", report.Today)
	}
	if report.Week.Sessions != 0 || report.Week.InputTokens != 0 || report.Week.OutputTokens != 0 ||
		report.Week.CacheReadTokens != 0 || report.Week.CacheWriteTokens != 0 {
		t.Errorf("week totals = %+v, want zero", report.Week)
	}
	if report.Today.Since != time.Now().UTC().Format("2006-01-02") || report.Week.Since != currentWeekStart(time.Now()) {
		t.Errorf("since dates today=%q week=%q", report.Today.Since, report.Week.Since)
	}
}

func TestUsageSessionIncludesSubagentsAndAggregatePeriods(t *testing.T) {
	st := storetest.SQLite(t)
	dir := t.TempDir()
	s := New(st, dir, "test")
	projectID, err := st.UpsertProject(store.ProjectIdentity{
		Kind: "directory", Identity: dir, DisplayName: "usage-test",
	}, store.Now())
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now().UTC().Truncate(time.Second)
	ended := started.Add(43*time.Second + 900*time.Millisecond)
	startedAt := started.Format(store.TimeLayout)
	endedAt := ended.Format(store.TimeLayout)
	parentID, err := st.EnsureSession("claude-session-123", projectID, startedAt, "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUsage(parentID, "claude-sonnet", store.Usage{Input: 100, Output: 20, CacheRead: 30, CacheWrite: 40}); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseSession(parentID, endedAt, "normal"); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []store.SubSession{
		{ExternalID: "claude-session-123/agent-a", ProjectID: projectID, ParentID: parentID, Assistant: "claude-code", StartedAt: startedAt, EndedAt: endedAt, Usage: store.Usage{Input: 11, Output: 3}},
		{ExternalID: "claude-session-123/agent-b", ProjectID: projectID, ParentID: parentID, Assistant: "claude-code", StartedAt: startedAt, EndedAt: endedAt, Usage: store.Usage{Input: 7, Output: 5}},
	} {
		if _, err := st.UpsertSubSession(sub); err != nil {
			t.Fatal(err)
		}
	}

	result, _, err := s.usage(context.Background(), nil, usageIn{SessionID: "claude-session-123"})
	if err != nil {
		t.Fatal(err)
	}
	report := decodeUsageReport(t, result)
	wantSession := &usageSession{
		ID: "claude-session-123", InputTokens: 100, OutputTokens: 20, DurationSeconds: 43,
		LaneRuns: 2, LaneInputTokens: 18, LaneOutputTokens: 8,
	}
	if report.Session == nil || *report.Session != *wantSession {
		t.Errorf("session = %+v, want %+v", report.Session, wantSession)
	}
	for name, totals := range map[string]usagePeriod{"today": report.Today, "week": report.Week} {
		if totals.Sessions != 1 || totals.InputTokens != 118 || totals.OutputTokens != 28 ||
			totals.CacheReadTokens != 30 || totals.CacheWriteTokens != 40 {
			t.Errorf("%s totals = %+v, want 1 session, 118 input, 28 output, 30 cache-read, 40 cache-write", name, totals)
		}
	}

	if prefixMatches, _, err := s.usage(context.Background(), nil, usageIn{SessionID: "claude-session"}); err != nil {
		t.Fatal(err)
	} else if decodeUsageReport(t, prefixMatches).Session != nil {
		t.Fatal("usage_stats must not prefix-match Claude session IDs")
	}

	for _, sessionID := range []string{"unknown-session", "claude-session"} {
		unknown, _, err := s.usage(context.Background(), nil, usageIn{SessionID: sessionID})
		if err != nil {
			t.Fatal(err)
		}
		unknownReport := decodeUsageReport(t, unknown)
		if unknownReport.Session != nil {
			t.Errorf("session %q = %+v, want null", sessionID, unknownReport.Session)
		}
	}

	var projectCount int
	if err := st.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&projectCount); err != nil {
		t.Fatal(err)
	}
	if projectCount != 1 {
		t.Errorf("project count = %d, usage must not upsert projects", projectCount)
	}
}

func TestUsageMatchesClaudeNativeIDForV2Sessions(t *testing.T) {
	st := storetest.SQLite(t)
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	projectID, err := st.UpsertProject(store.ProjectIdentity{
		Kind: "directory", Identity: dir, DisplayName: "usage-v2-test",
	}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	binding, err := st.EnsureBoundSession(projectID, "claude-code", "native-claude-session", dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(`UPDATE sessions SET external_id = ? WHERE id = ?`, "v2:"+binding.Handle, binding.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUsage(binding.SessionID, "claude-sonnet", store.Usage{Input: 13, Output: 8}); err != nil {
		t.Fatal(err)
	}

	s := New(st, dir, "test")
	result, _, err := s.usage(context.Background(), nil, usageIn{SessionID: "native-claude-session"})
	if err != nil {
		t.Fatal(err)
	}
	report := decodeUsageReport(t, result)
	if report.Session == nil || report.Session.ID != "native-claude-session" ||
		report.Session.InputTokens != 13 || report.Session.OutputTokens != 8 {
		t.Fatalf("native session lookup = %+v, want Claude native id and token totals", report.Session)
	}
}

func TestUsageAvailableWithoutMemoryEntitlementOrConsent(t *testing.T) {
	st := storetest.SQLite(t)
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", t.TempDir())
	s := New(st, t.TempDir(), "test")

	result, _, err := s.usage(context.Background(), nil, usageIn{})
	if err != nil {
		t.Fatalf("usage_stats should not call the memory gate: %v", err)
	}
	if got := string(result.Content[0].(*mcp.TextContent).Text); !strings.Contains(got, `"today"`) {
		t.Fatalf("result = %s, want usage report without memory entitlement or consent", got)
	}
}

func TestUsageUnavailableWithoutStore(t *testing.T) {
	s := New(nil, t.TempDir(), "test")
	result, _, err := s.usage(context.Background(), nil, usageIn{})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Content[0].(*mcp.TextContent).Text; got != `{"available":false}` {
		t.Fatalf("unavailable result = %s, want {\"available\":false}", got)
	}
}

func decodeUsageReport(t *testing.T, result *mcp.CallToolResult) usageReport {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("content blocks = %d, want one", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content block type = %T, want text", result.Content[0])
	}
	var report usageReport
	if err := json.Unmarshal([]byte(text.Text), &report); err != nil {
		t.Fatalf("decode usage result %q: %v", text.Text, err)
	}
	return report
}
