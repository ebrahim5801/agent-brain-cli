package memory

import (
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func seed(t *testing.T, st *store.Store, projectID int64, dir, content, kind string) int64 {
	t.Helper()
	res, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: content, Kind: kind, Origin: OriginAuto})
	if err != nil {
		t.Fatal(err)
	}
	return res.ID
}

func TestSearchKeywordsAndKind(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	seed(t, st, projectID, dir, "we use pgx for Postgres access", "convention")
	seed(t, st, projectID, dir, "Postgres runs in docker locally", "fact")
	seed(t, st, projectID, dir, "retry uses exponential backoff", "decision")

	got, err := Search(st, projectID, dir, "postgres", "", 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("postgres matches = %d, want 2", len(got))
	}

	// OR-with-ranking: both entries mentioning postgres match, and the one
	// matching both keywords (pgx + postgres) ranks first.
	got, err = Search(st, projectID, dir, "POSTGRES pgx", "", 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("any-word match = %d, want 2", len(got))
	}
	if got[0].Entry.Content != "we use pgx for Postgres access" {
		t.Fatalf("best keyword match should rank first, got %q", got[0].Entry.Content)
	}

	got, err = Search(st, projectID, dir, "postgres", "fact", 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Entry.Kind != "fact" {
		t.Fatalf("kind filter = %+v", got)
	}

	// Empty query returns everything, capped by limit.
	got, err = Search(st, projectID, dir, "", "", 2, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("limited = %d, want 2", len(got))
	}
}

// End-to-end BM25 check: inflating "common"'s document frequency lowers its
// IDF, so a query mixing it with the rarer "rare" should rank the
// rare-term entry first even though both entries otherwise look alike.
func TestSearchBM25RarerTermOutranksCommon(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	seed(t, st, projectID, dir, "common setup step one", "fact")
	seed(t, st, projectID, dir, "common setup step two", "fact")
	seed(t, st, projectID, dir, "common setup step three", "fact")
	seed(t, st, projectID, dir, "we rely on common config here", "fact")
	seed(t, st, projectID, dir, "we rely on rare config here", "fact")

	got, err := Search(st, projectID, dir, "common rare", "", 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Entry.Content != "we rely on rare config here" {
		t.Fatalf("rarer term should rank first, got %+v", got)
	}
}

func TestSearchScopedToProject(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	other, _ := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/tmp/other", DisplayName: "other"}, store.Now())
	seed(t, st, projectID, dir, "mine: uses redis", "fact")
	seed(t, st, other, dir, "theirs: uses redis", "fact")

	got, err := Search(st, projectID, dir, "redis", "", 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Entry.ProjectID != projectID {
		t.Fatalf("cross-project leak: %+v", got)
	}
}

// Stemming lets a query for "running" find an entry that only says "run",
// via the shared normalization tokenize() and queryTerms() both apply.
func TestSearchStemmingMatchesMorphologicalVariant(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	seed(t, st, projectID, dir, "tests run before merging", "fact")
	seed(t, st, projectID, dir, "retry uses exponential backoff", "decision")

	got, err := Search(st, projectID, dir, "running", "", 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Entry.Content != "tests run before merging" {
		t.Fatalf("stemmed match = %+v, want the entry containing \"run\"", got)
	}
}

// Synonym expansion lets a query for "auth" find an entry that only says
// "login", since the two are mapped as equivalent dev shorthand.
func TestSearchSynonymExpansionMatchesMappedTerm(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	seed(t, st, projectID, dir, "user login flow uses sessions", "fact")
	seed(t, st, projectID, dir, "retry uses exponential backoff", "decision")

	got, err := Search(st, projectID, dir, "auth", "", 10, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Entry.Content != "user login flow uses sessions" {
		t.Fatalf("synonym match = %+v, want the entry containing \"login\"", got)
	}
}

// Repeating a term's own synonyms in the query must not inflate its BM25
// weight: "postgres", "pg", and "postgresql" are all synonyms of each other,
// so expand() should dedupe them down to the same term set regardless of
// how many of the three the query spells out.
func TestSearchSynonymExpansionDedupesWithoutDistortingRanking(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	seed(t, st, projectID, dir, "we use pgx for Postgres access", "convention")
	seed(t, st, projectID, dir, "Postgres runs in docker locally", "fact")
	seed(t, st, projectID, dir, "retry uses exponential backoff", "decision")

	now := time.Now()
	single, err := Search(st, projectID, dir, "postgres", "", 10, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := Search(st, projectID, dir, "postgres pg postgresql", "", 10, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(single) != len(repeated) {
		t.Fatalf("result count differs: single=%d repeated=%d", len(single), len(repeated))
	}
	for i := range single {
		if single[i].Entry.ID != repeated[i].Entry.ID || single[i].Score != repeated[i].Score {
			t.Fatalf("rank/score %d differs: single=%+v repeated=%+v", i, single[i], repeated[i])
		}
	}
}

func TestListIncludesSupersededOnRequest(t *testing.T) {
	st, projectID := openStore(t)
	dir := t.TempDir()
	oldID := seed(t, st, projectID, dir, "old", "fact")
	if _, err := Save(st, SaveInput{ProjectID: projectID, Dir: dir, Content: "new", Kind: "fact", Origin: OriginAuto, Supersedes: []int64{oldID}}); err != nil {
		t.Fatal(err)
	}

	active, err := List(st, projectID, dir, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Entry.Content != "new" {
		t.Fatalf("active list = %+v", active)
	}
	all, err := List(st, projectID, dir, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Entry.Status != store.MemoryActive {
		t.Fatalf("full list = %+v", all)
	}
}
