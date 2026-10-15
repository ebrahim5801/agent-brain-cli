package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func setupMemoryList(t *testing.T, count int) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("AGENT_BRAIN_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", filepath.Join(root, "config"))
	work := filepath.Join(root, "project")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	project := attribution.Resolve(work)
	projectID, err := st.UpsertProject(store.ProjectIdentity{Kind: project.Kind, Identity: project.Identity, DisplayName: project.DisplayName}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	priorities := []string{store.MemoryPriorityCritical, store.MemoryPriorityNormal, store.MemoryPriorityBackground}
	for i := 1; i <= count; i++ {
		kind := "fact"
		if i%2 == 0 {
			kind = "decision"
		}
		at := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format(store.TimeLayout)
		if _, err := st.InsertMemory(store.NewMemory{
			ProjectID: projectID, Content: fmt.Sprintf("cli-memory-%03d", i), Kind: kind,
			Origin: "auto", Priority: priorities[(i-1)%len(priorities)],
		}, at); err != nil {
			t.Fatal(err)
		}
	}
	return work
}

func runMemoryList(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newMemoryListCmd()
	cmd.SetArgs(args)
	var err error
	out := capture(t, func() { err = cmd.Execute() })
	return out, err
}

func TestMemoryListDefaultPageAndOffsetFooter(t *testing.T) {
	setupMemoryList(t, 60)
	out, err := runMemoryList(t)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, "[#"); got != 50 {
		t.Fatalf("default page entries = %d, want 50", got)
	}
	if !strings.HasSuffix(strings.TrimSuffix(out, "\n"), "Showing 1-50 of 60. Next: agent-brain memory list --offset 50 (or --limit 0 for all)") {
		t.Fatalf("first-page footer incorrect: %q", out[len(out)-min(len(out), 140):])
	}
	if !strings.Contains(out, "cli-memory-060") || !strings.Contains(out, "cli-memory-011") || strings.Contains(out, "cli-memory-010") {
		t.Fatalf("default page is not newest-first with 50 entries")
	}

	last, err := runMemoryList(t, "--offset", "50")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(last, "[#"); got != 10 || !strings.HasSuffix(strings.TrimSuffix(last, "\n"), "Showing 51-60 of 60.") {
		t.Fatalf("last page count/footer incorrect: count=%d output tail=%q", got, last[len(last)-min(len(last), 100):])
	}
}

func TestMemoryListLimitZeroAndFiltersApplyBeforePaging(t *testing.T) {
	setupMemoryList(t, 60)
	all, err := runMemoryList(t, "--limit", "0")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(all, "[#"); got != 60 || strings.Contains(all, "Showing ") {
		t.Fatalf("--limit 0 output count/footer incorrect: count=%d", got)
	}

	filtered, err := runMemoryList(t, "--kind", "decision", "--priority", "normal", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(filtered, "[#"); got != 2 || !strings.Contains(filtered, "Showing 1-2 of 10. Next:") {
		t.Fatalf("filtered paging was not applied after filters: %q", filtered)
	}
}

func TestMemoryListJSONHonorsPagingWithoutFooter(t *testing.T) {
	setupMemoryList(t, 6)
	out, err := runMemoryList(t, "--json", "--limit", "2", "--offset", "2")
	if err != nil {
		t.Fatal(err)
	}
	var entries []memoryJSON
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, out)
	}
	if len(entries) != 2 || entries[0].Content != "cli-memory-004" || entries[1].Content != "cli-memory-003" {
		t.Errorf("JSON page = %+v", entries)
	}
	if strings.Contains(out, "Showing ") {
		t.Errorf("JSON output has a footer: %s", out)
	}
}
