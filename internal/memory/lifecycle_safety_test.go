package memory_test

import (
	"fmt"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
	"strings"
	"testing"
	"time"
)

func TestLifecycleCASRollbackSharingAndCorrectionRefresh(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.Store
			dir := t.TempDir()
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			old, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "Use historical invoice queue", Kind: "decision", Origin: "auto", ShareLive: true}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			before, _ := st.GetMemory(old)
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			content := "Use current invoice queue"
			path, e := userEvidence(t, dir, "user", fmt.Sprintf("Replace #%d: %s", old, content))
			b, err := st.EnsureBoundSession(pid, "codex", "first", dir, path, "")
			if err != nil {
				t.Fatal(err)
			}
			pack, err := memory.BuildPack(st, pid, dir, 2000, 0, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range pack.PersonalIDs {
				if err := st.RecordRetrievalRevision(b.SessionID, pid, "personal", id, "", pack.PersonalRevisions[id], store.Now()); err != nil {
					t.Fatal(err)
				}
			}
			in := memory.SaveInput{ProjectID: pid, SessionHandle: b.Handle, Dir: dir, Content: content, Kind: "decision", Origin: "auto", OperationID: "accepted-operation", Rationale: "Improved durable receipts", Subject: "invoice-queue", Supersedes: []int64{old}, Evidence: []memory.EvidenceInput{e}, ShareLive: true}
			r, err := memory.Save(st, in)
			if err != nil || len(r.Superseded) != 1 {
				t.Fatalf("replacement: %+v %v", r, err)
			}
			retry, err := memory.Save(st, in)
			if err != nil || retry.ID != r.ID || len(retry.Superseded) != 1 {
				t.Fatalf("lifecycle replay changed receipt: %+v %v", retry, err)
			}
			refresh, err := memory.BuildRevisionRefresh(st, pid, b.SessionID, dir, 2000, 0, time.Now())
			if err != nil || !strings.Contains(refresh.Text, "lifecycle: superseded") || !strings.Contains(refresh.Text, content) {
				t.Fatalf("correction not refreshed: %s %v", refresh.Text, err)
			}
			pending, err := st.PendingContributions(pid)
			if err != nil || len(pending) != 1 || pending[0].Content != content || len(pending[0].Supersedes) != 1 {
				t.Fatalf("sharing ignored overlays: %+v %v", pending, err)
			}
			t.Setenv("AGENT_BRAIN_MEMORY_V2_RANKING", "off")
			found, err := memory.Search(st, pid, dir, "historical", memory.Filter{}, 10, 0, time.Now())
			if err != nil || len(found) != 1 || found[0].Entry.ID != r.ID {
				t.Fatalf("rollback ignored lifecycle: %+v %v", found, err)
			}
			after, _ := st.GetMemory(old)
			if before != after {
				t.Fatal("historical row rewritten")
			}

			// Even a freshly-read revision cannot revive a retired physical
			// row: it is preserved as active on disk, but no longer a head.
			historyBefore, err := st.MemorySnapshot(pid, nil)
			if err != nil {
				t.Fatal(err)
			}
			var oldRevision int64
			for _, m := range historyBefore.Entries {
				if m.ID == old {
					oldRevision = m.Revision
				}
			}
			repeated := in
			repeated.OperationID = "different-operation-on-retired-head"
			repeated.ExpectedRevision = oldRevision
			rejected, err := memory.Save(st, repeated)
			if err != nil || len(rejected.Superseded) > 0 || rejected.Rejected[old] == "" {
				t.Fatalf("retired head accepted again: %+v %v", rejected, err)
			}
			// A new caller with a stale revision cannot create another accepted head.
			path, e = userEvidence(t, dir, "user", fmt.Sprintf("Replace #%d: Use a concurrent invoice queue", r.ID))
			c, err := st.EnsureBoundSession(pid, "codex", "stale", dir, path, "")
			if err != nil {
				t.Fatal(err)
			}
			stale, err := memory.Save(st, memory.SaveInput{ProjectID: pid, SessionHandle: c.Handle, Dir: dir, Content: "Use a concurrent invoice queue", Kind: "decision", Origin: "auto", Supersedes: []int64{r.ID}, Evidence: []memory.EvidenceInput{e}})
			if err != nil || len(stale.Superseded) != 0 || len(stale.Pending) != 1 {
				t.Fatalf("stale head accepted: %+v %v", stale, err)
			}
			found, err = memory.Search(st, pid, dir, "concurrent", memory.Filter{}, 10, 0, time.Now())
			if err != nil || len(found) < 2 {
				t.Fatalf("unresolved peer omitted: %+v %v", found, err)
			}
			if err := st.RestoreMemory(old, store.Now()); err != nil {
				t.Fatal(err)
			}
			history, err := st.MemorySnapshot(pid, nil)
			if err != nil {
				t.Fatal(err)
			}
			restored := false
			for _, m := range history.Entries {
				restored = restored || (m.ID == old && m.Status == "active" && m.Conflicted)
			}
			if !restored {
				t.Fatal("undo did not preserve conflict/history")
			}
		})
	}
}

func TestAutomationOffAndTypedRelationsCannotRetireHistory(t *testing.T) {
	st := storetest.SQLite(t)
	dir := t.TempDir()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	old, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "Keep private local data", Kind: "decision", Origin: "auto"}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	content := "Use local privacy controls"
	path, e := userEvidence(t, dir, "user", fmt.Sprintf("Replace #%d: %s", old, content))
	b, err := st.EnsureBoundSession(pid, "codex", "rollback", dir, path, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_BRAIN_MEMORY_V2_AUTOMATION", "off")
	r, err := memory.Save(st, memory.SaveInput{ProjectID: pid, SessionHandle: b.Handle, Dir: dir, Content: content, Kind: "decision", Origin: "auto", Supersedes: []int64{old}, Evidence: []memory.EvidenceInput{e}, Relations: []store.NewRelation{{TargetID: old, Type: "supports"}}})
	if err != nil || len(r.Superseded) > 0 {
		t.Fatalf("automation disabled but retired: %+v %v", r, err)
	}
	snap, err := st.MemorySnapshot(pid, nil)
	if err != nil || len(snap.Relations) != 2 {
		t.Fatalf("relationships not retained: %+v %v", snap, err)
	}
	for _, m := range snap.Entries {
		if m.Status != "active" {
			t.Fatal("disabled automation changed current history")
		}
	}
	foreign, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: t.TempDir()}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.InsertMemory(store.NewMemory{ProjectID: foreign, Content: "Other private data", Kind: "fact", Origin: "auto"}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Save(st, memory.SaveInput{ProjectID: pid, Dir: dir, Content: "Invalid relationship", Kind: "fact", Origin: "auto", Relations: []store.NewRelation{{TargetID: target, Type: "derived_from"}}}); err == nil {
		t.Fatal("cross-project relation accepted")
	}
}

func TestEditedDeletedAndCompletedCorrectionsRefresh(t *testing.T) {
	st := storetest.SQLite(t)
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.EnsureBoundSession(pid, "codex", "refresh", dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := memory.Save(st, memory.SaveInput{ProjectID: pid, Dir: dir, Content: "Original durable fact", Kind: "fact", Origin: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRetrievalRevision(b.SessionID, pid, "personal", r.ID, "", 0, store.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateMemoryContent(r.ID, "Corrected durable fact", store.Now()); err != nil {
		t.Fatal(err)
	}
	pack, err := memory.BuildRevisionRefresh(st, pid, b.SessionID, dir, 2000, 0, time.Now())
	if err != nil || !strings.Contains(pack.Text, "Corrected durable fact") {
		t.Fatalf("edit not refreshed: %s %v", pack.Text, err)
	}
	if err := st.RecordRetrievalRevision(b.SessionID, pid, "personal", r.ID, "", pack.PersonalRevisions[r.ID], store.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteMemory(r.ID, store.Now()); err != nil {
		t.Fatal(err)
	}
	pack, err = memory.BuildRevisionRefresh(st, pid, b.SessionID, dir, 2000, 0, time.Now())
	if err != nil || !strings.Contains(pack.Text, "lifecycle: deleted") {
		t.Fatalf("removal not refreshed: %s %v", pack.Text, err)
	}
}
