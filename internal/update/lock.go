package update

import (
	"os"
	"path/filepath"
	"time"
)

// lockStaleAfter bounds how long a crashed updater can block later ones: a lock
// older than this is presumed abandoned (an update takes seconds, not minutes).
const lockStaleAfter = 15 * time.Minute

// TryLock guards against two updaters racing the binary replace (e.g. two
// interactive commands both spawning a background update). It returns a release
// func and true when the lock was acquired; false means another update is
// already running and the caller should simply exit.
func TryLock() (func(), bool) {
	path := filepath.Join(os.TempDir(), "agent-brain-update.lock")
	for range 2 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, true
		}
		fi, statErr := os.Stat(path)
		if statErr != nil || time.Since(fi.ModTime()) < lockStaleAfter {
			return nil, false
		}
		// Abandoned lock from a crashed updater: clear it and try once more.
		_ = os.Remove(path)
	}
	return nil, false
}
