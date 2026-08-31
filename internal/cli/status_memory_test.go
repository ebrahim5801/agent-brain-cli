package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func capture(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func priorityMixStore(t *testing.T, criticals, normals int) (*store.Store, int64) {
	t.Helper()
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	at := store.Now()
	projectID, err := st.UpsertProject(store.ProjectIdentity{Kind: "remote",
		Identity: "gitlab.com/acme/mix", DisplayName: "mix"}, at)
	if err != nil {
		t.Fatal(err)
	}
	add := func(n int, priority string) {
		for i := 0; i < n; i++ {
			if _, err := st.InsertMemory(store.NewMemory{ProjectID: projectID, Content: "x",
				Kind: "fact", Origin: "auto", Priority: priority}, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(criticals, store.MemoryPriorityCritical)
	add(normals, store.MemoryPriorityNormal)
	return st, projectID
}

// Self-assigned priority is only useful while critical stays rare. The mix is
// printed once anything is classified, and crossing the ceiling says so: a
// corpus that is mostly critical has handed the pack to a noisy signal and
// demoted recency for nothing.
func TestPrintMemoryPriorityMix(t *testing.T) {
	st, projectID := priorityMixStore(t, 1, 99)
	out := capture(t, func() { printMemoryPriorityMix(st, projectID) })
	if !strings.Contains(out, "1 critical, 99 normal, 0 background") {
		t.Errorf("mix line = %q", out)
	}
	if strings.Contains(out, "losing its meaning") {
		t.Errorf("warned at 1%%: %q", out)
	}

	loud, loudID := priorityMixStore(t, 30, 70)
	out = capture(t, func() { printMemoryPriorityMix(loud, loudID) })
	if !strings.Contains(out, "30% of entries are critical") || !strings.Contains(out, "losing its meaning") {
		t.Errorf("no calibration warning at 30%%: %q", out)
	}
}

// A project that has never classified anything gets no line at all — the check
// must not add noise to every status call before the feature is used.
func TestPrintMemoryPriorityMixSilentWhenAllDefault(t *testing.T) {
	st, projectID := priorityMixStore(t, 0, 5)
	if out := capture(t, func() { printMemoryPriorityMix(st, projectID) }); out != "" {
		t.Errorf("unclassified project printed %q", out)
	}
}
