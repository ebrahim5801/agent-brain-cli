package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/redact"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// ExportFormat names the file `agent-brain memory export --format json` writes
// and `agent-brain memory import` reads. A file moves a project's memories to
// another machine or a colleague, so it holds nothing tied to this machine:
// no session or transcript references, no evidence, and no local paths (a
// project without a git remote is identified by a digest of its path).
const (
	ExportFormat  = "agent-brain-memory"
	ExportVersion = 1

	MaxExportBytes     = 32 << 20
	maxExportEntries   = 100_000
	maxExportRelations = 400_000
)

type ExportFile struct {
	Format     string           `json:"format"`
	Version    int              `json:"version"`
	ExportedAt string           `json:"exported_at"`
	Generator  string           `json:"generator,omitempty"`
	Project    ExportProject    `json:"project"`
	Entries    []ExportEntry    `json:"entries"`
	Relations  []ExportRelation `json:"relations"`
}

type ExportProject struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Remote         string `json:"remote,omitempty"` // only for a project identified by its git remote
	IdentityDigest string `json:"identity_sha256"`
}

// ExportEntry scopes are "", "branch:<name>", or "checkout" for the checkout
// the export ran in; import maps "checkout" to the checkout it runs in.
type ExportEntry struct {
	Key          int64  `json:"key"`
	Kind         string `json:"kind"`
	Origin       string `json:"origin"`
	Priority     string `json:"priority"`
	Status       string `json:"status"`
	Completed    bool   `json:"completed,omitempty"`
	SupersededBy int64  `json:"superseded_by,omitempty"`
	Content      string `json:"content"`
	Rationale    string `json:"rationale,omitempty"`
	SubjectKey   string `json:"subject_key,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Commit       string `json:"commit,omitempty"`
	CapturedAt   string `json:"captured_at"`
	UpdatedAt    string `json:"updated_at"`
	Edited       bool   `json:"edited,omitempty"`
	PersonalOnly bool   `json:"personal_only,omitempty"`
}

type ExportRelation struct {
	Source    int64  `json:"source"`
	Target    int64  `json:"target"`
	Type      string `json:"type"`
	State     string `json:"state"`
	Scope     string `json:"scope,omitempty"`
	Rationale string `json:"rationale,omitempty"`
}

// ExportNotes counts entries an export left out on purpose.
type ExportNotes struct {
	Shared         int // team sync carries these
	OtherCheckouts int // scoped to another checkout on this machine
}

func identityDigest(identity string) string {
	h := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(h[:])
}

// networkRemote reports whether a canonical remote names a host. A remote that
// is a folder on this machine (/srv/repo, file:///srv/repo, C:/repo, ../repo)
// is a local path, so the file keeps only its digest.
func networkRemote(canonical string) bool {
	host, _, _ := strings.Cut(canonical, "/")
	isDrive := len(host) == 2 && host[1] == ':'
	return host != "" && host != "." && host != ".." && !strings.HasPrefix(host, "~") && !isDrive && !strings.Contains(canonical, `\`)
}

// ProjectMatches reports whether f was exported from project p.
func (f ExportFile) ProjectMatches(p attribution.Project) bool {
	return f.Project.IdentityDigest == identityDigest(p.Identity)
}

// BuildExport collects a project's memories as seen from dir: project-wide
// entries, every branch's entries and this checkout's. Without all, retired
// entries (deleted, superseded, completed) are left out.
func BuildExport(st *store.Store, projectID int64, p attribution.Project, dir string, all bool, generator string, now time.Time) (ExportFile, ExportNotes, error) {
	f := ExportFile{Format: ExportFormat, Version: ExportVersion, ExportedAt: now.UTC().Format(store.TimeLayout), Generator: generator,
		Project: ExportProject{Name: p.DisplayName, Kind: p.Kind, IdentityDigest: identityDigest(p.Identity)},
		Entries: []ExportEntry{}, Relations: []ExportRelation{}}
	if p.Kind == attribution.KindRemote && networkRemote(p.Identity) {
		f.Project.Remote = p.Identity
	}
	var notes ExportNotes
	if projectID == 0 {
		return f, notes, nil
	}
	snapshot, err := st.MemorySnapshot(projectID, map[string]bool{"*": true})
	if err != nil {
		return f, notes, err
	}
	flags, err := st.MemoryExportFlags(projectID)
	if err != nil {
		return f, notes, err
	}
	here := "checkout:" + store.CheckoutIdentity(dir)
	localScope := func(scope string) (string, bool) {
		switch {
		case scope == "" || strings.HasPrefix(scope, "branch:"):
			return scope, true
		case scope == here:
			return "checkout", true
		}
		return "", false
	}
	completed := map[int64]bool{}
	for _, e := range snapshot.Events {
		switch e.Kind {
		case "completed":
			completed[e.MemoryID] = true
		case "reopened":
			completed[e.MemoryID] = false
		}
	}
	retired := map[int64]bool{}
	for _, r := range snapshot.Relations {
		if r.Type == "supersedes" && r.State == "accepted" && (r.Scope == "" || r.Scope == here) {
			retired[r.TargetID] = true
		}
	}
	exported := map[int64]bool{}
	// The snapshot is newest first; a file reads (and imports) oldest first.
	for _, m := range slices.Backward(snapshot.Entries) {
		fl := flags[m.ID]
		scope, ok := localScope(m.Details.Scope)
		switch {
		case fl.Shared:
			notes.Shared++
			continue
		case !ok:
			notes.OtherCheckouts++
			continue
		case !all && (fl.Status != store.MemoryActive || completed[m.ID] || retired[m.ID]):
			continue
		}
		exported[m.ID] = true
		f.Entries = append(f.Entries, ExportEntry{
			Key: m.ID, Kind: m.Kind, Origin: m.Origin, Priority: m.Priority, Status: fl.Status,
			Completed: completed[m.ID], SupersededBy: fl.SupersededBy, Content: m.Content,
			Rationale: m.Details.Rationale, SubjectKey: m.Details.Subject, Scope: scope,
			Branch: m.Branch.String, Commit: m.CommitHash.String, CapturedAt: m.CapturedAt, UpdatedAt: m.UpdatedAt,
			Edited: m.Edited, PersonalOnly: fl.PersonalOnly,
		})
	}
	for i := range f.Entries {
		if !exported[f.Entries[i].SupersededBy] {
			f.Entries[i].SupersededBy = 0
		}
	}
	for _, r := range snapshot.Relations {
		scope, ok := localScope(r.Scope)
		if !ok || !exported[r.SourceID] || !exported[r.TargetID] {
			continue
		}
		f.Relations = append(f.Relations, ExportRelation{Source: r.SourceID, Target: r.TargetID, Type: r.Type, State: r.State, Scope: scope, Rationale: r.Rationale})
	}
	return f, notes, nil
}

// ReadExport parses and checks an export file. Text gets the same masking and
// limits as a normal save; anything unexpected rejects the whole file, so an
// import is all or nothing.
func ReadExport(r io.Reader) (ExportFile, error) {
	var f ExportFile
	data, err := io.ReadAll(io.LimitReader(r, MaxExportBytes+1))
	if err != nil {
		return f, err
	}
	if len(data) > MaxExportBytes {
		return f, fmt.Errorf("file is larger than %d MB", MaxExportBytes>>20)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("not an agent-brain memory file: %w", err)
	}
	if dec.More() {
		return f, errors.New("not an agent-brain memory file: unexpected data after the export")
	}
	if f.Format != ExportFormat {
		return f, errors.New("not an agent-brain memory file (made with `agent-brain memory export --format json`)")
	}
	if f.Version > ExportVersion {
		return f, fmt.Errorf("this file has format version %d, newer than this agent-brain reads; update agent-brain first", f.Version)
	}
	if f.Version != ExportVersion {
		return f, fmt.Errorf("unsupported format version %d", f.Version)
	}
	if len(f.Entries) > maxExportEntries || len(f.Relations) > maxExportRelations {
		return f, errors.New("file holds more entries than an import accepts")
	}
	if len(f.Project.IdentityDigest) != 64 || !isHex(f.Project.IdentityDigest) {
		return f, errors.New("file does not say which project it came from")
	}
	if f.Project.Name, err = cleanText("project name", f.Project.Name, 256); err != nil {
		return f, err
	}
	if f.Project.Remote, err = cleanText("project remote", f.Project.Remote, 1024); err != nil {
		return f, err
	}
	if f.Generator, err = cleanText("generator", f.Generator, 64); err != nil {
		return f, err
	}
	keys := map[int64]bool{}
	for i := range f.Entries {
		e := &f.Entries[i]
		if err := cleanEntry(e); err != nil {
			return f, fmt.Errorf("entry %d: %w", e.Key, err)
		}
		if keys[e.Key] {
			return f, fmt.Errorf("entry %d appears twice", e.Key)
		}
		keys[e.Key] = true
	}
	for i := range f.Entries {
		if e := &f.Entries[i]; !keys[e.SupersededBy] {
			e.SupersededBy = 0
		}
	}
	type link struct {
		source, target int64
		kind           string
	}
	seen := map[link]bool{}
	relations := f.Relations[:0]
	for _, r := range f.Relations {
		if !keys[r.Source] || !keys[r.Target] || r.Source == r.Target {
			return f, fmt.Errorf("link %d -> %d does not join two entries of the file", r.Source, r.Target)
		}
		if !slices.Contains([]string{"supersedes", "contradicts", "supports", "derived_from"}, r.Type) {
			return f, fmt.Errorf("link %d -> %d has unknown type %q", r.Source, r.Target, r.Type)
		}
		if !slices.Contains([]string{"pending", "accepted", "rejected", "retracted"}, r.State) {
			return f, fmt.Errorf("link %d -> %d has unknown state %q", r.Source, r.Target, r.State)
		}
		if err := checkScope(r.Scope); err != nil {
			return f, fmt.Errorf("link %d -> %d: %w", r.Source, r.Target, err)
		}
		if r.Rationale, err = cleanText("rationale", r.Rationale, MaxContentLen); err != nil {
			return f, fmt.Errorf("link %d -> %d: %w", r.Source, r.Target, err)
		}
		if k := (link{r.Source, r.Target, r.Type}); !seen[k] {
			seen[k] = true
			relations = append(relations, r)
		}
	}
	f.Relations = relations
	return f, nil
}

func cleanEntry(e *ExportEntry) error {
	if e.Key <= 0 {
		return errors.New("missing key")
	}
	if !ValidKind(e.Kind) {
		return fmt.Errorf("unknown kind %q", e.Kind)
	}
	if e.Origin != "auto" && e.Origin != "explicit" {
		return fmt.Errorf("unknown origin %q", e.Origin)
	}
	if e.Priority == "" {
		e.Priority = store.MemoryPriorityNormal
	}
	if !slices.Contains([]string{store.MemoryPriorityCritical, store.MemoryPriorityNormal, store.MemoryPriorityBackground}, e.Priority) {
		return fmt.Errorf("unknown priority %q", e.Priority)
	}
	if !slices.Contains([]string{store.MemoryActive, store.MemorySuperseded, store.MemoryDeleted}, e.Status) {
		return fmt.Errorf("unknown status %q", e.Status)
	}
	if e.Completed && e.Kind != "task_state" {
		return errors.New("only a task note can be completed")
	}
	e.Content = strings.TrimSpace(redact.Apply(stripToolArtifacts(e.Content)))
	if e.Content == "" {
		return errors.New("empty content")
	}
	if utf8.RuneCountInString(e.Content) > MaxContentLen {
		return ErrContentTooLong
	}
	var err error
	if e.Rationale, err = cleanText("rationale", e.Rationale, MaxContentLen); err != nil {
		return err
	}
	if e.SubjectKey, err = cleanText("subject_key", e.SubjectKey, 256); err != nil {
		return err
	}
	if err := checkScope(e.Scope); err != nil {
		return err
	}
	if e.Branch, err = cleanText("branch", e.Branch, 256); err != nil {
		return err
	}
	if len(e.Commit) > 64 || !isHex(e.Commit) {
		return errors.New("commit is not a git commit ID")
	}
	if e.CapturedAt, err = cleanTime(e.CapturedAt); err != nil {
		return fmt.Errorf("captured_at: %w", err)
	}
	if e.UpdatedAt == "" {
		e.UpdatedAt = e.CapturedAt
	}
	if e.UpdatedAt, err = cleanTime(e.UpdatedAt); err != nil {
		return fmt.Errorf("updated_at: %w", err)
	}
	return nil
}

// cleanText masks secrets and rejects control characters in one-line fields
// and text over limit bytes (runes for MaxContentLen-sized prose).
func cleanText(field, s string, limit int) (string, error) {
	s = strings.TrimSpace(redact.Apply(s))
	if limit == MaxContentLen {
		if utf8.RuneCountInString(s) > limit {
			return "", fmt.Errorf("%s is longer than %d characters", field, limit)
		}
		return s, nil
	}
	if len(s) > limit || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%s is not a valid one-line value", field)
	}
	return s, nil
}

func checkScope(scope string) error {
	name, isBranch := strings.CutPrefix(scope, "branch:")
	switch {
	case scope == "" || scope == "checkout":
		return nil
	case isBranch && name != "" && len(scope) <= 1024 && strings.IndexFunc(name, unicode.IsControl) < 0:
		return nil
	}
	return fmt.Errorf("scope %q is not project-wide, branch:<name> or checkout", scope)
}

func cleanTime(s string) (string, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return "", errors.New("not a timestamp")
	}
	return t.UTC().Format(store.TimeLayout), nil
}

func isHex(s string) bool {
	return strings.Trim(s, "0123456789abcdef") == ""
}

// ImportRecords converts a checked file for the store, mapping the "checkout"
// scope to the checkout in dir.
func (f ExportFile) ImportRecords(dir string) ([]store.ImportEntry, []store.ImportRelation) {
	here := "checkout:" + store.CheckoutIdentity(dir)
	local := func(scope string) string {
		if scope == "checkout" {
			return here
		}
		return scope
	}
	entries := make([]store.ImportEntry, 0, len(f.Entries))
	for _, e := range f.Entries {
		entries = append(entries, store.ImportEntry{
			Key: e.Key, Content: e.Content, Kind: e.Kind, Origin: e.Origin, Priority: e.Priority, Status: e.Status,
			SupersededBy: e.SupersededBy, Completed: e.Completed, Rationale: e.Rationale, Subject: e.SubjectKey,
			Scope: local(e.Scope), Branch: e.Branch, Commit: e.Commit, CapturedAt: e.CapturedAt, UpdatedAt: e.UpdatedAt,
			Edited: e.Edited, PersonalOnly: e.PersonalOnly,
		})
	}
	relations := make([]store.ImportRelation, 0, len(f.Relations))
	for _, r := range f.Relations {
		relations = append(relations, store.ImportRelation{Source: r.Source, Target: r.Target, Type: r.Type, State: r.State, Scope: local(r.Scope), Rationale: r.Rationale})
	}
	return entries, relations
}
