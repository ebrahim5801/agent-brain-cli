package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/version"
)

// obsidianMarker lists the notes an Obsidian export wrote, so a later export
// into the same folder replaces them and removes notes for entries that are
// gone, without touching anything else in the folder.
const obsidianMarker = ".agent-brain-export.json"

type obsidianManifest struct {
	Format string   `json:"format"`
	Files  []string `json:"files"`
}

// exportObsidian writes one Markdown note per memory into dir, with Obsidian
// properties and wiki-links between related entries, plus an index note.
// Opening the folder (or a vault containing it) shows the memories as a graph.
func exportObsidian(st *store.Store, projectID int64, cwd string, all bool, dir string) error {
	if dir == "" {
		return errors.New("--format obsidian needs --out FOLDER (a folder inside your vault, for example)")
	}
	f, notes, err := memory.BuildExport(st, projectID, attribution.Resolve(cwd), cwd, all, "agent-brain "+version.Version, time.Now())
	if err != nil {
		return err
	}
	previous, err := prepareObsidianDir(dir)
	if err != nil {
		return err
	}
	files := renderObsidianNotes(f)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	if err := checkObsidianTargets(dir, names, previous); err != nil {
		return err
	}
	for _, name := range names {
		if err := writePrivateFile(filepath.Join(dir, name), []byte(files[name])); err != nil {
			return err
		}
	}
	for _, old := range previous {
		if _, kept := files[old]; !kept {
			_ = os.Remove(filepath.Join(dir, old))
		}
	}
	marker, err := json.MarshalIndent(obsidianManifest{Format: "agent-brain-obsidian", Files: names}, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivateFile(filepath.Join(dir, obsidianMarker), append(marker, '\n')); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %d notes to %s. Open the folder in Obsidian, or export into a folder inside your vault.\n", len(f.Entries), dir)
	if notes.Shared > 0 {
		fmt.Fprintf(os.Stderr, "Left out %d entries shared with your team.\n", notes.Shared)
	}
	if notes.OtherCheckouts > 0 {
		fmt.Fprintf(os.Stderr, "Left out %d entries that belong to other checkouts of this project.\n", notes.OtherCheckouts)
	}
	return nil
}

// prepareObsidianDir makes sure dir is ours to write: a new folder, an empty
// one, or one a previous export wrote. It returns the notes that export listed.
// A symlinked folder is refused so the export cannot be redirected elsewhere.
func prepareObsidianDir(dir string) ([]string, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.MkdirAll(dir, 0o700)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder; choose a new or empty folder", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, obsidianMarker))
	if err != nil {
		return nil, fmt.Errorf("%s already holds other files; choose a new or empty folder, or one an earlier Obsidian export wrote", dir)
	}
	var m obsidianManifest
	if err := json.Unmarshal(raw, &m); err != nil || m.Format != "agent-brain-obsidian" {
		return nil, fmt.Errorf("%s has an unreadable %s; remove the folder or choose another", dir, obsidianMarker)
	}
	var names []string
	for _, name := range m.Files {
		// Only bare note names this exporter could have written.
		if name == filepath.Base(name) && strings.HasSuffix(name, ".md") && !strings.HasPrefix(name, ".") {
			names = append(names, name)
		}
	}
	return names, nil
}

// checkObsidianTargets checks every name before anything is written: a file
// you added to the folder yourself is never replaced, even if its name matches
// a note this export would write.
func checkObsidianTargets(dir string, names, previous []string) error {
	ours := map[string]bool{}
	for _, name := range previous {
		ours[name] = true
	}
	for _, name := range names {
		_, err := os.Lstat(filepath.Join(dir, name))
		if err == nil && !ours[name] {
			return fmt.Errorf("%s already has a file named %q that an earlier export did not write; move it, then export again", dir, name)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

var obsidianKindTitles = map[string]string{"decision": "Decisions", "convention": "Conventions", "task_state": "Task notes", "fact": "Facts"}

// renderObsidianNotes returns note file names and their contents.
func renderObsidianNotes(f memory.ExportFile) map[string]string {
	names := map[int64]string{}
	for _, e := range f.Entries {
		names[e.Key] = obsidianNoteName(e)
	}
	links := map[int64][]string{}
	linked := map[string]bool{}
	link := func(from, to int64, label string) {
		target, ok := names[to]
		key := fmt.Sprintf("%d %d %s", from, to, label)
		if !ok || linked[key] {
			return
		}
		linked[key] = true
		links[from] = append(links[from], fmt.Sprintf("- %s [[%s|#%d]]", label, strings.TrimSuffix(target, ".md"), to))
	}
	// A replacement is recorded either on the old entry (superseded_by) or, in
	// Memory V2, as an accepted "supersedes" relation from the new entry.
	superseded := map[int64]bool{}
	for _, e := range f.Entries {
		if e.SupersededBy != 0 {
			superseded[e.Key] = true
			link(e.Key, e.SupersededBy, "replaced by")
			link(e.SupersededBy, e.Key, "replaces")
		}
	}
	for _, r := range f.Relations {
		if r.Type == "supersedes" && r.State == "accepted" {
			superseded[r.Target] = true
			link(r.Source, r.Target, "replaces")
			link(r.Target, r.Source, "replaced by")
			continue
		}
		label := strings.ReplaceAll(r.Type, "_", " ")
		if r.State != "accepted" && r.State != "" {
			label += " (" + r.State + ")"
		}
		link(r.Source, r.Target, label)
	}

	project := f.Project.Name
	if project == "" {
		project = "project"
	}
	index := obsidianSafe("agent-brain memory - "+project, 80) + ".md"
	files := map[string]string{}
	byKind := map[string][]memory.ExportEntry{}
	for _, e := range f.Entries {
		byKind[e.Kind] = append(byKind[e.Kind], e)
		var b strings.Builder
		b.WriteString("---\n")
		property(&b, "id", strconv.FormatInt(e.Key, 10))
		property(&b, "kind", e.Kind)
		property(&b, "priority", e.Priority)
		property(&b, "origin", e.Origin)
		status := e.Status
		if superseded[e.Key] && status == "active" {
			status = "superseded"
		}
		property(&b, "status", status)
		if e.Completed {
			property(&b, "completed", "true")
		}
		quoted(&b, "captured", e.CapturedAt)
		if e.Branch != "" {
			quoted(&b, "branch", e.Branch)
		}
		if e.Commit != "" {
			quoted(&b, "commit", e.Commit)
		}
		if e.SubjectKey != "" {
			quoted(&b, "subject", e.SubjectKey)
		}
		fmt.Fprintf(&b, "tags:\n  - agent-brain\n  - agent-brain/%s\n", e.Kind)
		fmt.Fprintf(&b, "aliases:\n  - %s\n", jsonString("#"+strconv.FormatInt(e.Key, 10)))
		b.WriteString("---\n\n")
		b.WriteString(e.Content)
		b.WriteString("\n")
		if e.Rationale != "" {
			b.WriteString("\n**Why:** " + e.Rationale + "\n")
		}
		if l := links[e.Key]; len(l) > 0 {
			b.WriteString("\n## Links\n\n" + strings.Join(l, "\n") + "\n")
		}
		fmt.Fprintf(&b, "\nPart of [[%s|%s memory]].\n", strings.TrimSuffix(index, ".md"), project)
		files[names[e.Key]] = b.String()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "---\ntags:\n  - agent-brain\nexported: %s\n---\n\n# agent-brain memory: %s\n\n", jsonString(f.ExportedAt), project)
	fmt.Fprintf(&b, "%d entries exported by %s.\n", len(f.Entries), f.Generator)
	for _, kind := range []string{"decision", "convention", "task_state", "fact"} {
		entries := byKind[kind]
		if len(entries) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", obsidianKindTitles[kind])
		for _, e := range entries {
			fmt.Fprintf(&b, "- [[%s|#%d]] %s\n", strings.TrimSuffix(names[e.Key], ".md"), e.Key, obsidianSummary(e.Content))
		}
	}
	files[index] = b.String()
	return files
}

func property(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "%s: %s\n", key, value)
}

// quoted writes a free-text property as a JSON string, which YAML reads as the
// same string whatever characters it holds.
func quoted(b *strings.Builder, key, value string) {
	property(b, key, jsonString(value))
}

func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// obsidianNoteName is "<id> <first words>.md"; the ID keeps names unique.
func obsidianNoteName(e memory.ExportEntry) string {
	return strconv.FormatInt(e.Key, 10) + " " + obsidianSafe(e.Content, 60) + ".md"
}

func obsidianSummary(content string) string {
	s := strings.Join(strings.Fields(content), " ")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "…"
	}
	return s
}

// obsidianSafe keeps a note name to characters every file system and Obsidian
// link accept: no path separators, no link syntax (#^[]|) and no control
// characters, at most max runes, without trailing dots or spaces (Windows).
func obsidianSafe(s string, max int) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if n >= max {
			break
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`\/:*?"<>|#^[]`, r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		b.WriteRune(r)
		n++
	}
	out := strings.TrimRight(b.String(), ". ")
	if out == "" {
		return "memory"
	}
	return out
}
