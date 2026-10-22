package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// The periodic checkpoint needs a few real tool calls, and each "nothing to
// save" answer doubles the wait before the next one.
func TestPeriodicCaptureNeedsWorkAndBacksOffAfterEmptyAnswers(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	enableMemoryForTest(t)
	setMemoryCapture(t, intPtr(0), intPtr(25))
	st := openTestStore(t)
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"session_id":"periodic","cwd":%q}`, proj)
	tool := fmt.Sprintf(`{"session_id":"periodic","cwd":%q,"tool_name":"Edit"}`, proj)
	hookWith(t, "session-start", "claude-code", payload)
	hookWith(t, "prompt", "claude-code", payload)

	var sessionID, projectID int64
	if err := st.QueryRow(`SELECT session_id,project_id FROM memory_session_bindings ORDER BY session_id DESC LIMIT 1`).Scan(&sessionID, &projectID); err != nil {
		t.Fatal(err)
	}
	ago := func(d time.Duration) string { return time.Now().Add(-d).UTC().Format(store.TimeLayout) }
	if _, err := st.Exec(`UPDATE sessions SET started_at=? WHERE id=?`, ago(30*time.Minute), sessionID); err != nil {
		t.Fatal(err)
	}
	// Four tool calls in half an hour are not enough work to ask.
	for i := 0; i < periodicMinTools-1; i++ {
		if got := hookWith(t, "tool-use", "claude-code", tool); got != "" {
			t.Fatalf("periodic capture after %d tool calls", i+1)
		}
	}
	if got := hookWith(t, "tool-use", "claude-code", tool); !strings.Contains(got, "Local memory capture checkpoint") {
		t.Fatalf("no periodic capture after %d tool calls: %q", periodicMinTools, got)
	}

	answer := func() {
		t.Helper()
		var id, handle, checkout string
		if err := st.QueryRow(`SELECT b.id,b.source_handle,s.checkout FROM memory_capture_batches b JOIN memory_session_bindings s ON s.handle=b.source_handle WHERE s.session_id=? AND b.state='pending' ORDER BY b.created_at DESC LIMIT 1`, sessionID).Scan(&id, &handle, &checkout); err != nil {
			t.Fatal(err)
		}
		binding, err := st.ResolveBinding(handle, projectID, checkout)
		if err != nil {
			t.Fatal(err)
		}
		if done, err := st.CompleteCapture(id, binding, 0, true, time.Now()); err != nil || done.State != "no_durable_changes" {
			t.Fatalf("one-call answer: %+v %v", done, err)
		}
	}
	age := func(d time.Duration) {
		t.Helper()
		if _, err := st.Exec(`UPDATE memory_capture_batches SET created_at=? WHERE source_handle IN (SELECT handle FROM memory_session_bindings WHERE session_id=?)`, ago(d), sessionID); err != nil {
			t.Fatal(err)
		}
	}
	work := func() string {
		t.Helper()
		var got string
		for i := 0; i < periodicMinTools; i++ {
			got = hookWith(t, "tool-use", "claude-code", tool)
		}
		return got
	}

	// One empty answer: the next request waits 10 minutes, not 5.
	answer()
	age(6 * time.Minute)
	if got := work(); got != "" {
		t.Fatal("periodic capture ignored an empty answer's back-off")
	}
	age(11 * time.Minute)
	if got := hookWith(t, "tool-use", "claude-code", tool); !strings.Contains(got, "Local memory capture checkpoint") {
		t.Fatalf("no periodic capture after the back-off: %q", got)
	}

	// Two empty answers in a row: 20 minutes.
	answer()
	age(15 * time.Minute)
	if got := work(); got != "" {
		t.Fatal("second empty answer did not double the wait")
	}
	age(21 * time.Minute)
	if got := hookWith(t, "tool-use", "claude-code", tool); !strings.Contains(got, "Local memory capture checkpoint") {
		t.Fatalf("no periodic capture after the doubled back-off: %q", got)
	}
}

func TestPeriodicWaitIsCappedAndResetBySaving(t *testing.T) {
	sandbox(t)
	st := openTestStore(t)
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	if got := periodicWait(st, "nobody"); got != periodicBaseWait {
		t.Fatalf("first wait = %v", got)
	}
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: t.TempDir()}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.EnsureBoundSession(pid, "claude-code", "cap", t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	add := func(watermark int64, state string) {
		t.Helper()
		batch, err := st.RequestCapture(b, "periodic", watermark, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(time.Duration(watermark) * time.Second).UTC().Format(store.TimeLayout)
		if _, err := st.Exec(`UPDATE memory_capture_batches SET state=?,updated_at=? WHERE id=?`, state, at, batch.ID); err != nil {
			t.Fatal(err)
		}
	}
	for w := int64(1); w <= 6; w++ {
		add(w, "no_durable_changes")
	}
	if got := periodicWait(st, b.Handle); got != periodicMaxWait {
		t.Fatalf("wait after many empty answers = %v, want %v", got, periodicMaxWait)
	}
	add(7, "persisted")
	if got := periodicWait(st, b.Handle); got != periodicBaseWait {
		t.Fatalf("wait after a save = %v, want %v", got, periodicBaseWait)
	}
}
