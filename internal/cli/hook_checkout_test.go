package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

var captureRequest = regexp.MustCompile(`session_handle=([0-9a-f-]+), batch_id=([0-9a-f-]+)`)

// The reported bug: a Claude Code session's shell changes into subfolders of
// the repository. Every hook must keep the session's one handle, the MCP
// server (started at the repository root) must accept it, and an unanswered
// stop request must not be repeated at every stop.
func TestShellCdKeepsOneHandleAndStopDoesNotLoop(t *testing.T) {
	for _, viaProjectDir := range []bool{false, true} {
		t.Run(fmt.Sprintf("CLAUDE_PROJECT_DIR=%v", viaProjectDir), func(t *testing.T) {
			sandbox(t)
			root := t.TempDir()
			for _, d := range []string{".git", filepath.Join("apps", "web"), filepath.Join("apps", "api")} {
				if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if viaProjectDir {
				t.Setenv("CLAUDE_PROJECT_DIR", root)
			}
			enableMemoryForTest(t)
			setMemoryCapture(t, intPtr(1), intPtr(0))
			st := openTestStore(t)
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			payload := func(dir, tool string) string {
				p := fmt.Sprintf(`{"session_id":"cd-session","cwd":%q`, dir)
				if tool != "" {
					p += fmt.Sprintf(`,"tool_name":%q`, tool)
				}
				return p + "}"
			}
			hookWith(t, "session-start", "claude-code", payload(root, ""))
			hookWith(t, "prompt", "claude-code", payload(root, ""))
			for _, dir := range []string{"web", "api"} {
				hookWith(t, "tool-use", "claude-code", payload(filepath.Join(root, "apps", dir), "Bash"))
			}
			var bindings int
			if err := st.QueryRow(`SELECT COUNT(*) FROM memory_session_bindings WHERE native_id='cd-session'`).Scan(&bindings); err != nil {
				t.Fatal(err)
			}
			if bindings != 1 {
				t.Fatalf("%d bindings after cd into subfolders, want 1", bindings)
			}

			sub := filepath.Join(root, "apps", "api")
			got := hookWith(t, "stop", "claude-code", payload(sub, ""))
			m := captureRequest.FindStringSubmatch(got)
			if m == nil {
				t.Fatalf("stop did not request capture: %q", got)
			}
			handle, batchID := m[1], m[2]

			var pid int64
			if err := st.QueryRow(`SELECT id FROM projects WHERE identity=?`, attribution.Resolve(root).Identity).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			binding, err := st.ResolveBinding(handle, pid, root)
			if err != nil {
				t.Fatalf("MCP server at the root rejected the stop hook's handle: %v", err)
			}

			// The agent does more work but never begins the request: no new block.
			hookWith(t, "tool-use", "claude-code", payload(sub, "Bash"))
			if got := hookWith(t, "stop", "claude-code", payload(sub, "")); got != "" {
				t.Fatalf("stop blocked again while the first request was unanswered: %q", got)
			}

			// Deferring the never-begun request acknowledges it and re-arms capture.
			if _, err := st.DeferCapture(batchID, binding, 0, "context_unavailable", time.Now()); err != nil {
				t.Fatalf("defer of a never-begun request: %v", err)
			}
			hookWith(t, "tool-use", "claude-code", payload(sub, "Bash"))
			if got := hookWith(t, "stop", "claude-code", payload(sub, "")); captureRequest.FindStringSubmatch(got) == nil {
				t.Fatalf("capture did not re-arm after the defer: %q", got)
			}
		})
	}
}

func TestDeferNeedsAValidRequestState(t *testing.T) {
	sandbox(t)
	st := openTestStore(t)
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.EnsureBoundSession(pid, "claude-code", "defer-session", dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := st.RequestCapture(b, "stop", 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeferCapture(batch.ID, b, 3, "context_unavailable", time.Now()); err == nil {
		t.Fatal("defer with a wrong generation was accepted")
	}
	deferred, err := st.DeferCapture(batch.ID, b, 0, "context_unavailable", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if deferred.State != "needs_context" || deferred.ExecutorHandle != b.Handle {
		t.Fatalf("deferred batch = %+v", deferred)
	}
	if _, err := st.DeferCapture(batch.ID, b, 0, "context_unavailable", time.Now()); err == nil {
		t.Fatal("a deferred batch was deferred again without being begun")
	}
}
