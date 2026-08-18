package memory

import "testing"

func TestTokenizeAndMatchesAny(t *testing.T) {
	if !matchesAny(tokenize("Hello World"), tokenize("world")) {
		t.Error("expected match on case-insensitive token")
	}
	if matchesAny(tokenize("Hello World"), tokenize("xyz")) {
		t.Error("expected no match for absent term")
	}
}

// A term that appears in only one corpus document carries a higher IDF than
// one spread across most documents, so a query mixing a common and a rare
// term should rank the rare-term document first even at equal term
// frequency and document length.
func TestBM25RarerTermOutranksCommon(t *testing.T) {
	docs := [][]string{
		tokenize("common setup step one"),
		tokenize("common setup step two"),
		tokenize("common setup step three"),
		tokenize("we rely on common config here"),
		tokenize("we rely on rare config here"),
	}
	terms := tokenize("common rare")
	model := newBM25Model(docs)

	commonScore := model.score(docs[3], terms)
	rareScore := model.score(docs[4], terms)
	if rareScore <= commonScore {
		t.Fatalf("rare-term doc should outscore common-term doc: rare=%v common=%v", rareScore, commonScore)
	}
}

// A document matching every query term should outscore documents matching
// only one, even against a corpus where the single terms are individually
// rare.
func TestBM25MultiTermBeatsSingleTerm(t *testing.T) {
	docs := [][]string{
		tokenize("alpha appears here"),
		tokenize("alpha and beta both appear here"),
		tokenize("beta appears here"),
	}
	terms := tokenize("alpha beta")
	model := newBM25Model(docs)

	both := model.score(docs[1], terms)
	alphaOnly := model.score(docs[0], terms)
	betaOnly := model.score(docs[2], terms)
	if both <= alphaOnly || both <= betaOnly {
		t.Fatalf("multi-term match should beat single-term: both=%v alphaOnly=%v betaOnly=%v", both, alphaOnly, betaOnly)
	}
}

func TestBM25EmptyCorpusNoPanic(t *testing.T) {
	model := newBM25Model(nil)
	if score := model.score(nil, tokenize("anything")); score != 0 {
		t.Fatalf("empty corpus score = %v, want 0", score)
	}
}

func TestTokenizeStemsMorphologicalVariants(t *testing.T) {
	if got := tokenize("running"); got[0] != stem("run") {
		t.Errorf("tokenize(running) = %v, want stem to match run", got)
	}
	if s := stem("running"); s != stem("run") {
		t.Errorf("stem(running) = %q, stem(run) = %q, want equal", s, stem("run"))
	}
}

func TestExpandAddsSynonymsAndDedupes(t *testing.T) {
	terms := expand(rawTokens("auth"))
	if !matchesAny(tokenize("login flow"), terms) {
		t.Errorf("expand(auth) = %v, want it to reach login via synonym", terms)
	}

	a := expand(rawTokens("postgres"))
	b := expand(rawTokens("postgres pg postgresql"))
	if len(a) != len(b) {
		t.Fatalf("expand should dedupe synonyms of each other: single=%v repeated=%v", a, b)
	}
}
