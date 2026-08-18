package memory

import (
	"math"
	"strings"
)

// Classic Okapi BM25 parameters.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// bm25Model holds the corpus statistics (IDF per term, average document
// length) needed to score documents against a query. Built once per search
// over the candidate set loaded for that query — the corpus is small
// (hundreds to low-thousands of entries), so recomputing per query is cheap
// and needs no persistence.
type bm25Model struct {
	idf       map[string]float64
	avgDocLen float64
}

func tokenize(s string) []string {
	fields := strings.Fields(strings.ToLower(s))
	out := make([]string, len(fields))
	for i, w := range fields {
		out[i] = stem(w)
	}
	return out
}

func rawTokens(s string) []string {
	return strings.Fields(strings.ToLower(s))
}

func newBM25Model(docs [][]string) *bm25Model {
	df := make(map[string]int)
	var totalLen int
	for _, doc := range docs {
		totalLen += len(doc)
		seen := make(map[string]bool, len(doc))
		for _, w := range doc {
			if seen[w] {
				continue
			}
			seen[w] = true
			df[w]++
		}
	}
	n := float64(len(docs))
	idf := make(map[string]float64, len(df))
	for term, f := range df {
		idf[term] = math.Log(1 + (n-float64(f)+0.5)/(float64(f)+0.5))
	}
	avgDocLen := 1.0
	if len(docs) > 0 {
		avgDocLen = float64(totalLen) / n
	}
	return &bm25Model{idf: idf, avgDocLen: avgDocLen}
}

// score computes the BM25 score of doc against the query terms. Terms absent
// from doc contribute nothing; terms absent from the corpus have zero IDF.
func (m *bm25Model) score(doc []string, terms []string) float64 {
	tf := make(map[string]int, len(doc))
	for _, w := range doc {
		tf[w]++
	}
	dl := float64(len(doc))
	var score float64
	for _, term := range terms {
		f := float64(tf[term])
		if f == 0 {
			continue
		}
		denom := f + bm25K1*(1-bm25B+bm25B*dl/m.avgDocLen)
		score += m.idf[term] * (f * (bm25K1 + 1)) / denom
	}
	return score
}

// matchesAny reports whether doc contains at least one of terms. Empty terms
// (empty query) matches nothing here — callers treat an empty query as "no
// filter" separately.
func matchesAny(doc []string, terms []string) bool {
	set := make(map[string]bool, len(doc))
	for _, w := range doc {
		set[w] = true
	}
	for _, t := range terms {
		if set[t] {
			return true
		}
	}
	return false
}
