// Package fsutil holds small file-system helpers shared by the packages that
// rewrite user files in place.
package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"syscall"
	"time"
)

// Rename is os.Rename, retried for up to two seconds on Windows. There,
// replacing a file that another process or goroutine has open (a hook reading
// the config, a second install writing it) fails with "Access is denied" or a
// sharing violation until that handle closes; on other systems the rename is
// atomic and never retried.
func Rename(oldpath, newpath string) error {
	err := os.Rename(oldpath, newpath)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for delay := 5 * time.Millisecond; transient(err) && time.Now().Before(deadline); delay *= 2 {
		time.Sleep(delay)
		err = os.Rename(oldpath, newpath)
	}
	return err
}

// transient reports ERROR_ACCESS_DENIED and ERROR_SHARING_VIOLATION (32).
func transient(err error) bool {
	var errno syscall.Errno
	return errors.Is(err, fs.ErrPermission) || errors.As(err, &errno) && errno == 32
}
