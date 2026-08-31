package memory

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func teamRowAt(uid, content, branch, commit string) store.TeamMemoryRow {
	return store.TeamMemoryRow{
		UID: uid, Author: "a@example.com", Content: content, Kind: "decision", Origin: "auto",
		Status: "active", Branch: branch, CommitHash: commit, CapturedAt: store.Now(), UpdatedAt: store.Now(),
	}
}

// T030: team entries render each freshness signal against the READER's own
// checkout, using the same comparer as personal entries.
func TestTeamFreshnessSignalsAgainstReaderCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "commit", "--allow-empty", "-m", "one")
	first := runGit(t, dir, "rev-parse", "HEAD")
	runGit(t, dir, "commit", "--allow-empty", "-m", "two")
	head := runGit(t, dir, "rev-parse", "HEAD")

	st, projectID := openStore(t)
	seedTeam(t, st, projectID,
		teamRowAt("cur", "current entry", "main", head),
		teamRowAt("br", "other branch entry", "feature/x", "deadbeefdeadbeef"),
		teamRowAt("mv", "moved entry", "main", first),
		teamRowAt("uv", "unverifiable entry", "main", "0123456789abcdef0123"),
	)

	lines, err := TeamList(st, projectID, dir, 2*time.Second, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	assertLine(t, joined, "current entry", "current")
	assertLine(t, joined, "other branch entry", "different-branch: feature/x")
	assertLine(t, joined, "moved entry", "moved-on: 1 commits since")
	assertLine(t, joined, "unverifiable entry", "unverifiable")

	// No VCS → unknown for every entry.
	nonRepo, err := TeamList(st, projectID, t.TempDir(), 500*time.Millisecond, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(nonRepo, "\n"), "unknown: no git state") {
		t.Errorf("non-repo reader should see unknown:\n%s", strings.Join(nonRepo, "\n"))
	}
}

// assertLine finds the rendered line containing content and asserts it carries
// the expected freshness token.
func assertLine(t *testing.T, joined, content, token string) {
	t.Helper()
	for _, line := range strings.Split(joined, "\n") {
		if strings.Contains(line, content) {
			if !strings.Contains(line, token) {
				t.Errorf("entry %q: want token %q in %q", content, token, line)
			}
			return
		}
	}
	t.Errorf("entry %q not found in:\n%s", content, joined)
}

func seedTeam(t *testing.T, st *store.Store, projectID int64, rows ...store.TeamMemoryRow) {
	t.Helper()
	if err := st.ApplyPull(projectID, rows); err != nil {
		t.Fatal(err)
	}
}

func teamRow(uid, author, content string) store.TeamMemoryRow {
	return store.TeamMemoryRow{
		UID: uid, Author: author, Content: content, Kind: "decision", Origin: "auto",
		Status: "active", CapturedAt: store.Now(), UpdatedAt: store.Now(),
	}
}

// T019: a fresh member with no personal entries still gets team entries, and
// team lines render with attribution.
func TestBuildPackServesTeamOnly(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	seedTeam(t, st, projectID, teamRow("u1", "alice@example.com", "retries use backoff"))

	pack, err := BuildPack(st, projectID, dir, 0, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pack.Text, "## Project memory (agent-brain)") {
		t.Error("header missing")
	}
	if !strings.Contains(pack.Text, "team · alice@example.com") {
		t.Errorf("team attribution missing in:\n%s", pack.Text)
	}
	if !strings.Contains(pack.Text, "retries use backoff") {
		t.Error("team content missing")
	}
}

func TestBuildPackMergesPersonalAndTeam(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	if _, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: "my local decision", Kind: "decision", Origin: OriginExplicit}); err != nil {
		t.Fatal(err)
	}
	seedTeam(t, st, projectID, teamRow("u1", "bob@example.com", "team decision"))

	pack, err := BuildPack(st, projectID, dir, 0, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pack.Text, "my local decision") || !strings.Contains(pack.Text, "team decision") {
		t.Errorf("merged pack missing an entry:\n%s", pack.Text)
	}
	if !strings.Contains(pack.Text, "[#") || !strings.Contains(pack.Text, "team · bob@example.com") {
		t.Errorf("both renderings should appear:\n%s", pack.Text)
	}
}

func TestTeamRenderingFormerAndContradiction(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	former := teamRow("u1", "gone@example.com", "single-threaded worker")
	former.AuthorFormer = true
	conflict := teamRow("u2", "carol@example.com", "conflicting decision")
	conflict.Contradicts = "u3"
	seedTeam(t, st, projectID, former, conflict)

	lines, err := TeamList(st, projectID, dir, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "(former member)") {
		t.Errorf("former-member marking missing:\n%s", joined)
	}
	if !strings.Contains(joined, "CONTRADICTS") {
		t.Errorf("contradiction marking missing:\n%s", joined)
	}
}

// The serving line leads with a stable [team#<handle>] the assistant can echo
// back to memory_save's supersedes_team.
func TestTeamRenderingHandle(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	seedTeam(t, st, projectID, teamRow("abc12345deadbeef", "alice@example.com", "retries use backoff"))

	lines, err := TeamList(st, projectID, dir, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "[team#abc12345]") {
		t.Errorf("handle missing (want first-8 prefix) in: %v", lines)
	}
	if TeamHandle("abc12345deadbeef") != "abc12345" {
		t.Errorf("TeamHandle = %q, want abc12345", TeamHandle("abc12345deadbeef"))
	}
}

func TestTeamSearchFiltersByKeyword(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	seedTeam(t, st, projectID,
		teamRow("u1", "a@example.com", "retries use backoff"),
		teamRow("u2", "a@example.com", "deploy on fridays"),
	)
	lines, _, err := TeamSearch(st, projectID, dir, "backoff", Filter{}, 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "retries use backoff") {
		t.Errorf("keyword search = %v", lines)
	}
}

// TeamSearch's second return is the served uids in the same order as the
// rendered lines, so a caller can record retrieval against them.
func TestTeamSearchReturnsServedUIDs(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	seedTeam(t, st, projectID,
		teamRow("u1", "a@example.com", "retries use backoff"),
		teamRow("u2", "a@example.com", "deploy on fridays"),
	)
	lines, uids, err := TeamSearch(st, projectID, dir, "backoff", Filter{}, 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(uids) != len(lines) {
		t.Fatalf("uids len %d != lines len %d", len(uids), len(lines))
	}
	if len(uids) != 1 || uids[0] != "u1" {
		t.Errorf("uids = %v, want [u1]", uids)
	}
	if !strings.Contains(lines[0], TeamHandle(uids[0])) {
		t.Errorf("rendered line %q does not carry the served uid's handle", lines[0])
	}
}

// Team path mirrors the personal path: BM25 ranks the entry matching more
// query terms first.
func TestTeamSearchBM25MultiTermRanksFirst(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	seedTeam(t, st, projectID,
		teamRow("u1", "a@example.com", "we use pgx for postgres access"),
		teamRow("u2", "a@example.com", "postgres runs in docker locally"),
		teamRow("u3", "a@example.com", "retry uses exponential backoff"),
	)

	lines, _, err := TeamSearch(st, projectID, dir, "postgres pgx", Filter{}, 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("any-word match = %d, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "we use pgx for postgres access") {
		t.Fatalf("best keyword match should rank first, got %v", lines)
	}
}

// Team path shares the same stemming + synonym expansion as personal search.
func TestTeamSearchStemmingAndSynonyms(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	seedTeam(t, st, projectID,
		teamRow("u1", "a@example.com", "tests run before merging"),
		teamRow("u2", "a@example.com", "user login flow uses sessions"),
		teamRow("u3", "a@example.com", "deploy on fridays"),
	)

	lines, _, err := TeamSearch(st, projectID, dir, "running", Filter{}, 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "tests run before merging") {
		t.Errorf("stemmed match = %v", lines)
	}

	lines, _, err = TeamSearch(st, projectID, dir, "auth", Filter{}, 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "user login flow uses sessions") {
		t.Errorf("synonym match = %v", lines)
	}
}
