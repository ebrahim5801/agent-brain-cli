package syncer

import (
	"path/filepath"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// A session that retrieved and cited memory entries must carry those rows on
// its wire snapshot, and a clean ack must stamp them synced and drop the
// session from the outbox — mirroring event counts (Phase 4).
func TestEligibleSessionsCarryMemoryUsage(t *testing.T) {
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	at := store.Now()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "dir", Identity: "/proj", DisplayName: "proj"}, at)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := st.EnsureSession("sess-1", pid, at, "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRetrieval(sid, pid, "personal", 42, "", at); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkCited(sid, "personal", 42, "", at); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRetrieval(sid, pid, "team", 0, "team-uid-1", at); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseSession(sid, store.Now(), "clear"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Links: map[string]config.Link{
		config.LinkKey("dir", "/proj"): {ProjectKey: "pk"},
	}}

	snapshot, err := eligibleSessions(st, cfg, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 {
		t.Fatalf("got %d eligible sessions, want 1", len(snapshot))
	}
	refs := snapshot[0].session.MemoryUsage
	if len(refs) != 2 {
		t.Fatalf("got %d memory usage refs, want 2: %+v", len(refs), refs)
	}
	var personal, team bool
	for _, r := range refs {
		switch r.Scope {
		case "personal":
			personal = true
			if r.MemoryID != 42 || !r.Cited || r.Retrieved != 1 {
				t.Errorf("personal ref = %+v, want id=42 cited=true retrieved=1", r)
			}
		case "team":
			team = true
			if r.TeamUID != "team-uid-1" || r.Cited || r.Retrieved != 1 {
				t.Errorf("team ref = %+v, want uid=team-uid-1 cited=false retrieved=1", r)
			}
		default:
			t.Errorf("unexpected scope %q", r.Scope)
		}
	}
	if !personal || !team {
		t.Fatalf("missing scope: personal=%v team=%v", personal, team)
	}

	if err := markSynced(st, snapshot); err != nil {
		t.Fatal(err)
	}
	after, err := eligibleSessions(st, cfg, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("session with synced usage still eligible: %d rows", len(after))
	}

	row := st.DB.QueryRow(`SELECT synced_at, sync_dirty FROM memory_usage_events WHERE session_id = ? AND scope = 'personal'`, sid)
	var dirtyVal int
	var syncedVal *string
	if err := row.Scan(&syncedVal, &dirtyVal); err != nil {
		t.Fatal(err)
	}
	if syncedVal == nil || *syncedVal == "" {
		t.Error("personal usage row not stamped synced_at")
	}
	if dirtyVal != 0 {
		t.Errorf("personal usage row sync_dirty = %d, want 0", dirtyVal)
	}
}

// A usage change recorded after the batch snapshot was read must keep the
// session dirty so it re-sends — the same concurrent-update discipline that
// already covers usage/close, extended to memory usage (the load-bearing
// cross-process dirty flag, since retrieval originates in the MCP process).
func TestMemoryUsageChangeAfterSnapshotKeepsSessionDirty(t *testing.T) {
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	at := store.Now()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "dir", Identity: "/proj", DisplayName: "proj"}, at)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := st.EnsureSession("sess-1", pid, at, "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRetrieval(sid, pid, "personal", 7, "", at); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseSession(sid, store.Now(), "clear"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Links: map[string]config.Link{
		config.LinkKey("dir", "/proj"): {ProjectKey: "pk"},
	}}

	snapshot, err := eligibleSessions(st, cfg, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 {
		t.Fatalf("got %d eligible sessions, want 1", len(snapshot))
	}

	// Concurrent MCP-process retrieval after the snapshot but before the ack.
	if err := st.RecordRetrieval(sid, pid, "personal", 7, "", store.Now()); err != nil {
		t.Fatal(err)
	}

	if err := markSynced(st, snapshot); err != nil {
		t.Fatal(err)
	}

	after, err := eligibleSessions(st, cfg, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("session with a post-snapshot usage change was dropped from the outbox; %d rows eligible, want 1", len(after))
	}
	if after[0].session.MemoryUsage[0].Retrieved != 2 {
		t.Errorf("retrieved = %d, want 2 (post-snapshot increment)", after[0].session.MemoryUsage[0].Retrieved)
	}
}
