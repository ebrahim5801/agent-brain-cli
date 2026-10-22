package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
)

func obsidianFixture() memory.ExportFile {
	return memory.ExportFile{
		ExportedAt: "2026-10-22T10:00:00.000Z",
		Generator:  "agent-brain test",
		Project:    memory.ExportProject{Name: "demo"},
		Entries: []memory.ExportEntry{
			{Key: 1, Kind: "decision", Origin: "explicit", Priority: "critical", Status: "active", Content: "Use tabs / not [spaces] | #always\nsecond line", Branch: `feat/"quoted": yes`, CapturedAt: "2026-10-01T00:00:00.000Z"},
			{Key: 2, Kind: "task_state", Origin: "auto", Priority: "normal", Status: "active", Content: "Release 1.0 shipped", Rationale: "keeps it resumable", CapturedAt: "2026-10-02T00:00:00.000Z"},
			{Key: 3, Kind: "task_state", Origin: "auto", Priority: "normal", Status: "active", Content: "Release 1.0 in progress", CapturedAt: "2026-10-01T00:00:00.000Z"},
			{Key: 4, Kind: "fact", Origin: "auto", Priority: "normal", Status: "active", Content: "Tabs keep diffs small", CapturedAt: "2026-10-03T00:00:00.000Z"},
		},
		Relations: []memory.ExportRelation{
			{Source: 2, Target: 3, Type: "supersedes", State: "accepted"},
			{Source: 4, Target: 1, Type: "supports", State: "accepted"},
		},
	}
}

func noteFor(t *testing.T, files map[string]string, prefix string) (string, string) {
	t.Helper()
	for name, text := range files {
		if strings.HasPrefix(name, prefix) {
			return name, text
		}
	}
	t.Fatalf("no note starting with %q in %v", prefix, files)
	return "", ""
}

func TestObsidianNotesAreLinkedAndSafelyNamed(t *testing.T) {
	files := renderObsidianNotes(obsidianFixture())
	if len(files) != 5 {
		t.Fatalf("got %d files, want 4 notes and an index", len(files))
	}
	name, first := noteFor(t, files, "1 ")
	if name != "1 Use tabs not spaces always second line.md" {
		t.Errorf("note name = %q", name)
	}
	if !strings.Contains(first, "branch: \"feat/\\\"quoted\\\": yes\"\n") {
		t.Errorf("free-text property not quoted:\n%s", first)
	}
	if !strings.Contains(first, "Use tabs / not [spaces] | #always\nsecond line") {
		t.Errorf("content changed:\n%s", first)
	}

	_, newer := noteFor(t, files, "2 ")
	_, older := noteFor(t, files, "3 ")
	if !strings.Contains(newer, "- replaces [[3 Release 1.0 in progress|#3]]") || !strings.Contains(newer, "**Why:** keeps it resumable") {
		t.Errorf("replacement note lacks its link or rationale:\n%s", newer)
	}
	if !strings.Contains(older, "status: superseded") || !strings.Contains(older, "- replaced by [[2 Release 1.0 shipped|#2]]") {
		t.Errorf("replaced note not marked or linked:\n%s", older)
	}
	_, fact := noteFor(t, files, "4 ")
	if !strings.Contains(fact, "- supports [[1 Use tabs not spaces always second line|#1]]") {
		t.Errorf("supports link missing:\n%s", fact)
	}

	_, index := noteFor(t, files, "agent-brain memory - demo")
	for _, want := range []string{"## Decisions", "## Task notes", "## Facts", "[[2 Release 1.0 shipped|#2]]"} {
		if !strings.Contains(index, want) {
			t.Errorf("index lacks %q:\n%s", want, index)
		}
	}
	for name := range files {
		if strings.ContainsAny(name, `/\:*?"<>|#^[]`) {
			t.Errorf("unsafe note name %q", name)
		}
	}
}

func mustWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestObsidianFolderRules(t *testing.T) {
	root := t.TempDir()

	// A new folder is created private.
	fresh := filepath.Join(root, "vault", "memory")
	if names, err := prepareObsidianDir(fresh); err != nil || names != nil {
		t.Fatalf("new folder: %v, %v", names, err)
	}
	if info, err := os.Stat(fresh); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Fatalf("new folder: %v, %v", info, err)
	}

	// A folder holding someone else's files is refused.
	foreign := filepath.Join(root, "notes")
	mustWrite(t, filepath.Join(foreign, "diary.md"), "mine")
	if _, err := prepareObsidianDir(foreign); err == nil {
		t.Fatal("folder with other files accepted")
	}

	// A symlinked folder is refused, so the export cannot be redirected.
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "link")
		if err := os.Symlink(foreign, link); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareObsidianDir(link); err == nil {
			t.Fatal("symlinked folder accepted")
		}
	}

	// An earlier export's folder is reused; only bare note names it listed count.
	earlier := filepath.Join(root, "earlier")
	marker, _ := json.Marshal(obsidianManifest{Format: "agent-brain-obsidian", Files: []string{"1 old.md", "../escape.md", ".hidden.md", "notes.txt"}})
	mustWrite(t, filepath.Join(earlier, obsidianMarker), string(marker))
	names, err := prepareObsidianDir(earlier)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "1 old.md" {
		t.Errorf("listed notes = %v, want only the bare note name", names)
	}

	// A marker that isn't ours is refused.
	mustWrite(t, filepath.Join(earlier, obsidianMarker), `{"format":"other"}`)
	if _, err := prepareObsidianDir(earlier); err == nil {
		t.Fatal("foreign marker accepted")
	}
}

// A later export replaces only notes the earlier one wrote: a file you added
// under a name the export now wants is left alone, and nothing is written.
func TestObsidianExportNeverReplacesYourFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "1 old.md"), "ours")
	mustWrite(t, filepath.Join(dir, "agent-brain memory - demo.md"), "yours")
	if err := checkObsidianTargets(dir, []string{"1 old.md", "2 new.md"}, []string{"1 old.md"}); err != nil {
		t.Fatalf("replacing our own note refused: %v", err)
	}
	err := checkObsidianTargets(dir, []string{"1 old.md", "agent-brain memory - demo.md"}, []string{"1 old.md"})
	if err == nil || !strings.Contains(err.Error(), "agent-brain memory - demo.md") {
		t.Fatalf("user file would be replaced: %v", err)
	}
}
