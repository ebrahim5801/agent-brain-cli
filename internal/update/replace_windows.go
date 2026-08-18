//go:build windows

package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// replace swaps newPath into target on Windows, which cannot overwrite a running
// .exe but CAN rename it. We rename the running binary aside to .old (allowed
// while running), move the new one into place, then best-effort delete the old
// image. cleanupStale removes a leftover .old on the next startup.
func replace(newPath, target string) error {
	old := target + ".old"
	_ = os.Remove(old)
	if err := os.Rename(target, old); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return &ErrManualInstall{
				TempPath: newPath,
				Target:   target,
				Hint:     fmt.Sprintf("cannot replace %s (permission denied); move %s into place as Administrator", target, newPath),
			}
		}
		return fmt.Errorf("rename running binary aside: %w", err)
	}
	if err := os.Rename(newPath, target); err != nil {
		// Roll back so the install is never left without a binary.
		_ = os.Rename(old, target)
		return fmt.Errorf("move new binary into place: %w", err)
	}
	_ = os.Remove(old) // may fail while the old image is mapped; cleaned up next run
	return nil
}

// CleanupStale removes a leftover .old image left by a prior Windows replace.
func CleanupStale(target string) {
	_ = os.Remove(target + ".old")
}
