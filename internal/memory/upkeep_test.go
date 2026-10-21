package memory_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func upkeepProject(t *testing.T, st *store.Store) (int64, string) {
	t.Helper()
	dir := t.TempDir()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	return pid, dir
}

func upkeepSession(t *testing.T, st *store.Store, pid int64, dir, name string) string {
	t.Helper()
	b, err := st.EnsureBoundSession(pid, "codex", name, dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return b.Handle
}

func upkeepSave(t *testing.T, st *store.Store, in memory.SaveInput) memory.SaveResult {
	t.Helper()
	r, err := memory.Save(st, in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func packText(t *testing.T, st *store.Store, pid int64, dir string, now time.Time) string {
	t.Helper()
	pack, err := memory.BuildPack(st, pid, dir, 0, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	return pack.Text
}

// A session that updates its own automatic task note needs no user review: the
// old note is retired at once and only the replacement is injected.
func TestOwnAutomaticReplacementIsAccepted(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.Store
			pid, dir := upkeepProject(t, st)
			h := upkeepSession(t, st, pid, dir, "own")
			old := upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: h, Dir: dir, Content: "Release 2.11.1 is being built", Kind: "task_state", Origin: "auto"})
			r := upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: h, Dir: dir, Content: "Release 2.11.1 is deployed", Kind: "task_state", Origin: "auto", Supersedes: []int64{old.ID}})
			if !slices.Equal(r.Superseded, []int64{old.ID}) || len(r.Pending) != 0 {
				t.Fatalf("superseded=%v pending=%v, want #%d accepted", r.Superseded, r.Pending, old.ID)
			}
			text := packText(t, st, pid, dir, time.Now())
			if strings.Contains(text, "being built") || !strings.Contains(text, "is deployed") {
				t.Fatalf("pack:\n%s", text)
			}
			// Handles are visible in history, so anything beyond a task note
			// still needs review even from the session that wrote it.
			fact := upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: h, Dir: dir, Content: "The queue is Redis", Kind: "fact", Origin: "auto"})
			r = upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: h, Dir: dir, Content: "The queue is Postgres", Kind: "fact", Origin: "auto", Supersedes: []int64{fact.ID}})
			if !slices.Equal(r.Pending, []int64{fact.ID}) || len(r.Superseded) != 0 {
				t.Fatalf("own fact: superseded=%v pending=%v, want #%d pending", r.Superseded, r.Pending, fact.ID)
			}
		})
	}
}

// Another session's note, or a note the user stated, still needs review. Until
// then the pack injects only the replacement of an automatic note, and keeps a
// stated one next to it.
func TestForeignReplacementStaysPendingButIsNotInjectedTwice(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.Store
			pid, dir := upkeepProject(t, st)
			first := upkeepSession(t, st, pid, dir, "first")
			second := upkeepSession(t, st, pid, dir, "second")
			auto := upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: first, Dir: dir, Content: "Deploy host is staging-one", Kind: "fact", Origin: "auto"})
			stated, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "Use Postgres for the queue", Kind: "decision", Origin: "explicit"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			r := upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: second, Dir: dir, Content: "Deploy host is staging-two", Kind: "fact", Origin: "auto", Supersedes: []int64{auto.ID}})
			if !slices.Equal(r.Pending, []int64{auto.ID}) {
				t.Fatalf("pending=%v superseded=%v, want #%d pending", r.Pending, r.Superseded, auto.ID)
			}
			upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: second, Dir: dir, Content: "Use SQLite for the queue", Kind: "decision", Origin: "auto", Supersedes: []int64{stated}})
			text := packText(t, st, pid, dir, time.Now())
			if strings.Contains(text, "staging-one") || !strings.Contains(text, "staging-two") {
				t.Fatalf("pending automatic target injected next to its replacement:\n%s", text)
			}
			if !strings.Contains(text, "Use Postgres") {
				t.Fatalf("stated decision dropped while its replacement is pending:\n%s", text)
			}
			found, err := memory.Search(st, pid, dir, "staging-one", memory.Filter{}, 10, 0, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if len(found) == 0 {
				t.Fatalf("pending target no longer searchable")
			}
		})
	}
}

// Task notes stop being injected a month after they were last written, unless
// critical. They stay searchable.
func TestOldTaskNotesAreNotInjected(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.Store
			pid, dir := upkeepProject(t, st)
			h := upkeepSession(t, st, pid, dir, "aging")
			upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: h, Dir: dir, Content: "Migration step three is half done", Kind: "task_state", Origin: "auto"})
			upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: h, Dir: dir, Content: "Keep the billing freeze until the audit closes", Kind: "task_state", Origin: "auto", Priority: memory.PriorityCritical})
			upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: h, Dir: dir, Content: "Invoices are stored in UTC", Kind: "fact", Origin: "auto"})
			now := time.Now()
			if text := packText(t, st, pid, dir, now); !strings.Contains(text, "half done") {
				t.Fatalf("fresh task note missing:\n%s", text)
			}
			later := now.Add(memory.TaskStateMaxAge + time.Hour)
			text := packText(t, st, pid, dir, later)
			if strings.Contains(text, "half done") {
				t.Fatalf("aged task note injected:\n%s", text)
			}
			for _, want := range []string{"billing freeze", "stored in UTC"} {
				if !strings.Contains(text, want) {
					t.Fatalf("%q dropped by aging:\n%s", want, text)
				}
			}
			found, err := memory.Search(st, pid, dir, "migration step", memory.Filter{}, 10, 0, later)
			if err != nil {
				t.Fatal(err)
			}
			if len(found) == 0 {
				t.Fatalf("aged task note no longer searchable")
			}
		})
	}
}

// The injected pack leaves out labels that hold for most entries; search keeps
// them, since they matter when acting on an entry.
func TestPackLeavesOutRoutineLabels(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.Store
			pid, dir := upkeepProject(t, st)
			path, ev := userEvidence(t, dir, "user", "Remember: invoices are stored in UTC")
			b, err := st.EnsureBoundSession(pid, "codex", "lean", dir, path, "")
			if err != nil {
				t.Fatal(err)
			}
			plain := upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: b.Handle, Dir: dir, Content: "The cache lives in Redis", Kind: "fact", Origin: "auto"})
			verified := upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: b.Handle, Dir: dir, Content: "invoices are stored in UTC", Kind: "fact", Origin: "explicit", Evidence: []memory.EvidenceInput{ev}})
			scoped := upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: b.Handle, Dir: dir, Content: "Feature flags default off here", Kind: "convention", Origin: "auto", Scope: "checkout:" + dir})
			text := packText(t, st, pid, dir, time.Now())
			for _, want := range []string{
				fmt.Sprintf(`[#%d] (fact, auto) "The cache lives in Redis"`, plain.ID),
				fmt.Sprintf(`[#%d] (fact, explicit, verification: source_checked at `, verified.ID),
				fmt.Sprintf(`[#%d] (convention, auto, scope: "checkout:`, scoped.ID),
				"unverified unless labeled",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("pack lacks %q:\n%s", want, text)
				}
			}
			for _, noise := range []string{"revision:", "completeness=", "scope: project", "unknown (legacy)", "no git state", "verification: unverified"} {
				if strings.Contains(text, noise) {
					t.Errorf("pack still carries %q:\n%s", noise, text)
				}
			}
			found, err := memory.Search(st, pid, dir, "cache Redis", memory.Filter{}, 10, 0, time.Now())
			if err != nil || len(found) == 0 {
				t.Fatalf("search: %v %v", found, err)
			}
			full := memory.RenderEntry(found[0])
			for _, want := range []string{"verification: unverified", "scope: project", "completeness="} {
				if !strings.Contains(full, want) {
					t.Errorf("search result lacks %q: %s", want, full)
				}
			}
		})
	}
}

// An entry too long for the budget is skipped rather than cut, so it cannot
// crowd out the entries that fit.
func TestPackSkipsAnEntryTooLongToFitWhole(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.Store
			pid, dir := upkeepProject(t, st)
			h := upkeepSession(t, st, pid, dir, "budget")
			upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: h, Dir: dir, Content: strings.Repeat("a long critical decision ", 60), Kind: "decision", Origin: "auto", Priority: memory.PriorityCritical})
			upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: h, Dir: dir, Content: "Small fact one", Kind: "fact", Origin: "auto"})
			pack, err := memory.BuildPack(st, pid, dir, 200, 0, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if pack.Served() != 1 || !strings.Contains(pack.Text, "Small fact one") || strings.Contains(pack.Text, "…") {
				t.Fatalf("served %d:\n%s", pack.Served(), pack.Text)
			}
		})
	}
}
