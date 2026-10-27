package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// setupSessionMemories stores memories from a good and a bad session. The bad
// session replaced good-2 directly and good-3 through a chain (bad-a, then bad-b).
func setupSessionMemories(t *testing.T) (ids map[string]int64, projectID int64) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("AGENT_BRAIN_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", filepath.Join(root, "config"))
	work := filepath.Join(root, "project")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	project := attribution.Resolve(work)
	projectID, err = st.UpsertProject(store.ProjectIdentity{Kind: project.Kind, Identity: project.Identity, DisplayName: project.DisplayName}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	at := func(sec int) string { return time.Date(2026, 1, 1, 0, 0, sec, 0, time.UTC).Format(store.TimeLayout) }
	good, err := st.EnsureSession("good-session-1111", projectID, at(0), "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	bad, err := st.EnsureSession("bad-session-2222", projectID, at(10), "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	ids = map[string]int64{}
	for i, row := range []struct {
		name    string
		session int64
	}{{"good-1", good}, {"good-2", good}, {"good-3", good}, {"bad-1", bad}, {"bad-2", bad}, {"bad-a", bad}, {"bad-b", bad}, {"loose", 0}} {
		id, err := st.InsertMemory(store.NewMemory{ProjectID: projectID, SessionID: row.session, Content: "memory " + row.name,
			Kind: "fact", Origin: "auto", Priority: store.MemoryPriorityNormal}, at(20+i))
		if err != nil {
			t.Fatal(err)
		}
		ids[row.name] = id
	}
	for _, pair := range [][2]string{{"good-2", "bad-2"}, {"good-3", "bad-a"}, {"bad-a", "bad-b"}} {
		if err := st.SupersedeMemory(ids[pair[0]], ids[pair[1]], projectID, at(40)); err != nil {
			t.Fatal(err)
		}
	}
	return ids, projectID
}

func runMemoryCommand(t *testing.T, cmd interface {
	SetArgs([]string)
	Execute() error
}, args ...string) (string, error) {
	t.Helper()
	cmd.SetArgs(args)
	var err error
	out := capture(t, func() { err = cmd.Execute() })
	return out, err
}

func statuses(t *testing.T, projectID int64) map[string]string {
	t.Helper()
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir, _ := os.Getwd()
	entries, err := memory.List(st, projectID, dir, true, cliGitBudget)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range entries {
		out[strings.TrimPrefix(r.Entry.Content, "memory ")] = r.Entry.Status
	}
	return out
}

func TestMemoryListAndShowBySession(t *testing.T) {
	ids, _ := setupSessionMemories(t)
	out, err := runMemoryCommand(t, newMemoryListCmd(), "--session", "bad-sess")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"memory bad-1", "memory bad-2", "memory bad-b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list --session misses %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"memory good-1", "memory bad-a", "memory loose"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("list --session shows %q:\n%s", unwanted, out)
		}
	}
	out, err = runMemoryCommand(t, newMemoryShowCmd(), strconv.FormatInt(ids["bad-1"], 10))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "session:   bad-session-2222 (started 2026-01-01") {
		t.Fatalf("show does not name the session:\n%s", out)
	}
	out, err = runMemoryCommand(t, newMemoryShowCmd(), strconv.FormatInt(ids["loose"], 10))
	if err != nil || strings.Contains(out, "session:") {
		t.Fatalf("an entry without a session shows one (err %v):\n%s", err, out)
	}
}

func TestMemoryDeleteSessionDeletesItsEntriesAndRestoresWhatTheyReplaced(t *testing.T) {
	_, projectID := setupSessionMemories(t)
	out, err := runMemoryCommand(t, newMemoryDeleteCmd(), "--session", "bad-session-2222", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "saved 3 active memories") || !strings.Contains(out, "They replaced 2 older memories") ||
		!strings.Contains(out, "Deleted 3 memories from session bad-session-2222 and restored 2 they had replaced.") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	want := map[string]string{
		"good-1": store.MemoryActive, "good-2": store.MemoryActive, "good-3": store.MemoryActive, "loose": store.MemoryActive,
		"bad-1": store.MemoryDeleted, "bad-2": store.MemoryDeleted, "bad-b": store.MemoryDeleted,
		// Already hidden behind another entry of the same session; left as it was.
		"bad-a": store.MemorySuperseded,
	}
	got := statuses(t, projectID)
	for name, status := range want {
		if got[name] != status {
			t.Fatalf("%s is %q, want %q (all: %v)", name, got[name], status, got)
		}
	}
	out, err = runMemoryCommand(t, newMemoryDeleteCmd(), "--session", "bad-session-2222", "--yes")
	if err != nil || !strings.Contains(out, "has no active memories left") {
		t.Fatalf("second run (err %v):\n%s", err, out)
	}
}

func TestMemoryDeleteSessionKeepsEverythingWithoutConfirmation(t *testing.T) {
	_, projectID := setupSessionMemories(t)
	before := statuses(t, projectID)
	stdin := os.Stdin
	t.Cleanup(func() { os.Stdin = stdin })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString("n\n")
	_ = w.Close()
	os.Stdin = r
	out, err := runMemoryCommand(t, newMemoryDeleteCmd(), "--session", "bad-session")
	if err != nil || !strings.Contains(out, "Kept.") {
		t.Fatalf("declined delete (err %v):\n%s", err, out)
	}
	after := statuses(t, projectID)
	for name, status := range before {
		if after[name] != status {
			t.Fatalf("%s changed from %q to %q after declining", name, status, after[name])
		}
	}
}

func TestMemoryDeleteNeedsExactlyOneTarget(t *testing.T) {
	setupSessionMemories(t)
	for _, args := range [][]string{{}, {"1", "--session", "bad-session"}} {
		if _, err := runMemoryCommand(t, newMemoryDeleteCmd(), args...); err == nil || !strings.Contains(err.Error(), "either a memory id or --session") {
			t.Fatalf("delete %v: err = %v", args, err)
		}
	}
}

func TestResolveSession(t *testing.T) {
	sessions := []store.MemorySession{{ID: 1, ExternalID: "abcdef12-one"}, {ID: 2, ExternalID: "abcdef12-two"}, {ID: 3, ExternalID: "short"},
		{ID: 4, ExternalID: "v2:c909621f-27e8"}}
	if s, err := memory.ResolveSession(sessions, "short"); err != nil || s.ID != 3 {
		t.Fatalf("an exact short ID: %v %v", s, err)
	}
	for _, ref := range []string{"c909621f", "v2:c909621f", "c909621f-27e8"} {
		if s, err := memory.ResolveSession(sessions, ref); err != nil || s.ID != 4 {
			t.Fatalf("a bound session as %q: %v %v", ref, s, err)
		}
	}
	if s, err := memory.ResolveSession(sessions, "abcdef12-t"); err != nil || s.ID != 2 {
		t.Fatalf("a unique prefix: %v %v", s, err)
	}
	for ref, msg := range map[string]string{"abcdef12": "matches 2 sessions", "abc": "at least 8 characters", "zzzzzzzzz": "no session"} {
		if _, err := memory.ResolveSession(sessions, ref); err == nil || !strings.Contains(err.Error(), msg) {
			t.Fatalf("%q: err = %v, want %q", ref, err, msg)
		}
	}
}
