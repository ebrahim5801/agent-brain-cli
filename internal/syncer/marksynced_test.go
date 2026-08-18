package syncer

import (
	"path/filepath"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// markSynced must not clear sync_dirty for a row that a concurrent hook mutated
// between the batch snapshot and the server ack. Otherwise the corrected state
// (close time, final usage) would never re-sync, since a normally-closed
// session receives no further writes.
func TestMarkSyncedKeepsDirtyOnConcurrentUpdate(t *testing.T) {
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
	if err := st.AppendEvent(sid, "prompt", "", at); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Links: map[string]config.Link{
		config.LinkKey("dir", "/proj"): {ProjectKey: "pk"},
	}}

	// Snapshot the open session as the sync loop would.
	snapshot, err := eligibleSessions(st, cfg, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 {
		t.Fatalf("got %d eligible sessions, want 1", len(snapshot))
	}

	// Concurrent hook: the session ends and its real usage is backfilled after
	// the snapshot was taken but before the ack is applied.
	if err := st.CloseSession(sid, store.Now(), "clear"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUsage(sid, "claude-sonnet-5", store.Usage{Input: 1000, Output: 2000}); err != nil {
		t.Fatal(err)
	}

	// Ack the stale snapshot.
	if err := markSynced(st, snapshot); err != nil {
		t.Fatal(err)
	}

	// The row must still be eligible so its corrected state re-syncs.
	after, err := eligibleSessions(st, cfg, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("concurrently-updated session was marked synced and lost; %d rows still eligible, want 1", len(after))
	}
	if after[0].session.EndedAt == "" || after[0].session.Usage.InputTokens != 1000 {
		t.Errorf("re-sync would carry stale state: ended=%q input=%d",
			after[0].session.EndedAt, after[0].session.Usage.InputTokens)
	}

	// A clean ack of the current snapshot does clear the flag.
	if err := markSynced(st, after); err != nil {
		t.Fatal(err)
	}
	final, err := eligibleSessions(st, cfg, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != 0 {
		t.Errorf("a matching ack should have cleared sync_dirty; %d rows still eligible", len(final))
	}
}
