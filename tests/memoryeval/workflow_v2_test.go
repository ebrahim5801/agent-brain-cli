//go:build !memoryv2baseline

package memoryeval

import (
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"testing"
	"time"
)

const baselineWorkflow = false

type workflowDriver struct {
	st       *store.Store
	pid      int64
	dir      string
	bindings map[string]store.SessionBinding
}

func newWorkflowDriver(t *testing.T, st *store.Store, pid int64, dir string) *workflowDriver {
	t.Helper()
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	return &workflowDriver{st: st, pid: pid, dir: dir, bindings: map[string]store.SessionBinding{}}
}
func (d *workflowDriver) start(host, native, path string) (string, error) {
	b, err := d.st.EnsureBoundSession(d.pid, host, native, d.dir, path, "")
	d.bindings[b.Handle] = b
	return b.Handle, err
}
func (d *workflowDriver) save(in workflowInput) (workflowResult, error) {
	var evidence []memory.EvidenceInput
	for _, e := range in.evidence {
		evidence = append(evidence, memory.EvidenceInput{Type: e.Type, Reference: e.Reference, Digest: e.Digest, Excerpt: e.Excerpt})
	}
	r, err := memory.Save(d.st, memory.SaveInput{ProjectID: d.pid, Dir: d.dir, Content: in.content, Kind: in.kind, Origin: "auto", SessionHandle: in.handle, OperationID: in.op, Rationale: in.rationale, Subject: in.subject, Scope: in.scope, Supersedes: in.supersedes, Completes: in.completes, ExpectedRevision: in.revision, Evidence: evidence, BatchID: in.batch, Generation: in.generation})
	return workflowResult{r.ID, len(r.Superseded), len(r.Completed), len(r.Pending)}, err
}
func (d *workflowDriver) records(scope string) ([]workflowRecord, error) {
	snap, err := d.st.MemorySnapshot(d.pid, map[string]bool{"": true, scope: true})
	var out []workflowRecord
	for _, m := range snap.Entries {
		out = append(out, workflowRecord{m.ID, m.Status, m.Verification, m.Details.Rationale, m.Details.Scope, m.Conflicted, m.Revision})
	}
	return out, err
}
func (d *workflowDriver) begin(handle, id string, items []string, now time.Time) (workflowBatch, error) {
	b := d.bindings[handle]
	if id == "" {
		batch, err := d.st.RequestCapture(b, "checkpoint", 25, now)
		if err != nil {
			return workflowBatch{}, err
		}
		id = batch.ID
	}
	batch, err := d.st.BeginCapture(id, b, items, now)
	out := workflowBatch{id: batch.ID, state: batch.State, generation: batch.Generation}
	for _, i := range batch.Items {
		out.ops = append(out.ops, i.OperationID)
	}
	return out, err
}
func (d *workflowDriver) complete(handle string, b workflowBatch, empty bool, now time.Time) (string, error) {
	out, err := d.st.CompleteCapture(b.id, d.bindings[handle], b.generation, empty, now)
	return out.State, err
}
func (d *workflowDriver) deferContext(handle string, b workflowBatch, now time.Time) (string, error) {
	out, err := d.st.DeferCapture(b.id, d.bindings[handle], b.generation, "context_unavailable", now)
	return out.State, err
}
func (d *workflowDriver) review(id int64) (string, error) {
	out, err := memory.ReviewSources(d.st, d.pid, d.dir, []int64{id})
	return out[id], err
}
func (d *workflowDriver) summary(handle, text string) error {
	return d.st.SetBoundSummary(d.bindings[handle], text)
}
func (d *workflowDriver) session(handle string) int64 { return d.bindings[handle].SessionID }
