package cli

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func TestHookKeepsStartHandleInNestedCheckout(t *testing.T) {
	sandbox(t)
	enableMemoryForTest(t)
	st := openTestStore(t)
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}

	start := t.TempDir()
	nested := filepath.Join(start, "repo")
	if out, err := exec.Command("git", "init", "-q", nested).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	payload := func(cwd string) string {
		return fmt.Sprintf(`{"session_id":"nested","cwd":%q,"tool_name":"Bash"}`, cwd)
	}

	hookWith(t, "session-start", "claude-code", payload(start))
	hookWith(t, "tool-use", "claude-code", payload(nested))
	hookWith(t, "stop", "claude-code", payload(nested))

	rows, err := st.Query(`SELECT handle,project_id FROM memory_session_bindings WHERE native_id='nested'`)
	if err != nil {
		t.Fatal(err)
	}
	var handles []string
	var projectID int64
	for rows.Next() {
		var h string
		if err := rows.Scan(&h, &projectID); err != nil {
			t.Fatal(err)
		}
		handles = append(handles, h)
	}
	rows.Close()
	if len(handles) != 1 {
		t.Fatalf("bindings for one session = %v, want exactly one", handles)
	}

	startProject, err := st.UpsertProject(projectIdentityFor(t, start), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	if projectID != startProject {
		t.Fatalf("binding project = %d, want the start folder's project %d", projectID, startProject)
	}
	b, err := st.ResolveBinding(handles[0], startProject, start)
	if err != nil {
		t.Fatalf("MCP-side resolve from the start folder: %v", err)
	}
	var events int
	if err := st.QueryRow(`SELECT COUNT(*) FROM events WHERE session_id=? AND kind='tool_use'`, b.SessionID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("tool_use events on the start session = %d, want 1", events)
	}
	batch, err := st.RequestCapture(b, "stop", 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginCapture(batch.ID, b, []string{"item"}, time.Now()); err != nil {
		t.Fatalf("capture begin with the start handle: %v", err)
	}
}

func TestHookSameNativeIDOutsideStartFolderStaysIndependent(t *testing.T) {
	sandbox(t)
	st := openTestStore(t)
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	a, b := t.TempDir(), t.TempDir()
	payload := func(cwd string) string {
		return fmt.Sprintf(`{"session_id":"shared","cwd":%q}`, cwd)
	}
	hookWith(t, "session-start", "claude-code", payload(a))
	hookWith(t, "session-start", "claude-code", payload(b))

	var n int
	if err := st.QueryRow(`SELECT COUNT(DISTINCT handle) FROM memory_session_bindings WHERE native_id='shared'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("handles for one native id in unrelated folders = %d, want 2", n)
	}
}
