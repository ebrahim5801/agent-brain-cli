package store_test

import (
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

// TestTransferSQLiteToPostgres seeds a populated SQLite store covering every
// table (including a superseded_by chain and share-tracking columns), migrates
// it to Postgres, and asserts a row-for-row match, that sequences advanced past
// the copied max(id), and that the SQLite data is logically unchanged. It is
// gated on AGENT_BRAIN_TEST_PG_DSN.
func TestTransferSQLiteToPostgres(t *testing.T) {
	dst := storetest.Postgres(t) // skips when the DSN is unset
	src := storetest.SQLite(t)

	seed := seedAllTables(t, src)

	counts, err := src.TransferTo(dst)
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	for _, c := range counts {
		if c.SQLite != c.Postgres {
			t.Errorf("table %s: sqlite=%d postgres=%d", c.Table, c.SQLite, c.Postgres)
		}
		if c.SQLite == 0 {
			t.Errorf("table %s seeded 0 rows — the test does not exercise it", c.Table)
		}
	}

	// Ids preserved: the superseded memory still points at its successor.
	got, err := dst.GetMemory(seed.supersededID)
	if err != nil {
		t.Fatalf("get migrated superseded memory: %v", err)
	}
	if got.Status != store.MemorySuperseded || !got.SupersededBy.Valid || got.SupersededBy.Int64 != seed.successorID {
		t.Fatalf("superseded chain not preserved: %+v", got)
	}

	// Sequences advanced: a fresh insert on Postgres must not collide with a
	// preserved id.
	newID, err := dst.InsertMemory(store.NewMemory{ProjectID: seed.projectID, Content: "post-migration", Kind: "fact", Origin: "manual"}, store.Now())
	if err != nil {
		t.Fatalf("insert after migration: %v", err)
	}
	if newID <= seed.successorID {
		t.Fatalf("sequence not advanced: new id %d <= copied max %d", newID, seed.successorID)
	}

	// SQLite logically unchanged: re-open the file and re-count (never
	// byte-equality — WAL checkpointing can rewrite the file on open).
	reopened, err := store.OpenAt(src.Path)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	defer reopened.Close()
	for _, table := range store.DataTables() {
		before, _ := src.CountRows(table)
		after, err := reopened.CountRows(table)
		if err != nil {
			t.Fatalf("recount %s: %v", table, err)
		}
		if before != after {
			t.Errorf("sqlite %s changed by the migration: %d -> %d", table, before, after)
		}
	}
}

// TestTransferDeltaPass covers the concurrent-write catch-up: rows written
// after the snapshot copy (via the test hook, standing in for an active hook
// process) must still land in Postgres through the delta pass.
func TestTransferDeltaPass(t *testing.T) {
	dst := storetest.Postgres(t)
	src := storetest.SQLite(t)
	seed := seedAllTables(t, src)

	var extraMemory, extraEvent int64
	store.SetTransferTestHook(func() {
		var err error
		extraMemory, err = src.InsertMemory(store.NewMemory{ProjectID: seed.projectID, Content: "written during copy", Kind: "fact", Origin: "distilled"}, store.Now())
		if err != nil {
			t.Errorf("concurrent insert: %v", err)
		}
		if err := src.AppendEvent(seed.sessionID, "tool_use", "concurrent", store.Now()); err != nil {
			t.Errorf("concurrent event: %v", err)
		}
		if err := src.QueryRow(`SELECT MAX(id) FROM events`).Scan(&extraEvent); err != nil {
			t.Errorf("read concurrent event id: %v", err)
		}
	})
	defer store.SetTransferTestHook(nil)

	counts, err := src.TransferTo(dst)
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if !allMatch(counts) {
		t.Fatalf("counts mismatch after delta: %+v", counts)
	}
	if _, err := dst.GetMemory(extraMemory); err != nil {
		t.Fatalf("memory written during the copy missing from postgres: %v", err)
	}
	var n int64
	if err := dst.QueryRow(`SELECT COUNT(*) FROM events WHERE id = ?`, extraEvent).Scan(&n); err != nil || n != 1 {
		t.Fatalf("event written during the copy missing from postgres: n=%d err=%v", n, err)
	}
}

type seededIDs struct {
	projectID, sessionID, supersededID, successorID int64
}

// seedAllTables writes at least one row into every transferred table, including
// a superseded_by chain, share-tracking columns, and a team-memory supersede
// mapping.
func seedAllTables(t *testing.T, st *store.Store) seededIDs {
	t.Helper()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "repo_root", Identity: "/seed", DisplayName: "seed"}, store.Now())
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	sid, err := st.EnsureSession("seed-ext", pid, store.Now(), "/tmp/t.jsonl", "claude-code")
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := st.SetUsage(sid, "sonnet", store.Usage{Input: 5, Output: 7, CacheRead: 1, CacheWrite: 2}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	if err := st.AppendEvent(sid, "prompt", "hello", store.Now()); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if err := st.ReplaceModelUsage(sid, []store.ModelUsage{
		{Model: "sonnet", Usage: store.Usage{Input: 5, Output: 7, CacheRead: 1, CacheWrite: 2}},
	}); err != nil {
		t.Fatalf("seed model usage: %v", err)
	}

	successor, err := st.InsertMemory(store.NewMemory{ProjectID: pid, SessionID: sid, Content: "newer", Kind: "fact", Origin: "distilled", ShareLive: true}, store.Now())
	if err != nil {
		t.Fatalf("seed successor memory: %v", err)
	}
	superseded, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "older", Kind: "fact", Origin: "distilled"}, store.Now())
	if err != nil {
		t.Fatalf("seed superseded memory: %v", err)
	}
	if err := st.SupersedeMemory(superseded, successor, pid, store.Now()); err != nil {
		t.Fatalf("seed supersede: %v", err)
	}

	// team_memories + team_sync_state.
	if err := st.ApplyPull(pid, []store.TeamMemoryRow{
		{UID: "seed-team-uid", ProjectID: pid, Author: "a@x", Content: "team", Kind: "fact", Origin: "distilled", Status: "active", CapturedAt: store.Now(), UpdatedAt: store.Now()},
	}); err != nil {
		t.Fatalf("seed team memory: %v", err)
	}
	if err := st.SetPullCursor(pid, "seed-cursor"); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	// memory_team_supersedes mapping.
	if err := st.AddTeamSupersede(successor, "seed-team-uid"); err != nil {
		t.Fatalf("seed team supersede: %v", err)
	}
	// diagnostics.
	if _, err := st.Exec(`INSERT INTO diagnostics (occurred_at, component, message) VALUES (?, ?, ?)`, store.Now(), "test", "seed"); err != nil {
		t.Fatalf("seed diagnostic: %v", err)
	}
	// memory_usage_events.
	if err := st.RecordRetrieval(sid, pid, "personal", successor, "", store.Now()); err != nil {
		t.Fatalf("seed memory usage event: %v", err)
	}

	return seededIDs{projectID: pid, sessionID: sid, supersededID: superseded, successorID: successor}
}

func allMatch(counts []store.TableCount) bool {
	for _, c := range counts {
		if c.SQLite != c.Postgres {
			return false
		}
	}
	return true
}
