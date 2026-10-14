package memory

import (
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func TestSaveRollsBackWhenSupersedeFails(t *testing.T) {
	for _, b := range storetest.Both(t) {
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/atomic"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			in := SaveInput{ProjectID: pid, Dir: t.TempDir(), Content: "original decision", Kind: "decision", Origin: "auto"}
			old, err := Save(st, in)
			if err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER fail_supersede BEFORE UPDATE OF status ON memories BEGIN SELECT RAISE(ABORT, 'simulated write failure'); END`
			if st.Backend == store.BackendPostgres {
				trigger = `CREATE FUNCTION fail_supersede() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'simulated write failure'; END $$;
CREATE TRIGGER fail_supersede BEFORE UPDATE OF status ON memories FOR EACH ROW EXECUTE FUNCTION fail_supersede()`
			}
			if _, err := st.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			in.Content, in.Supersedes = "replacement decision", []int64{old.ID}
			if _, _, _, err := st.InsertMemorySuperseding(store.NewMemory{ProjectID: pid, Content: in.Content, Kind: in.Kind, Origin: in.Origin}, in.Supersedes, nil, store.Now()); err == nil {
				t.Fatal("expected injected write failure")
			}
			rows, err := st.ListMemories(pid, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].ID != old.ID || rows[0].Status != store.MemoryActive {
				t.Fatalf("failed save left partial state: %d rows", len(rows))
			}
		})
	}
}

func TestContentLimitCountsCharacters(t *testing.T) {
	st, pid := openStore(t)
	content := strings.Repeat("ذ", MaxContentLen)
	res, err := Save(st, SaveInput{ProjectID: pid, Dir: t.TempDir(), Content: content, Kind: "fact", Origin: "auto"})
	if err != nil {
		t.Fatalf("valid 4000-character memory rejected: %v", err)
	}
	if err := Edit(st, res.ID, content); err != nil {
		t.Fatal(err)
	}
	if err := Edit(st, res.ID, content+"ذ"); err != ErrContentTooLong {
		t.Fatalf("over-limit edit: %v", err)
	}
}
