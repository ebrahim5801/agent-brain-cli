package memory

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func openStore(t *testing.T) (*store.Store, int64) {
	t.Helper()
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	projectID, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/tmp/p", DisplayName: "p"}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	return st, projectID
}

func TestSaveValidatesAndRedacts(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir() // not a git repo: capture state stays NULL

	res, err := Save(st, SaveInput{
		ProjectID: projectID, Dir: dir,
		Content: "deploy key is ghp_abcdefghij1234567890KLMNOP for CI",
		Kind:    "fact", Origin: OriginExplicit,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.GetMemory(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Content, "ghp_") {
		t.Errorf("secret survived: %q", m.Content)
	}
	if !strings.Contains(m.Content, "[redacted:github-token]") {
		t.Errorf("no redaction marker: %q", m.Content)
	}
	if m.Origin != OriginExplicit || m.Kind != "fact" {
		t.Errorf("entry = %+v", m)
	}
	if m.Branch.Valid || m.CommitHash.Valid {
		t.Errorf("non-repo capture recorded git state: %+v", m)
	}
}

func TestSaveRejectsInvalidInput(t *testing.T) {
	st, projectID := openStore(t)
	base := SaveInput{ProjectID: projectID, Dir: t.TempDir(), Content: "x", Kind: "decision", Origin: "auto"}

	badKind := base
	badKind.Kind = "opinion"
	if _, err := Save(st, badKind); !errors.Is(err, ErrBadKind) {
		t.Errorf("bad kind err = %v", err)
	}

	badOrigin := base
	badOrigin.Origin = "guessed"
	if _, err := Save(st, badOrigin); !errors.Is(err, ErrBadOrigin) {
		t.Errorf("bad origin err = %v", err)
	}

	empty := base
	empty.Content = "   "
	if _, err := Save(st, empty); !errors.Is(err, ErrEmptyContent) {
		t.Errorf("empty err = %v", err)
	}

	long := base
	long.Content = strings.Repeat("a", MaxContentLen+1)
	if _, err := Save(st, long); !errors.Is(err, ErrContentTooLong) {
		t.Errorf("too long err = %v", err)
	}
}

func TestSaveSupersedes(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()

	first, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: "use REST", Kind: "decision", Origin: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	otherProject, _ := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/tmp/q", DisplayName: "q"}, store.Now())
	foreign, err := Save(st, SaveInput{ProjectID: otherProject, Dir: dir, Content: "foreign", Kind: "fact", Origin: "auto"})
	if err != nil {
		t.Fatal(err)
	}

	res, err := Save(st, SaveInput{
		ProjectID: projectID, Dir: dir, Content: "use events instead", Kind: "decision", Origin: "auto",
		Supersedes: []int64{first.ID, foreign.ID, 99999},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Superseded) != 1 || res.Superseded[0] != first.ID {
		t.Errorf("superseded = %v", res.Superseded)
	}
	if len(res.Rejected) != 2 {
		t.Errorf("rejected = %v", res.Rejected)
	}
	old, _ := st.GetMemory(first.ID)
	if old.Status != store.MemorySuperseded || old.SupersededBy.Int64 != res.ID {
		t.Errorf("old entry = %+v", old)
	}
	untouched, _ := st.GetMemory(foreign.ID)
	if untouched.Status != store.MemoryActive {
		t.Error("cross-project supersede mutated a foreign entry")
	}
}

func TestSaveStripsToolCallArtifacts(t *testing.T) {
	st, projectID := openStore(t)
	base := SaveInput{ProjectID: projectID, Dir: t.TempDir(), Kind: "fact", Origin: OriginAuto}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"trailing closing tags",
			"DELETE returns 422 rather than silently succeeding.</parameter>\n</invoke>",
			"DELETE returns 422 rather than silently succeeding.",
		},
		{
			"leading opening tag",
			`<parameter name="content">the actual fact`,
			"the actual fact",
		},
		{
			"namespaced tags",
			"a durable fact" + "</" + "antml:parameter>\n" + "</" + "antml:invoke>",
			"a durable fact",
		},
		{
			"prose about xml untouched",
			"in Blade components always close the outer </div>",
			"in Blade components always close the outer </div>",
		},
		{
			"artifact tag mid-content untouched",
			"the string </invoke> appears in our parser fixtures",
			"the string </invoke> appears in our parser fixtures",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.Content = tc.in
			res, err := Save(st, in)
			if err != nil {
				t.Fatal(err)
			}
			m, err := st.GetMemory(res.ID)
			if err != nil {
				t.Fatal(err)
			}
			if m.Content != tc.want {
				t.Errorf("stored content = %q, want %q", m.Content, tc.want)
			}
		})
	}

	// Content that is nothing but artifacts is rejected as empty.
	in := base
	in.Content = "</" + "parameter>\n" + "</" + "invoke>"
	if _, err := Save(st, in); !errors.Is(err, ErrEmptyContent) {
		t.Errorf("artifact-only content err = %v", err)
	}
}

func TestEditRedactsAndPreservesProvenance(t *testing.T) {
	st, projectID := openStore(t)
	res, err := Save(st, SaveInput{ProjectID: projectID, Dir: t.TempDir(), Content: "original", Kind: "fact", Origin: "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetMemory(res.ID)

	if err := Edit(st, res.ID, "updated with password = supersecret99"); err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetMemory(res.ID)
	if strings.Contains(after.Content, "supersecret99") {
		t.Errorf("edit skipped redaction: %q", after.Content)
	}
	if !after.Edited {
		t.Error("edited marker not set")
	}
	if after.CapturedAt != before.CapturedAt {
		t.Error("edit altered captured_at")
	}
	if err := Edit(st, res.ID, "  "); !errors.Is(err, ErrEmptyContent) {
		t.Errorf("empty edit err = %v", err)
	}
}
