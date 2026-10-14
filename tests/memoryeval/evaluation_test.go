// Package memoryeval measures retrieval quality against versioned, independently
// labeled synthetic workflows. It exercises the public memory/store APIs.
package memoryeval

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

type family struct {
	Family      string   `json:"family"`
	Split       string   `json:"split"`
	Target      string   `json:"target"`
	Queries     []string `json:"queries"`
	Distractors []string `json:"distractors"`
}
type retrievalMetrics struct {
	Queries  int     `json:"queries"`
	Recall5  float64 `json:"recall_at_5"`
	Recall10 float64 `json:"recall_at_10"`
	MRR10    float64 `json:"mrr_at_10"`
	NDCG10   float64 `json:"ndcg_at_10"`
	Top1     float64 `json:"top1"`
}

func TestLabeledRetrieval(t *testing.T) {
	data, err := os.ReadFile("testdata/retrieval.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []family
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, b := range storetest.Both(t) {
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/evaluation"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			targets := map[string]int64{}
			for _, c := range cases {
				id, e := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: c.Target, Kind: "decision", Origin: "auto", Priority: "normal"}, now.Add(-60*24*time.Hour).Format(store.TimeLayout))
				if e != nil {
					t.Fatal(e)
				}
				targets[c.Family] = id
				for _, d := range c.Distractors {
					if _, e := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: d, Kind: "task_state", Origin: "auto", Priority: "critical"}, now.Format(store.TimeLayout)); e != nil {
						t.Fatal(e)
					}
				}
			}
			metrics := map[string]*retrievalMetrics{"all": {}, "development": {}, "heldout": {}}
			dir := t.TempDir()
			for _, c := range cases {
				for _, q := range c.Queries {
					got, e := memory.Search(st, pid, dir, q, memory.Filter{}, 10, 0, now)
					if e != nil {
						t.Fatal(e)
					}
					rank := 0
					for i, r := range got {
						if r.Entry.ID == targets[c.Family] {
							rank = i + 1
							break
						}
					}
					for _, key := range []string{"all", c.Split} {
						m := metrics[key]
						m.Queries++
						if rank > 0 && rank <= 5 {
							m.Recall5++
						}
						if rank > 0 {
							m.Recall10++
							m.MRR10 += 1 / float64(rank)
							m.NDCG10 += 1 / math.Log2(float64(rank+1))
						}
						if rank == 1 {
							m.Top1++
						}
					}
				}
			}
			for _, m := range metrics {
				n := float64(m.Queries)
				m.Recall5 /= n
				m.Recall10 /= n
				m.MRR10 /= n
				m.NDCG10 /= n
				m.Top1 /= n
			}
			if !baselineWorkflow && os.Getenv("AGENT_BRAIN_MEMORY_V2_RANKING") != "off" {
				// Frozen before tuning: held-out baseline nDCG .70146957, Recall@10 .98.
				if metrics["heldout"].NDCG10 < 0.701469572804576*1.10 || metrics["heldout"].Recall10 < 0.97 {
					t.Fatalf("held-out quality gate missed: %+v", metrics["heldout"])
				}
			}
			report, _ := json.MarshalIndent(metrics, "", "  ")
			t.Log(string(report))
			if path := os.Getenv("AGENT_BRAIN_EVAL_REPORT"); path != "" {
				if e := os.WriteFile(path+"-"+b.Name+".json", report, 0o600); e != nil {
					t.Fatal(e)
				}
			}
			if metrics["all"].Queries != 150 {
				t.Fatalf("fixture query count %d", metrics["all"].Queries)
			}
		})
	}
}
