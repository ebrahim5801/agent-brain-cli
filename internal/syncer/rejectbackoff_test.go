package syncer

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func rejectTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedSessions(t *testing.T, st *store.Store, project string, n int) int64 {
	t.Helper()
	at := store.Now()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "dir", Identity: project, DisplayName: project}, at)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		sid, err := st.EnsureSession(project+"-"+store.Now()+"-"+itoa(i), pid, at, "", "claude-code")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AppendEvent(sid, "prompt", "", at); err != nil {
			t.Fatal(err)
		}
	}
	return pid
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// The starvation guard: a project whose rows all get refused must not keep
// filling the finite ORDER BY id window and hide newer projects behind it.
func TestRejectedRowsStopStarvingLaterProjects(t *testing.T) {
	st := rejectTestStore(t)

	// "/blocked" is seeded first, so its rows hold the lowest ids and would
	// otherwise occupy the whole window.
	seedSessions(t, st, "/blocked", maxBatchSessions+20)
	seedSessions(t, st, "/healthy", 3)

	cfg := &config.Config{Links: map[string]config.Link{
		config.LinkKey("dir", "/blocked"): {ProjectKey: "pk-blocked"},
		config.LinkKey("dir", "/healthy"): {ProjectKey: "pk-healthy"},
	}}

	before, err := eligibleSessions(st, cfg, maxBatchSessions, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range before {
		if o.session.ProjectKey == "pk-healthy" {
			t.Fatal("setup is not exercising the bug: a healthy row already fits in the first window")
		}
	}

	if err := markRejected(st, before); err != nil {
		t.Fatalf("markRejected: %v", err)
	}

	after, err := eligibleSessions(st, cfg, maxBatchSessions, true)
	if err != nil {
		t.Fatal(err)
	}
	healthy := 0
	for _, o := range after {
		if o.session.ProjectKey == "pk-healthy" {
			healthy++
		}
	}
	if healthy != 3 {
		t.Fatalf("healthy rows now deliverable = %d, want 3 (rejected rows still starve the window)", healthy)
	}
}

// Deferring a row must not hide it from queue-depth reporting: `status` saying
// "nothing queued" while a project silently fails is how the real-world
// six-week outage stayed invisible.
func TestDeferredRowsStillCountAsQueued(t *testing.T) {
	st := rejectTestStore(t)
	seedSessions(t, st, "/blocked", 5)
	cfg := &config.Config{Links: map[string]config.Link{
		config.LinkKey("dir", "/blocked"): {ProjectKey: "pk-blocked"},
	}}

	rows, err := eligibleSessions(st, cfg, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := markRejected(st, rows); err != nil {
		t.Fatal(err)
	}

	due, err := eligibleSessions(st, cfg, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("deliverable rows = %d, want 0 while backing off", len(due))
	}
	if n := UnsyncedCount(st, cfg); n != 5 {
		t.Fatalf("UnsyncedCount = %d, want 5 — deferred rows are still queued", n)
	}
}

func TestRejectBackoffGrowsAndCaps(t *testing.T) {
	if got := rejectBackoff(1); got != rejectBackoffBase {
		t.Fatalf("first rejection = %v, want %v", got, rejectBackoffBase)
	}
	if got := rejectBackoff(2); got != 2*rejectBackoffBase {
		t.Fatalf("second rejection = %v, want %v", got, 2*rejectBackoffBase)
	}
	prev := time.Duration(0)
	for n := int64(1); n <= 40; n++ {
		got := rejectBackoff(n)
		if got < prev {
			t.Fatalf("backoff went backwards at n=%d: %v after %v", n, got, prev)
		}
		if got > rejectBackoffMax {
			t.Fatalf("backoff at n=%d = %v, exceeds cap %v", n, got, rejectBackoffMax)
		}
		prev = got
	}
	if rejectBackoff(40) != rejectBackoffMax {
		t.Fatalf("backoff should reach the cap, got %v", rejectBackoff(40))
	}
}

// A project that regains access must not carry an inflated delay forward.
func TestAckClearsRejectBackoff(t *testing.T) {
	st := rejectTestStore(t)
	seedSessions(t, st, "/flaky", 2)
	cfg := &config.Config{Links: map[string]config.Link{
		config.LinkKey("dir", "/flaky"): {ProjectKey: "pk-flaky"},
	}}

	rows, err := eligibleSessions(st, cfg, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := markRejected(st, rows); err != nil {
		t.Fatal(err)
	}
	if err := markSynced(st, rows); err != nil {
		t.Fatalf("markSynced: %v", err)
	}

	for _, r := range rows {
		var count int64
		var retryAfter *string
		if err := st.DB.QueryRow(st.Rebind(
			`SELECT sync_reject_count, sync_retry_after FROM sessions WHERE id = ?`), r.rowID).Scan(&count, &retryAfter); err != nil {
			t.Fatal(err)
		}
		if count != 0 || retryAfter != nil {
			t.Fatalf("row %d: reject_count = %d retry_after = %v, want 0/nil after ack", r.rowID, count, retryAfter)
		}
	}
}
