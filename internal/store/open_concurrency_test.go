package store_test

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func TestConcurrentStoreOpen(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "brain.db")
		concurrentOpen(t, func() (*store.Store, error) { return store.OpenAt(path) })
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := storetest.PostgresDSN(t)
		concurrentOpen(t, func() (*store.Store, error) { return store.OpenPostgres(dsn) })
	})
}

func concurrentOpen(t *testing.T, open func() (*store.Store, error)) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			st, err := open()
			if err != nil {
				t.Errorf("concurrent open failed: %v", err)
				return
			}
			defer st.Close()
			if _, err := st.ListMemories(1, false); err != nil {
				t.Errorf("incomplete schema: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
}

func TestNewSQLiteStoreIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	path := filepath.Join(t.TempDir(), "new", "brain.db")
	st, err := store.OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s readable by other users: %o", filepath.Base(p), fi.Mode().Perm())
		}
	}
}

func TestSQLiteStoreUsesWAL(t *testing.T) {
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var mode string
	if err := st.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}
