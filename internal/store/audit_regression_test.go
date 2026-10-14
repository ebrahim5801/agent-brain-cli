package store_test

import (
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func TestTeamPullCannotMutateAnotherProject(t *testing.T) {
	for _, b := range storetest.Both(t) {
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			p1, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/one"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			p2, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/two"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			row := store.TeamMemoryRow{UID: "same-uid", Author: "author", Content: "original", Kind: "fact", Origin: "auto", Status: "active", CapturedAt: store.Now(), UpdatedAt: store.Now()}
			if err := st.ApplyPull(p1, []store.TeamMemoryRow{row}); err != nil {
				t.Fatal(err)
			}
			row.Content = "from another project"
			_ = st.ApplyPull(p2, []store.TeamMemoryRow{row})
			got, err := st.ListTeamMemories(p1)
			if err != nil || len(got) != 1 || got[0].Content != "original" {
				t.Fatalf("foreign update changed project one's cache: %v", err)
			}
			row.Status = "deleted"
			_ = st.ApplyPull(p2, []store.TeamMemoryRow{row})
			got, err = st.ListTeamMemories(p1)
			if err != nil || len(got) != 1 {
				t.Fatalf("foreign tombstone deleted project one's cache: %v", err)
			}
		})
	}
}

func TestOwnTeamMemoryFromAnotherDeviceIsServed(t *testing.T) {
	for _, b := range storetest.Both(t) {
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/project"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			id, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "local", Kind: "fact", Origin: "auto", ShareLive: true}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			var localUID string
			if err := st.QueryRow("SELECT team_uid FROM memories WHERE id = ?", id).Scan(&localUID); err != nil {
				t.Fatal(err)
			}
			var rows []store.TeamMemoryRow
			for _, uid := range []string{localUID, "from-other-device"} {
				rows = append(rows, store.TeamMemoryRow{UID: uid, Author: "self", Mine: true, Content: "own team entry", Kind: "fact", Origin: "auto", Status: "active", CapturedAt: store.Now(), UpdatedAt: store.Now()})
			}
			if err := st.ApplyPull(pid, rows); err != nil {
				t.Fatal(err)
			}
			got, err := st.ListTeamMemories(pid)
			if err != nil || len(got) != 1 || got[0].UID != "from-other-device" {
				t.Fatalf("want own remote entry, excluding local duplicate: got %d entries, %v", len(got), err)
			}
		})
	}
}
