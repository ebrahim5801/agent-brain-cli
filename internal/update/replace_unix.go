//go:build !windows

package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// replace atomically moves newPath over target. On Linux/macOS the running
// process keeps its open inode, so overwriting the on-disk file is safe. A
// permission error on the target directory (e.g. /usr/local/bin owned by root)
// is reported as *ErrManualInstall with the exact sudo command; the verified
// binary is left at newPath so the user can finish manually.
func replace(newPath, target string) error {
	if err := os.Rename(newPath, target); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return &ErrManualInstall{
				TempPath: newPath,
				Target:   target,
				Hint:     fmt.Sprintf("cannot write %s (permission denied); finish with:\n  sudo install -m 0755 %s %s", target, newPath, target),
			}
		}
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// CleanupStale is a no-op on unix (no rename-aside leftover to clean).
func CleanupStale(string) {}
