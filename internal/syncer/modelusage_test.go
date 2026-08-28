package syncer

import (
	"path/filepath"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// A session that used more than one model must carry the per-model breakdown on
// its wire snapshot, heaviest first, and a clean ack must stamp the rows synced
// and drop the session from the outbox — the same discipline as memory usage.
func TestEligibleSessionsCarryModelUsage(t *testing.T) {
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
	if err := st.AddUsage(sid, "claude-opus-4-8", store.Usage{Input: 200, Output: 80}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddUsage(sid, "claude-haiku-4-5", store.Usage{Input: 20, Output: 5}); err != nil {
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
	refs := snapshot[0].session.ModelUsage
	if len(refs) != 2 {
		t.Fatalf("got %d model usage refs, want 2: %+v", len(refs), refs)
	}
	if refs[0].Model != "claude-opus-4-8" || refs[0].InputTokens != 200 || refs[0].OutputTokens != 80 {
		t.Errorf("refs[0] = %+v, want opus 200/80 (heaviest first)", refs[0])
	}
	if refs[1].Model != "claude-haiku-4-5" || refs[1].InputTokens != 20 || refs[1].OutputTokens != 5 {
		t.Errorf("refs[1] = %+v, want haiku 20/5", refs[1])
	}

	if err := markSynced(st, snapshot); err != nil {
		t.Fatal(err)
	}
	after, err := eligibleSessions(st, cfg, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("session with synced model usage still eligible: %d rows", len(after))
	}

	var dirtyVal int
	var syncedVal *string
	if err := st.DB.QueryRow(
		`SELECT synced_at, sync_dirty FROM session_model_usage WHERE session_id = ? AND model = 'claude-opus-4-8'`, sid,
	).Scan(&syncedVal, &dirtyVal); err != nil {
		t.Fatal(err)
	}
	if syncedVal == nil || *syncedVal == "" {
		t.Error("model usage row not stamped synced_at")
	}
	if dirtyVal != 0 {
		t.Errorf("model usage row sync_dirty = %d, want 0", dirtyVal)
	}
}

// A per-model usage change recorded after the batch snapshot was read must keep
// the session dirty so it re-sends, matching the memory-usage concurrent-update
// guard.
func TestModelUsageChangeAfterSnapshotKeepsSessionDirty(t *testing.T) {
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
	if err := st.AddUsage(sid, "claude-opus-4-8", store.Usage{Input: 100, Output: 40}); err != nil {
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

	// A later model-usage delta lands after the snapshot but before the ack.
	if err := st.AddUsage(sid, "claude-opus-4-8", store.Usage{Input: 10, Output: 5}); err != nil {
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
		t.Fatalf("session with a post-snapshot model-usage change was dropped; %d rows eligible, want 1", len(after))
	}
	if after[0].session.ModelUsage[0].InputTokens != 110 {
		t.Errorf("input = %d, want 110 (post-snapshot increment)", after[0].session.ModelUsage[0].InputTokens)
	}
}
