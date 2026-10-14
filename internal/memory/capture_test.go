package memory_test

import (
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func TestCaptureRecoveryRequiresEveryPersistedItemAndDeduplicatesRetries(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.Store
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			b, err := st.EnsureBoundSession(pid, "codex", "capture", dir, "", "")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			batch, err := st.RequestCapture(b, "checkpoint", 25, now)
			if err != nil {
				t.Fatal(err)
			}
			batch, err = st.BeginCapture(batch.ID, b, []string{"decision", "task"}, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.CompleteCapture(batch.ID, b, batch.Generation, false, now); err == nil {
				t.Fatal("incomplete capture marked successful")
			}
			in := memory.SaveInput{ProjectID: pid, SessionID: b.SessionID, SessionHandle: b.Handle, Dir: dir, Content: "Use local durable retries", Kind: "decision", Origin: "auto", OperationID: batch.Items[0].OperationID, BatchID: batch.ID, Generation: batch.Generation}
			first, err := memory.Save(st, in)
			if err != nil {
				t.Fatal(err)
			}
			retry, err := memory.Save(st, in)
			if err != nil || first.ID != retry.ID {
				t.Fatalf("lost-ack retry duplicated memory: %v", err)
			}
			changed := in
			changed.Content = "different payload"
			if _, err := memory.Save(st, changed); err == nil {
				t.Fatal("operation reuse accepted different content")
			}
			resumed, err := st.BeginCapture(batch.ID, b, []string{"task", "decision"}, now)
			if err != nil || resumed.Generation != batch.Generation {
				t.Fatalf("resume changed identity: %v", err)
			}
			in.OperationID = batch.Items[1].OperationID
			in.Content = "Retry test remains unfinished"
			in.Kind = "task_state"
			if _, err := memory.Save(st, in); err != nil {
				t.Fatal(err)
			}
			done, err := st.CompleteCapture(batch.ID, b, batch.Generation, false, now)
			if err != nil || done.State != "persisted" {
				t.Fatalf("capture not persisted: %v", err)
			}
			rows, err := st.ListMemories(pid, true)
			if err != nil || len(rows) != 2 {
				t.Fatalf("duplicate or missing records: %d %v", len(rows), err)
			}
		})
	}
}

func TestEmptyCaptureAndExpiredLeaseAreHonest(t *testing.T) {
	st := storetest.SQLite(t)
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.EnsureBoundSession(pid, "claude-code", "empty", dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	batch, err := st.RequestCapture(b, "stop", 1, now)
	if err != nil {
		t.Fatal(err)
	}
	batch, err = st.BeginCapture(batch.ID, b, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteCapture(batch.ID, b, batch.Generation, false, now); err == nil {
		t.Fatal("empty capture claimed saves")
	}
	if _, err := st.CompleteCapture(batch.ID, b, batch.Generation, true, now.Add(3*time.Minute)); err == nil {
		t.Fatal("expired worker completed capture")
	}
	batch, err = st.BeginCapture(batch.ID, b, nil, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	done, err := st.CompleteCapture(batch.ID, b, batch.Generation, true, now.Add(3*time.Minute))
	if err != nil || done.State != "no_durable_changes" {
		t.Fatalf("empty outcome: %v", err)
	}
}
