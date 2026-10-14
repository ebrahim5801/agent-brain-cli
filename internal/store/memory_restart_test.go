package store_test

import (
	"errors"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"path/filepath"
	"testing"
)

func TestPreUpgradeReadersCannotServeLegacyStateAfterActivation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	reader, err := store.OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	pid, err := reader.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: t.TempDir()}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	upgrader, err := store.OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgrader.Close()
	if err := upgrader.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.MemorySnapshot(pid, nil); !errors.Is(err, store.ErrMemoryRestartRequired) {
		t.Fatalf("stale snapshot capability accepted: %v", err)
	}
	if _, err := reader.ListMemories(pid, false); !errors.Is(err, store.ErrMemoryRestartRequired) {
		t.Fatalf("stale current-state list accepted: %v", err)
	}
	if _, err := reader.PendingContributions(pid); !errors.Is(err, store.ErrMemoryRestartRequired) {
		t.Fatalf("stale sharing selection accepted: %v", err)
	}
}
