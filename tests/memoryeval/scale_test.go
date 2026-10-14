package memoryeval

import (
	"encoding/json"
	"fmt"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

func TestScaleEnvelope(t *testing.T) {
	if os.Getenv("AGENT_BRAIN_SCALE") != "1" {
		t.Skip("enable AGENT_BRAIN_SCALE=1 for the isolated 10,000-record performance qualification")
	}
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.Store
			dir := t.TempDir()
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			newWorkflowDriver(t, st, pid, dir)
			rng := rand.New(rand.NewSource(42))
			now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
			tx, err := st.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for i := 0; i < 10000; i++ {
				kind := "fact"
				if i%3 == 0 {
					kind = "decision"
				}
				at := now.Add(-time.Duration(rng.Intn(180)) * 24 * time.Hour).Format(store.TimeLayout)
				_, err := st.ExecTx(tx, `INSERT INTO memories(project_id,content,kind,origin,priority,status,captured_at,updated_at) VALUES(?,?,?,'auto','normal','active',?,?)`, pid, fmt.Sprintf("Subject %d uses local checkpoint receipts because durable retry preserves invoices", i), kind, at, at)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			var search, pack []float64
			for i := 0; i < 30; i++ {
				start := time.Now()
				if _, err := memory.Search(st, pid, dir, fmt.Sprintf("checkpoint receipts %d", i*337), memory.Filter{}, 10, 0, now); err != nil {
					t.Fatal(err)
				}
				search = append(search, float64(time.Since(start).Microseconds())/1000)
				start = time.Now()
				p, err := memory.BuildPack(st, pid, dir, 2000, 0, now)
				if err != nil {
					t.Fatal(err)
				}
				pack = append(pack, float64(time.Since(start).Microseconds())/1000)
				if !baselineWorkflow && len(p.Text) > 8000 {
					t.Fatal("scale pack exceeded budget")
				}
			}
			runtime.ReadMemStats(&after)
			sort.Float64s(search)
			sort.Float64s(pack)
			report := map[string]any{"rows": 10000, "seed": 42, "samples": 30, "search_p50_ms": search[14], "search_p95_ms": search[28], "pack_p50_ms": pack[14], "pack_p95_ms": pack[28], "allocated_bytes": after.TotalAlloc - before.TotalAlloc, "go": runtime.Version(), "git_fixture": "no repository; separate Git correctness/timeout tests qualify repository states"}
			raw, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			t.Log(string(raw))
			if path := os.Getenv("AGENT_BRAIN_SCALE_REPORT"); path != "" {
				if err := os.WriteFile(path+"-"+backend.Name+".json", raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if !baselineWorkflow && pack[28] >= 1000 {
				t.Fatalf("pack p95 exceeds one-second injection envelope: %f ms", pack[28])
			}
		})
	}
}
