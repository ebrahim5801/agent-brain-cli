package memory

// synonyms is a small hand-curated, bidirectional map of dev-shorthand terms
// that should count as the same concept in a query. Grows as gaps are found.
var synonyms = map[string][]string{
	"auth":           {"login", "authentication"},
	"login":          {"auth", "authentication"},
	"authentication": {"auth", "login"},
	"pg":             {"postgres", "postgresql"},
	"postgres":       {"pg", "postgresql"},
	"postgresql":     {"pg", "postgres"},
	"db":             {"database"},
	"database":       {"db"},
	"k8s":            {"kubernetes"},
	"kubernetes":     {"k8s"},
	"env":            {"environment"},
	"environment":    {"env"},
	"repo":           {"repository"},
	"repository":     {"repo"},
}

// expand widens a query with known synonyms and applies the same stemming
// used for the corpus, so both sides normalize identically. Lookup happens
// on the raw (pre-stem) term, since the map is keyed on natural word forms;
// stemming only afterward avoids needing stemmed variants in the map. The
// seen set dedupes by stemmed form, so a synonym pair already covering each
// other (e.g. "postgres pg postgresql" in one query) doesn't add extra
// weight beyond what expanding any single one of them already contributes.
func expand(rawTerms []string) []string {
	seen := make(map[string]bool, len(rawTerms))
	var out []string
	add := func(w string) {
		s := stem(w)
		if seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, t := range rawTerms {
		add(t)
		for _, syn := range synonyms[t] {
			add(syn)
		}
	}
	return out
}

// queryTerms is the query-side counterpart to tokenize: it splits and
// lowercases like tokenize, but expands via synonyms before stemming instead
// of stemming directly, since query terms (unlike corpus content) should
// pull in known-equivalent words.
func queryTerms(query string) []string {
	return expand(rawTokens(query))
}
