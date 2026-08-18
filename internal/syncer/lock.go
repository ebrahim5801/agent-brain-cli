package syncer

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
)

const (
	syncLockWait  = 10 * time.Second
	syncLockStale = 10 * time.Minute
	syncLockRetry = 100 * time.Millisecond
)

// lockSync serializes uploaders on this machine. Stale locks (crashed
// process) are stolen after syncLockStale.
func lockSync() (func(), error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "sync.lock")
	deadline := time.Now().Add(syncLockWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if fi, statErr := os.Stat(path); statErr == nil && time.Since(fi.ModTime()) > syncLockStale {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another sync is already running (%s)", path)
		}
		time.Sleep(syncLockRetry)
	}
}
