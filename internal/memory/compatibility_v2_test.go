package memory_test

import (
	"errors"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
	"testing"
)

func TestCompatibilityFloorDoesNotSilentlyDiscardV2Contracts(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.Store
			dir := t.TempDir()
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			basic := memory.SaveInput{ProjectID: pid, Dir: dir, Content: "Keep legacy content readable", Kind: "decision", Origin: "auto"}
			if _, err := memory.Save(st, basic); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"operation", "rationale", "evidence", "relations"} {
				in := basic
				switch field {
				case "operation":
					in.OperationID = "needs-durable-receipt"
				case "rationale":
					in.Rationale = "Must not lose this explanation"
				case "evidence":
					in.Evidence = []memory.EvidenceInput{{Type: "assertion", Reference: "relevant-source"}}
				case "relations":
					in.Relations = []store.NewRelation{{TargetID: 1, Type: "supports"}}
				}
				if _, err := memory.Save(st, in); !errors.Is(err, store.ErrV2Required) {
					t.Fatalf("%s contract not refused before write: %v", field, err)
				}
			}
			var n int
			if err := st.QueryRow(`SELECT COUNT(*) FROM memories WHERE project_id=?`, pid).Scan(&n); err != nil || n != 1 {
				t.Fatalf("unsupported saves partially persisted: %d %v", n, err)
			}
			version, err := st.SchemaVersion()
			if err != nil || version != 15 {
				t.Fatal("save implicitly upgraded schema")
			}
		})
	}
}
