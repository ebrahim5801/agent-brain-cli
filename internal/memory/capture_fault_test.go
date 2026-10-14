package memory_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func TestCaptureConcurrentRetriesFaultAndRecovery(t *testing.T) {
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
			source, err := st.EnsureBoundSession(pid, "codex", "source", dir, "", "")
			if err != nil {
				t.Fatal(err)
			}
			executor, err := st.EnsureBoundSession(pid, "claude-code", "recovery", dir, "", "")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			batch, err := st.RequestCapture(source, "checkpoint", 25, now)
			if err != nil {
				t.Fatal(err)
			}
			batch, err = st.BeginCapture(batch.ID, source, []string{"choice"}, now)
			if err != nil {
				t.Fatal(err)
			}
			in := memory.SaveInput{ProjectID: pid, SessionHandle: source.Handle, Dir: dir, Content: "Keep retries durable", Kind: "decision", Origin: "auto", BatchID: batch.ID, OperationID: batch.Items[0].OperationID, Generation: batch.Generation}
			if st.Backend == store.BackendSQLite {
				_, err = st.Exec(`CREATE TRIGGER v2_fault BEFORE INSERT ON memory_details BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
			} else {
				_, err = st.Exec(`CREATE FUNCTION v2_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$; CREATE TRIGGER v2_fault BEFORE INSERT ON memory_details FOR EACH ROW EXECUTE FUNCTION v2_fault()`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := memory.Save(st, in); err == nil {
				t.Fatal("partial failure acknowledged")
			}
			var count int
			if err := st.QueryRow(`SELECT COUNT(*) FROM memories WHERE project_id=?`, pid).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial memory leaked: %d %v", count, err)
			}
			status, err := st.CaptureStatus(batch.ID, pid)
			if err != nil || status.Items[0].State != "pending" {
				t.Fatalf("receipt falsely persisted: %+v %v", status, err)
			}
			if st.Backend == store.BackendSQLite {
				_, err = st.Exec(`DROP TRIGGER v2_fault`)
			} else {
				_, err = st.Exec(`DROP TRIGGER v2_fault ON memory_details; DROP FUNCTION v2_fault()`)
			}
			if err != nil {
				t.Fatal(err)
			}
			deferred, err := st.DeferCapture(batch.ID, source, batch.Generation, "persistence_retry", now)
			if err != nil || deferred.State != "retry_wait" {
				t.Fatalf("retry outcome: %+v %v", deferred, err)
			}
			if _, err := st.BeginCapture(batch.ID, executor, []string{"choice"}, now); err == nil {
				t.Fatal("retry backoff bypassed")
			}
			batch, err = st.BeginCapture(batch.ID, executor, []string{"choice"}, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			in.SessionHandle = executor.Handle
			in.Generation = batch.Generation
			var wg sync.WaitGroup
			results := make(chan memory.SaveResult, 8)
			failures := make(chan error, 8)
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r, err := memory.Save(st, in)
					if err != nil {
						failures <- err
					} else {
						results <- r
					}
				}()
			}
			wg.Wait()
			close(results)
			close(failures)
			for err := range failures {
				t.Error(err)
			}
			var id int64
			for r := range results {
				if id != 0 && id != r.ID {
					t.Fatal("duplicate operation created multiple memories")
				}
				id = r.ID
			}
			if id == 0 {
				t.Fatal("no persisted result")
			}
			m, err := st.GetMemory(id)
			if err != nil || m.SessionID.Int64 != source.SessionID {
				t.Fatalf("recovery attributed to executor: %+v %v", m, err)
			}
			if _, err := st.CompleteCapture(batch.ID, executor, batch.Generation, false, now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if _, err := st.BeginCapture(batch.ID, executor, []string{"different-manifest"}, now.Add(time.Minute)); err == nil {
				t.Fatal("terminal manifest changed")
			}
			if err := st.QueryRow(`SELECT COUNT(*) FROM memories WHERE project_id=?`, pid).Scan(&count); err != nil || count != 1 {
				t.Fatalf("retry duplicates: %d %v", count, err)
			}
		})
	}
}

// This worker is a real process whose committed response is deliberately lost.
func TestV2PersistenceWorker(t *testing.T) {
	path := os.Getenv("AB_V2_FAULT_DB")
	dsn := os.Getenv("AB_V2_FAULT_DSN")
	if path == "" && dsn == "" {
		return
	}
	var st *store.Store
	var err error
	if dsn != "" {
		st, err = store.OpenPostgres(dsn)
	} else {
		st, err = store.OpenAt(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	var pid int64
	if err := st.QueryRow(`SELECT id FROM projects LIMIT 1`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("AB_V2_FAULT_STAGE") == "before_commit" {
		tx, err := st.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(st.Rebind(`INSERT INTO memories(project_id,content,kind,origin,priority,status,captured_at,updated_at) VALUES(?,'uncommitted','fact','auto','normal','active',?,?)`), pid, store.Now(), store.Now()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".ready", []byte("transaction open"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		return
	}
	result, err := memory.Save(st, memory.SaveInput{ProjectID: pid, Dir: filepath.Dir(path), Content: "Durable despite a lost reply", Kind: "fact", Origin: "auto", OperationID: "restart-operation"})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("AB_V2_FAULT_STAGE") == "after_ack" {
		fmt.Print(result.ID)
		st.Close()
		return
	}
	os.Exit(77)
}

func TestAcknowledgedAndLostReplyWritesSurviveProcessRestart(t *testing.T) {
	for _, stage := range []string{"before_commit", "after_commit", "after_ack"} {
		t.Run(stage, func(t *testing.T) {
			st := storetest.SQLite(t)
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			path := st.Path
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: filepath.Dir(path)}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestV2PersistenceWorker$")
			cmd.Env = append(os.Environ(), "AB_V2_FAULT_DB="+path, "AB_V2_FAULT_STAGE="+stage)
			if stage == "before_commit" {
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(path + ".ready"); err == nil {
						break
					}
					if time.Now().After(deadline) {
						_ = cmd.Process.Kill()
						t.Fatal("worker failed to open transaction")
					}
					time.Sleep(10 * time.Millisecond)
				}
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			} else {
				raw, err := cmd.CombinedOutput()
				if stage == "after_ack" && err != nil {
					t.Fatalf("worker: %s %v", raw, err)
				}
				if stage == "after_commit" {
					if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 77 {
						t.Fatalf("expected lost reply: %s %v", raw, err)
					}
				}
			}
			st.Close()
			reopened, err := store.OpenAt(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			var n int
			if err := reopened.QueryRow(`SELECT COUNT(*) FROM memories WHERE project_id=?`, pid).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if stage == "before_commit" {
				if n != 0 {
					t.Fatal("killed uncommitted transaction persisted")
				}
				return
			}
			if n != 1 {
				t.Fatalf("committed write lost: %d", n)
			}
			retry, err := memory.Save(reopened, memory.SaveInput{ProjectID: pid, Dir: filepath.Dir(path), Content: "Durable despite a lost reply", Kind: "fact", Origin: "auto", OperationID: "restart-operation"})
			if err != nil || retry.ID == 0 {
				t.Fatalf("restart receipt lost: %+v %v", retry, err)
			}
			if err := reopened.QueryRow(`SELECT COUNT(*) FROM memories WHERE project_id=?`, pid).Scan(&n); err != nil || n != 1 {
				t.Fatal("restart retry duplicated write")
			}
		})
	}
}

func TestPostgresAcknowledgmentAndProcessDeath(t *testing.T) {
	for _, stage := range []string{"before_commit", "after_commit", "after_ack"} {
		t.Run(stage, func(t *testing.T) {
			dsn := storetest.PostgresDSN(t)
			st, err := store.OpenPostgres(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "pg-marker")
			cmd := exec.Command(os.Args[0], "-test.run=^TestV2PersistenceWorker$")
			cmd.Env = append(os.Environ(), "AB_V2_FAULT_DB="+marker, "AB_V2_FAULT_DSN="+dsn, "AB_V2_FAULT_STAGE="+stage)
			if stage == "before_commit" {
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(marker + ".ready"); err == nil {
						break
					}
					if time.Now().After(deadline) {
						_ = cmd.Process.Kill()
						t.Fatal("PG worker did not open transaction")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Wait()
			} else {
				raw, err := cmd.CombinedOutput()
				if stage == "after_ack" && err != nil {
					t.Fatalf("worker: %s %v", raw, err)
				}
				if stage == "after_commit" {
					if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 77 {
						t.Fatalf("expected lost PG reply: %s %v", raw, err)
					}
				}
			}
			st.Close()
			st, err = store.OpenPostgres(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			var n int
			if err := st.QueryRow(`SELECT COUNT(*) FROM memories WHERE project_id=?`, pid).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if stage == "before_commit" {
				if n != 0 {
					t.Fatal("uncommitted PG memory survived")
				}
				return
			}
			if n != 1 {
				t.Fatalf("PG acknowledged memory lost: %d", n)
			}
			retry, err := memory.Save(st, memory.SaveInput{ProjectID: pid, Dir: dir, Content: "Durable despite a lost reply", Kind: "fact", Origin: "auto", OperationID: "restart-operation"})
			if err != nil || retry.ID == 0 {
				t.Fatalf("PG receipt not recoverable: %+v %v", retry, err)
			}
			if err := st.QueryRow(`SELECT COUNT(*) FROM memories WHERE project_id=?`, pid).Scan(&n); err != nil || n != 1 {
				t.Fatal("PG retry duplicated memory")
			}
		})
	}
}
