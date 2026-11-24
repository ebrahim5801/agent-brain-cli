package memory_test

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func throughFile(t *testing.T, f memory.ExportFile) memory.ExportFile {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	g, err := memory.ReadExport(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func freshStore(t *testing.T, backend storetest.Backend, v2 bool) *store.Store {
	t.Helper()
	st := storetest.SQLite(t)
	if backend.Name == "postgres" {
		st = storetest.Postgres(t)
	}
	if v2 {
		if err := st.UpgradeMemoryV2(); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func rowCount(t *testing.T, st *store.Store, table string) int {
	t.Helper()
	var n int
	if err := st.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func importFile(t *testing.T, st *store.Store, f memory.ExportFile, dir string, dryRun bool) store.ImportResult {
	t.Helper()
	entries, relations := f.ImportRecords(dir)
	r, err := st.ImportMemories(store.ProjectIdentity{Kind: "directory", Identity: dir, DisplayName: "demo"}, entries, relations, "imported for a test", store.Now(), dryRun)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A project's memories survive the trip to another machine: retired entries
// stay behind, links and rationales come along, the exporting checkout's
// entries land in the importing checkout, and a second import adds nothing.
func TestExportImportRoundTrip(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			src := backend.Store
			pid, dir := upkeepProject(t, src)
			a := upkeepSession(t, src, pid, dir, "a")
			b := upkeepSession(t, src, pid, dir, "b")
			save := func(h string, in memory.SaveInput) memory.SaveResult {
				in.ProjectID, in.SessionHandle, in.Dir = pid, h, dir
				return upkeepSave(t, src, in)
			}
			save(a, memory.SaveInput{Content: "Use Postgres for the job queue", Kind: "decision", Origin: "explicit", Priority: store.MemoryPriorityCritical, Rationale: "Redis lost jobs on restart"})
			oldTask := save(a, memory.SaveInput{Content: "Release is being built", Kind: "task_state", Origin: "auto"})
			save(a, memory.SaveInput{Content: "Release is deployed", Kind: "task_state", Origin: "auto", Supersedes: []int64{oldTask.ID}})
			oldFact := save(a, memory.SaveInput{Content: "The cache is memcached", Kind: "fact", Origin: "auto"})
			save(b, memory.SaveInput{Content: "The cache is Redis", Kind: "fact", Origin: "auto", Supersedes: []int64{oldFact.ID}})
			save(a, memory.SaveInput{Content: "Tag releases from main", Kind: "convention", Origin: "auto", Scope: "branch:main"})
			save(a, memory.SaveInput{Content: "This checkout builds with -race", Kind: "fact", Origin: "auto", Scope: "checkout:" + store.CheckoutIdentity(dir)})

			proj := attribution.Project{Kind: attribution.KindDirectory, Identity: dir, DisplayName: "demo"}
			f, _, err := memory.BuildExport(src, pid, proj, dir, false, "test", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			f = throughFile(t, f)
			if len(f.Entries) != 6 || len(f.Relations) != 1 || f.Relations[0].State != "pending" {
				t.Fatalf("export has %d entries and links %+v, want 6 and one pending replacement", len(f.Entries), f.Relations)
			}
			if !f.ProjectMatches(proj) || f.ProjectMatches(attribution.Project{Identity: t.TempDir()}) {
				t.Fatal("project check is wrong")
			}
			raw, _ := json.Marshal(f)
			if strings.Contains(string(raw), dir) || strings.Contains(string(raw), a) {
				t.Fatalf("export holds a local path or session handle:\n%s", raw)
			}
			all, _, err := memory.BuildExport(src, pid, proj, dir, true, "test", time.Now())
			if err != nil || len(all.Entries) != 7 || len(all.Relations) != 2 {
				t.Fatalf("--all export: %d entries, %d links, err %v; want 7 and 2", len(all.Entries), len(all.Relations), err)
			}

			dst := freshStore(t, backend, true)
			there := t.TempDir()
			if r := importFile(t, dst, f, there, true); r.Imported != 6 || r.Relations != 1 || rowCount(t, dst, "memories") != 0 {
				t.Fatalf("dry run: %+v, %d rows written", r, rowCount(t, dst, "memories"))
			}
			r := importFile(t, dst, f, there, false)
			if r.Imported != 6 || r.Relations != 1 || r.Explicit != 1 || r.Critical != 1 {
				t.Fatalf("import: %+v", r)
			}
			if again := importFile(t, dst, f, there, false); again.Imported != 0 || again.Duplicates != 6 || again.Relations != 0 {
				t.Fatalf("second import: %+v", again)
			}
			if n := rowCount(t, dst, "memory_evidence"); n != 0 {
				t.Fatalf("import copied %d evidence rows", n)
			}
			entries, err := memory.List(dst, r.ProjectID, there, false, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			byContent := map[string]store.Memory{}
			for _, e := range entries {
				byContent[e.Entry.Content] = e.Entry
			}
			d := byContent["Use Postgres for the job queue"]
			if d.Origin != "explicit" || d.Priority != store.MemoryPriorityCritical || d.Details.Rationale != "Redis lost jobs on restart" || d.Details.Completeness != "imported for a test" || d.Verification != "unverified" {
				t.Fatalf("decision came over as %+v", d)
			}
			if got := byContent["This checkout builds with -race"].Details.Scope; got != "checkout:"+store.CheckoutIdentity(there) {
				t.Fatalf("checkout entry has scope %q", got)
			}
			text := packText(t, dst, r.ProjectID, there, time.Now())
			if !strings.Contains(text, "The cache is Redis") || strings.Contains(text, "memcached") || strings.Contains(text, "being built") {
				t.Fatalf("pack after import:\n%s", text)
			}
		})
	}
}

// An export leaves out entries it cannot move: another checkout's, and those
// already shared with a team.
func TestExportLeavesOutOtherCheckouts(t *testing.T) {
	st := storetest.SQLite(t)
	pid, dir := upkeepProject(t, st)
	h := upkeepSession(t, st, pid, dir, "a")
	upkeepSave(t, st, memory.SaveInput{ProjectID: pid, SessionHandle: h, Dir: dir, Content: "A fact", Kind: "fact", Origin: "auto"})
	if _, err := st.Exec(`UPDATE memory_details SET scope='checkout:/elsewhere'`); err != nil {
		t.Fatal(err)
	}
	f, notes, err := memory.BuildExport(st, pid, attribution.Project{Identity: dir}, dir, true, "", time.Now())
	if err != nil || len(f.Entries) != 0 || notes.OtherCheckouts != 1 {
		t.Fatalf("entries %d, notes %+v, err %v", len(f.Entries), notes, err)
	}
	if _, err := st.Exec(`UPDATE memory_details SET scope=''`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(`UPDATE memories SET team_uid='u-1'`); err != nil {
		t.Fatal(err)
	}
	if f, notes, _ = memory.BuildExport(st, pid, attribution.Project{Identity: dir}, dir, true, "", time.Now()); len(f.Entries) != 0 || notes.Shared != 1 {
		t.Fatalf("entries %d, notes %+v", len(f.Entries), notes)
	}
}

// A git remote that is a folder on this machine is a local path: the file
// identifies the project by digest only.
func TestExportNamesOnlyNetworkRemotes(t *testing.T) {
	for _, origin := range []string{"/home/alice/private/project.git", "file:///home/alice/private/project.git", `C:\repos\project.git`, "C:/repos/project.git", "../project.git", "~/project.git"} {
		identity := attribution.CanonicalizeRemote(origin)
		f, _, err := memory.BuildExport(nil, 0, attribution.Project{Kind: attribution.KindRemote, Identity: identity, DisplayName: "project"}, t.TempDir(), false, "", time.Now())
		if err != nil || f.Project.Remote != "" || !f.ProjectMatches(attribution.Project{Identity: identity}) {
			t.Errorf("%s: remote %q, err %v", origin, f.Project.Remote, err)
		}
	}
	for _, origin := range []string{"git@github.com:org/project.git", "https://user:secret@gitlab.example.com/org/project.git", "ssh://git@gitserver:2222/org/project.git"} {
		identity := attribution.CanonicalizeRemote(origin)
		f, _, _ := memory.BuildExport(nil, 0, attribution.Project{Kind: attribution.KindRemote, Identity: identity}, t.TempDir(), false, "", time.Now())
		if f.Project.Remote != identity || strings.Contains(f.Project.Remote, "secret") {
			t.Errorf("%s: remote %q, want %q", origin, f.Project.Remote, identity)
		}
	}
}

func sampleExport() memory.ExportFile {
	return memory.ExportFile{
		Format: memory.ExportFormat, Version: memory.ExportVersion, ExportedAt: "2026-10-21T10:00:00.000Z",
		Project: memory.ExportProject{Name: "demo", Kind: "directory", IdentityDigest: strings.Repeat("a", 64)},
		Entries: []memory.ExportEntry{
			{Key: 1, Kind: "decision", Origin: "explicit", Priority: "critical", Status: "active", Content: "Use Postgres", Rationale: "Redis lost jobs", CapturedAt: "2026-10-01T09:00:00.000Z", UpdatedAt: "2026-10-01T09:00:00.000Z"},
			{Key: 2, Kind: "decision", Origin: "auto", Priority: "normal", Status: "active", Content: "Use Postgres 17", Scope: "branch:main", CapturedAt: "2026-10-02T09:00:00+02:00"},
			{Key: 3, Kind: "task_state", Origin: "auto", Priority: "normal", Status: "active", Completed: true, Content: "Upgrade done", CapturedAt: "2026-10-03T09:00:00Z"},
		},
		Relations: []memory.ExportRelation{{Source: 2, Target: 1, Type: "supersedes", State: "accepted"}},
	}
}

// An import never retires an entry that was already on this machine: an
// accepted replacement of one becomes a proposal waiting for review.
func TestImportNeverRetiresALocalEntry(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := freshStore(t, backend, true)
			dir := t.TempDir()
			first := sampleExport()
			first.Entries, first.Relations = first.Entries[:1], nil
			importFile(t, st, throughFile(t, first), dir, false)
			r := importFile(t, st, throughFile(t, sampleExport()), dir, false)
			if r.Imported != 2 || r.Duplicates != 1 || r.Relations != 1 || r.Pending != 1 {
				t.Fatalf("import: %+v", r)
			}
			entries, err := memory.List(st, r.ProjectID, dir, false, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			kept := false
			for _, e := range entries {
				kept = kept || (e.Entry.Content == "Use Postgres" && e.Entry.Status == store.MemoryActive)
				if e.Entry.Content == "Upgrade done" {
					t.Fatal("a completed task is listed as active")
				}
			}
			if !kept {
				t.Fatal("the local decision was retired by the import")
			}
		})
	}
}

// Without memory V2 an import keeps what the older schema can hold and says
// what it could not keep.
func TestImportWithoutMemoryV2(t *testing.T) {
	st := storetest.SQLite(t)
	if st.MemoryV2 {
		t.Skip("store opened with memory V2")
	}
	r := importFile(t, st, throughFile(t, sampleExport()), t.TempDir(), false)
	if r.Imported != 1 || r.ScopedSkipped != 1 || r.CompletedSkipped != 1 || r.RationalesLost != 1 || r.RelationsSkipped != 0 {
		t.Fatalf("import: %+v", r)
	}
	if n := rowCount(t, st, "memories"); n != 1 {
		t.Fatalf("%d rows written", n)
	}
}

func TestReadExportMasksSecretsAndNormalizesTimes(t *testing.T) {
	f := sampleExport()
	fake := "AKIA" + "IOSFODNN7EXAMPLE"
	f.Entries[0].Content = "the key is " + fake
	g := throughFile(t, f)
	if strings.Contains(g.Entries[0].Content, fake) || !strings.Contains(g.Entries[0].Content, "[redacted:") {
		t.Fatalf("content not masked: %q", g.Entries[0].Content)
	}
	if g.Entries[1].CapturedAt != "2026-10-02T07:00:00.000Z" || g.Entries[1].UpdatedAt != g.Entries[1].CapturedAt {
		t.Fatalf("times not normalized: %q %q", g.Entries[1].CapturedAt, g.Entries[1].UpdatedAt)
	}
}

func TestReadExportRejects(t *testing.T) {
	valid, err := json.Marshal(sampleExport())
	if err != nil {
		t.Fatal(err)
	}
	edit := func(change func(*memory.ExportFile)) string {
		f := sampleExport()
		change(&f)
		data, _ := json.Marshal(f)
		return string(data)
	}
	cases := map[string]string{
		"unknown field":  strings.Replace(string(valid), `"format"`, `"extra":1,"format"`, 1),
		"trailing data":  string(valid) + "{}",
		"wrong format":   edit(func(f *memory.ExportFile) { f.Format = "something-else" }),
		"newer version":  edit(func(f *memory.ExportFile) { f.Version = 2 }),
		"no project":     edit(func(f *memory.ExportFile) { f.Project.IdentityDigest = "" }),
		"unknown kind":   edit(func(f *memory.ExportFile) { f.Entries[0].Kind = "opinion" }),
		"unknown origin": edit(func(f *memory.ExportFile) { f.Entries[0].Origin = "user" }),
		"unknown status": edit(func(f *memory.ExportFile) { f.Entries[0].Status = "archived" }),
		"bad time":       edit(func(f *memory.ExportFile) { f.Entries[0].CapturedAt = "yesterday" }),
		"empty content":  edit(func(f *memory.ExportFile) { f.Entries[0].Content = "  " }),
		"long content":   edit(func(f *memory.ExportFile) { f.Entries[0].Content = strings.Repeat("x", memory.MaxContentLen+1) }),
		"local path":     edit(func(f *memory.ExportFile) { f.Entries[0].Scope = "checkout:/home/someone/project" }),
		"branch newline": edit(func(f *memory.ExportFile) { f.Entries[0].Branch = "main\nx" }),
		"bad commit":     edit(func(f *memory.ExportFile) { f.Entries[0].Commit = "not-a-commit" }),
		"completed fact": edit(func(f *memory.ExportFile) { f.Entries[0].Completed = true }),
		"duplicate key":  edit(func(f *memory.ExportFile) { f.Entries[1].Key = 1 }),
		"dangling link":  edit(func(f *memory.ExportFile) { f.Relations[0].Target = 9 }),
		"self link":      edit(func(f *memory.ExportFile) { f.Relations[0].Target = 2 }),
		"unknown link":   edit(func(f *memory.ExportFile) { f.Relations[0].Type = "replaces" }),
		"unknown state":  edit(func(f *memory.ExportFile) { f.Relations[0].State = "approved" }),
	}
	for name, text := range cases {
		if _, err := memory.ReadExport(strings.NewReader(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	big := io.MultiReader(strings.NewReader(string(valid)), strings.NewReader(strings.Repeat(" ", memory.MaxExportBytes)))
	if _, err := memory.ReadExport(big); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("oversized file: %v", err)
	}
}
