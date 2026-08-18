package store_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

// TestBackendMatrix runs a representative battery of store operations against
// every available backend (SQLite always; Postgres when AGENT_BRAIN_TEST_PG_DSN
// is set), asserting identical behavior. It covers the paths where dialect
// divergence lives: RETURNING id, ON CONFLICT upserts, the superseded_by
// self-FK, and partial-index queries.
func TestBackendMatrix(t *testing.T) {
	for _, b := range storetest.Both(t) {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store

			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "repo_root", Identity: "/x", DisplayName: "x"}, store.Now())
			if err != nil {
				t.Fatalf("upsert project: %v", err)
			}
			// ON CONFLICT upsert: same identity, new display name refreshes.
			pid2, err := st.UpsertProject(store.ProjectIdentity{Kind: "repo_root", Identity: "/x", DisplayName: "x2"}, store.Now())
			if err != nil || pid2 != pid {
				t.Fatalf("upsert conflict: id %d->%d err %v", pid, pid2, err)
			}

			sid, err := st.EnsureSession("ext-1", pid, store.Now(), "", "claude-code")
			if err != nil {
				t.Fatalf("ensure session: %v", err)
			}
			if err := st.AppendEvent(sid, "prompt", "hi", store.Now()); err != nil {
				t.Fatalf("append event: %v", err)
			}
			if err := st.SetUsage(sid, "sonnet", store.Usage{Input: 10, Output: 20}); err != nil {
				t.Fatalf("set usage: %v", err)
			}

			// InsertMemory uses RETURNING id — must yield a real positive id on both.
			m1, err := st.InsertMemory(store.NewMemory{ProjectID: pid, SessionID: sid, Content: "first", Kind: "fact", Origin: "distilled"}, store.Now())
			if err != nil || m1 <= 0 {
				t.Fatalf("insert memory: id %d err %v", m1, err)
			}
			m2, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "second", Kind: "fact", Origin: "distilled"}, store.Now())
			if err != nil || m2 <= 0 || m2 == m1 {
				t.Fatalf("insert memory 2: id %d err %v", m2, err)
			}

			// superseded_by lifecycle.
			if err := st.SupersedeMemory(m1, m2, pid, store.Now()); err != nil {
				t.Fatalf("supersede: %v", err)
			}
			active, err := st.ListMemories(pid, false)
			if err != nil {
				t.Fatalf("list active: %v", err)
			}
			if len(active) != 1 || active[0].ID != m2 {
				t.Fatalf("after supersede want [%d] active, got %+v", m2, active)
			}
			got, err := st.GetMemory(m1)
			if err != nil {
				t.Fatalf("get superseded: %v", err)
			}
			if got.Status != store.MemorySuperseded || !got.SupersededBy.Valid || got.SupersededBy.Int64 != m2 {
				t.Fatalf("superseded row wrong: %+v", got)
			}
			if err := st.RestoreMemory(m1, store.Now()); err != nil {
				t.Fatalf("restore: %v", err)
			}
			if all, _ := st.ListMemories(pid, false); len(all) != 2 {
				t.Fatalf("after restore want 2 active, got %d", len(all))
			}

			// Team-memory cache: ApplyPull ON CONFLICT + partial-index list.
			rows := []store.TeamMemoryRow{
				{UID: "team-aaaa", ProjectID: pid, Author: "a@x", Content: "team fact", Kind: "fact", Origin: "distilled", Status: "active", CapturedAt: store.Now(), UpdatedAt: store.Now()},
			}
			if err := st.ApplyPull(pid, rows); err != nil {
				t.Fatalf("apply pull: %v", err)
			}
			// Upsert same uid (conflict path).
			rows[0].Content = "team fact v2"
			if err := st.ApplyPull(pid, rows); err != nil {
				t.Fatalf("apply pull conflict: %v", err)
			}
			team, err := st.ListTeamMemories(pid)
			if err != nil {
				t.Fatalf("list team: %v", err)
			}
			if len(team) != 1 || team[0].Content != "team fact v2" {
				t.Fatalf("team upsert wrong: %+v", team)
			}
			uid, err := st.ResolveTeamHandle(pid, "team-a")
			if err != nil || uid != "team-aaaa" {
				t.Fatalf("resolve handle: %q err %v", uid, err)
			}

			// Pull cursor round-trip (ON CONFLICT DO UPDATE).
			if err := st.SetPullCursor(pid, "cur-1"); err != nil {
				t.Fatalf("set cursor: %v", err)
			}
			if err := st.SetPullCursor(pid, "cur-2"); err != nil {
				t.Fatalf("update cursor: %v", err)
			}
			if c, _ := st.PullCursor(pid); c != "cur-2" {
				t.Fatalf("cursor = %q want cur-2", c)
			}

			// Share queue via partial index (team_uid set, shared_at null).
			shareID, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "shareable", Kind: "fact", Origin: "distilled", ShareLive: true}, store.Now())
			if err != nil {
				t.Fatalf("insert shareable: %v", err)
			}
			pending, err := st.PendingContributions(pid)
			if err != nil {
				t.Fatalf("pending: %v", err)
			}
			if len(pending) != 1 {
				t.Fatalf("want 1 pending contribution, got %d", len(pending))
			}
			if err := st.MarkShared(pending[0].UID, store.Now()); err != nil {
				t.Fatalf("mark shared: %v", err)
			}
			if p, _ := st.PendingContributions(pid); len(p) != 0 {
				t.Fatalf("want 0 pending after mark shared, got %d", len(p))
			}
			_ = shareID
		})
	}
}

// TestReconcileStaleBothBackends guards the HAVING clause in ReconcileStale:
// referencing the SELECT alias there works on SQLite but is rejected by
// Postgres (42703), so the query must repeat the aggregate expression.
func TestReconcileStaleBothBackends(t *testing.T) {
	for _, b := range storetest.Both(t) {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store

			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "repo_root", Identity: "/stale", DisplayName: "stale"}, store.Now())
			if err != nil {
				t.Fatalf("upsert project: %v", err)
			}
			old := time.Now().UTC().Add(-store.LivenessHorizon - time.Hour).Format(store.TimeLayout)
			staleID, err := st.EnsureSession("stale-1", pid, old, "", "claude-code")
			if err != nil {
				t.Fatalf("ensure stale session: %v", err)
			}
			if err := st.AppendEvent(staleID, "prompt", "old", old); err != nil {
				t.Fatalf("append old event: %v", err)
			}
			freshID, err := st.EnsureSession("fresh-1", pid, store.Now(), "", "claude-code")
			if err != nil {
				t.Fatalf("ensure fresh session: %v", err)
			}

			closed, err := st.ReconcileStale(nil)
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if closed != 1 {
				t.Fatalf("closed = %d, want 1", closed)
			}
			var endedAt, reason sql.NullString
			if err := st.QueryRow(`SELECT ended_at, end_reason FROM sessions WHERE id = ?`, staleID).Scan(&endedAt, &reason); err != nil {
				t.Fatalf("read stale session: %v", err)
			}
			if !endedAt.Valid || endedAt.String != old {
				t.Fatalf("ended_at = %+v, want %q", endedAt, old)
			}
			if reason.String != "interrupted" {
				t.Fatalf("end_reason = %q, want interrupted", reason.String)
			}
			if err := st.QueryRow(`SELECT ended_at FROM sessions WHERE id = ?`, freshID).Scan(&endedAt); err != nil {
				t.Fatalf("read fresh session: %v", err)
			}
			if endedAt.Valid {
				t.Fatalf("fresh session unexpectedly closed at %q", endedAt.String)
			}
		})
	}
}
