package gitstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-b", "main")
	run(t, dir, "commit", "--allow-empty", "-m", "one")
	return dir
}

func TestCaptureNoRepo(t *testing.T) {
	st := Capture(t.TempDir())
	if st.HasRepo || st.Branch != "" || st.Commit != "" {
		t.Errorf("Capture(non-repo) = %+v, want zero", st)
	}
}

func TestCaptureBranchAndCommit(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)
	head := run(t, dir, "rev-parse", "HEAD")

	st := Capture(dir)
	if !st.HasRepo {
		t.Fatal("HasRepo = false in a repo")
	}
	if st.Branch != "main" {
		t.Errorf("Branch = %q, want main", st.Branch)
	}
	if st.Commit != head {
		t.Errorf("Commit = %q, want %q", st.Commit, head)
	}
}

func TestCaptureFromSubdirectory(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if st := Capture(sub); !st.HasRepo || st.Branch != "main" {
		t.Errorf("Capture(subdir) = %+v", st)
	}
}

func TestCaptureDetachedHead(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)
	head := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "checkout", "--detach", "HEAD")

	st := Capture(dir)
	if st.Branch != "" {
		t.Errorf("detached Branch = %q, want empty", st.Branch)
	}
	if st.Commit != head {
		t.Errorf("detached Commit = %q, want %q", st.Commit, head)
	}
}

func TestCapturePackedRefs(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)
	head := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "pack-refs", "--all")

	if st := Capture(dir); st.Commit != head {
		t.Errorf("packed-refs Commit = %q, want %q", st.Commit, head)
	}
}

func TestCompareCurrent(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)
	st := Capture(dir)

	f := NewComparer(dir, 500*time.Millisecond).Compare(st.Branch, st.Commit)
	if f.Signal != Current {
		t.Errorf("Signal = %s, want current", f.Signal)
	}
	if f.Render() != "current" {
		t.Errorf("Render = %q", f.Render())
	}
}

func TestCompareMovedOnWithCount(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)
	old := Capture(dir)
	run(t, dir, "commit", "--allow-empty", "-m", "two")
	run(t, dir, "commit", "--allow-empty", "-m", "three")

	f := NewComparer(dir, 2*time.Second).Compare(old.Branch, old.Commit)
	if f.Signal != MovedOn {
		t.Fatalf("Signal = %s, want moved-on", f.Signal)
	}
	if f.CommitsSince != 2 {
		t.Errorf("CommitsSince = %d, want 2", f.CommitsSince)
	}
	if f.Render() != "moved-on: 2 commits since" {
		t.Errorf("Render = %q", f.Render())
	}
}

func TestCompareDifferentBranch(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)
	old := Capture(dir)
	run(t, dir, "checkout", "-b", "feature")
	run(t, dir, "commit", "--allow-empty", "-m", "feat")

	f := NewComparer(dir, 2*time.Second).Compare(old.Branch, old.Commit)
	if f.Signal != DifferentBranch {
		t.Fatalf("Signal = %s, want different-branch", f.Signal)
	}
	if f.Render() != "different-branch: main" {
		t.Errorf("Render = %q", f.Render())
	}
}

func TestCompareUnverifiableAfterRewrite(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)
	run(t, dir, "commit", "--allow-empty", "-m", "two")
	old := Capture(dir)
	// Rewrite history: drop the captured commit entirely.
	run(t, dir, "reset", "--hard", "HEAD~1")
	run(t, dir, "commit", "--allow-empty", "-m", "rewritten")
	run(t, dir, "reflog", "expire", "--expire=now", "--all")
	run(t, dir, "gc", "--prune=now", "--aggressive")

	f := NewComparer(dir, 2*time.Second).Compare(old.Branch, old.Commit)
	if f.Signal != Unverifiable {
		t.Errorf("Signal = %s, want unverifiable", f.Signal)
	}
	if f.Render() != "unverifiable: capture point no longer exists" {
		t.Errorf("Render = %q", f.Render())
	}
}

func TestCompareUnknownStates(t *testing.T) {
	gitAvailable(t)
	nonRepo := t.TempDir()
	c := NewComparer(nonRepo, 500*time.Millisecond)
	if f := c.Compare("main", "abc123"); f.Signal != Unknown {
		t.Errorf("non-repo Signal = %s, want unknown", f.Signal)
	}

	repo := initRepo(t)
	c2 := NewComparer(repo, 500*time.Millisecond)
	if f := c2.Compare("", ""); f.Signal != Unknown {
		t.Errorf("no capture state Signal = %s, want unknown", f.Signal)
	}
	if f := c2.Compare("", ""); f.Render() != "unknown: no git state" {
		t.Errorf("Render = %q", f.Render())
	}
}

func TestCompareDegradesWithoutGitBinary(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)
	old := Capture(dir)
	run(t, dir, "commit", "--allow-empty", "-m", "two")

	c := NewComparer(dir, 2*time.Second)
	c.gitPath = "" // simulate git absent after state capture
	f := c.Compare(old.Branch, old.Commit)
	if f.Signal != MovedOn || f.CommitsSince != -1 {
		t.Errorf("degraded = %+v, want moved-on with unknown distance", f)
	}
	if f.Render() != "moved-on: distance unknown" {
		t.Errorf("Render = %q", f.Render())
	}
}

func TestCompareExhaustedBudget(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)
	old := Capture(dir)
	run(t, dir, "commit", "--allow-empty", "-m", "two")

	c := NewComparer(dir, -time.Second) // already past deadline
	f := c.Compare(old.Branch, old.Commit)
	if f.Signal != MovedOn || f.CommitsSince != -1 {
		t.Errorf("budget-exhausted = %+v, want moved-on with unknown distance", f)
	}
}
