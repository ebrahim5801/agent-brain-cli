package stats_test

import (
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/stats"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

// TestStatsBackendMatrix runs the aggregation and per-session queries against
// both backends, seeding the same data. It asserts equal aggregate numbers
// (tokens, duration, session counts) and pins the week-bucket label per dialect
// (SQLite %W vs Postgres ISO week may differ at year boundaries — FR-007).
func TestStatsBackendMatrix(t *testing.T) {
	for _, b := range storetest.Both(t) {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "repo_root", Identity: "/p", DisplayName: "proj"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			sid, err := st.EnsureSession("s1", pid, "2026-03-02T10:00:00.000Z", "", "claude-code")
			if err != nil {
				t.Fatal(err)
			}
			if err := st.CloseSession(sid, "2026-03-02T10:05:00.000Z", "normal"); err != nil {
				t.Fatal(err)
			}
			if err := st.SetUsage(sid, "sonnet", store.Usage{Input: 100, Output: 200, CacheRead: 5, CacheWrite: 6}); err != nil {
				t.Fatal(err)
			}

			// Ungrouped aggregate.
			rows, err := stats.Query(st, stats.Filter{})
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("want 1 row, got %d", len(rows))
			}
			r := rows[0]
			if r.Sessions != 1 || r.InputTokens != 100 || r.OutputTokens != 200 ||
				r.CacheReadTokens != 5 || r.CacheWriteTokens != 6 {
				t.Errorf("aggregate wrong: %+v", r)
			}
			if r.DurationSeconds != 300 { // 10:00 -> 10:05
				t.Errorf("duration = %d, want 300", r.DurationSeconds)
			}

			// Daily grouping label (YYYY-MM-DD, identical on both).
			daily, err := stats.Query(st, stats.Filter{Group: "daily"})
			if err != nil {
				t.Fatalf("daily: %v", err)
			}
			if len(daily) != 1 || daily[0].Period != "2026-03-02" {
				t.Errorf("daily period = %q, want 2026-03-02", periodOf(daily))
			}

			// Weekly grouping runs on both; label is dialect-specific (do not
			// assert byte-equality across backends), but must be non-empty and
			// year-prefixed.
			weekly, err := stats.Query(st, stats.Filter{Group: "weekly"})
			if err != nil {
				t.Fatalf("weekly: %v", err)
			}
			if len(weekly) != 1 || len(weekly[0].Period) < 4 || weekly[0].Period[:4] != "2026" {
				t.Errorf("weekly period = %q, want a 2026-prefixed label", periodOf(weekly))
			}

			// Per-session listing.
			sessions, err := stats.Sessions(st, stats.Filter{})
			if err != nil {
				t.Fatalf("sessions: %v", err)
			}
			if len(sessions) != 1 || sessions[0].DurationSeconds != 300 || sessions[0].InputTokens != 100 {
				t.Errorf("session row wrong: %+v", sessions)
			}
			if sessions[0].Open {
				t.Errorf("closed session reported open")
			}
		})
	}
}

func periodOf(rows []stats.Row) string {
	if len(rows) == 0 {
		return "<none>"
	}
	return rows[0].Period
}
