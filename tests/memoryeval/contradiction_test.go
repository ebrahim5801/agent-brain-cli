package memoryeval

import (
	"encoding/json"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
	"os"
	"testing"
)

type contradictionCase struct {
	Subject, Before, After, ScopeBefore, ScopeAfter, SubjectAfter string
	Conflict                                                      bool
}

func TestLabeledStructuredContradictions(t *testing.T) {
	data, err := os.ReadFile("testdata/contradictions.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []contradictionCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			tp, fp, fn, tn := 0, 0, 0, 0
			for _, c := range cases {
				dir := t.TempDir()
				pid, err := backend.Store.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
				if err != nil {
					t.Fatal(err)
				}
				d := newWorkflowDriver(t, backend.Store, pid, dir)
				a, err := d.save(workflowInput{content: c.Before, kind: "decision", subject: c.Subject, scope: c.ScopeBefore})
				if err != nil {
					t.Fatal(err)
				}
				subject := c.SubjectAfter
				if subject == "" {
					subject = c.Subject
				}
				b, err := d.save(workflowInput{content: c.After, kind: "decision", subject: subject, scope: c.ScopeAfter})
				if err != nil {
					t.Fatal(err)
				}
				rs, err := d.records("*")
				if err != nil {
					t.Fatal(err)
				}
				detected := false
				for _, r := range rs {
					if r.id == a.id || r.id == b.id {
						detected = detected || r.conflicted
					}
				}
				if detected && c.Conflict {
					tp++
				}
				if detected && !c.Conflict {
					fp++
				}
				if !detected && c.Conflict {
					fn++
				}
				if !detected && !c.Conflict {
					tn++
				}
			}
			precision, recall := 0.0, 0.0
			if tp+fp > 0 {
				precision = float64(tp) / float64(tp+fp)
			}
			if tp+fn > 0 {
				recall = float64(tp) / float64(tp+fn)
			}
			report := map[string]any{"cases": len(cases), "true_positive": tp, "false_positive": fp, "false_negative": fn, "true_negative": tn, "precision": precision, "recall": recall, "scope": "structured subject keys and defined lexical choices; not general semantic contradiction recall"}
			raw, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			t.Log(string(raw))
			if path := os.Getenv("AGENT_BRAIN_CONTRADICTION_REPORT"); path != "" {
				if err := os.WriteFile(path+"-"+backend.Name+".json", raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if !baselineWorkflow && (precision < 0.90 || recall < 0.80) {
				t.Fatalf("structured contradiction gate missed: %s", raw)
			}
		})
	}
}
