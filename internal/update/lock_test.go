package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestTryLockExcludesSecondHolder(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	unlock, ok := TryLock()
	if !ok {
		t.Fatal("first TryLock failed")
	}
	if _, ok := TryLock(); ok {
		t.Fatal("second TryLock succeeded while held")
	}
	unlock()
	unlock2, ok := TryLock()
	if !ok {
		t.Fatal("TryLock failed after release")
	}
	unlock2()
}

func TestTryLockClearsStaleLock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	path := filepath.Join(os.TempDir(), "agent-brain-update.lock")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-lockStaleAfter - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	unlock, ok := TryLock()
	if !ok {
		t.Fatal("stale lock was not cleared")
	}
	unlock()
}

func TestCanReplaceTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "agent-brain")
	if err := os.WriteFile(target, []byte("BIN"), 0o755); err != nil {
		t.Fatal(err)
	}
	swapExecutable(t, target)
	if !CanReplaceTarget() {
		t.Fatal("writable dir reported not replaceable")
	}

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("read-only dir check needs non-root unix")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if CanReplaceTarget() {
		t.Fatal("read-only dir reported replaceable")
	}
}
