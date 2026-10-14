package memory

import (
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func TestRelevantDurableDecisionOutranksRecentCriticalDistractor(t *testing.T) {
	st, pid := openStore(t)
	now := time.Now().UTC()
	dir := t.TempDir()
	want, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Kind: "decision", Origin: "auto", Content: "Use PostgreSQL transaction locks because concurrent migrations serialize across processes"}, now.Add(-60*24*time.Hour).Format(store.TimeLayout))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Kind: "task_state", Origin: "auto", Priority: "critical", Content: "postgres meeting notes: concurrent sidebar styling awaits a color decision"}, now.Format(store.TimeLayout)); err != nil {
		t.Fatal(err)
	}
	found, err := Search(st, pid, dir, "postgres concurrent migrations", Filter{}, 10, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 || found[0].Entry.ID != want {
		t.Fatalf("relevant old decision buried: %+v", found)
	}
}

func TestBootstrapPreservesDurableDecisionAheadOfAgedTaskNoise(t *testing.T) {
	st, pid := openStore(t)
	dir := t.TempDir()
	now := time.Now()
	id, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "Keep the durable invoice policy", Kind: "decision", Origin: "auto"}, now.Add(-180*24*time.Hour).Format(store.TimeLayout))
	if err != nil {
		t.Fatal(err)
	}
	for range 30 {
		if _, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "Old sidebar styling remains in flight", Kind: "task_state", Origin: "auto"}, now.Add(-60*24*time.Hour).Format(store.TimeLayout)); err != nil {
			t.Fatal(err)
		}
	}
	pack, err := BuildPack(st, pid, dir, 300, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, served := range pack.PersonalIDs {
		found = found || served == id
	}
	if !found {
		t.Fatal("old durable decision lost to aged task noise")
	}
}
