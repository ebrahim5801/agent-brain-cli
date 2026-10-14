package memory

import (
	"math"
	"os"
	"strings"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// CandidateProvider is the retrieval seam: providers return scores, never
// authorization or lifecycle decisions. A future semantic source can implement
// this interface and share the same local eligibility/rendering pipeline.
type CandidateProvider interface {
	Candidates([]RetrievalDocument, string) []RetrievalCandidate
}
type RetrievalDocument struct {
	Reference string
	Text      string
}
type RetrievalCandidate struct {
	Reference    string
	Score        float64
	MatchedTerms []string
}
type BM25Provider struct{}

func (BM25Provider) Candidates(docs []RetrievalDocument, query string) []RetrievalCandidate {
	tokens := make([][]string, len(docs))
	for i, d := range docs {
		tokens[i] = tokenize(d.Text)
	}
	model := newBM25Model(tokens)
	primary := tokenize(query)
	terms := queryTerms(query)
	direct := map[string]bool{}
	for _, t := range primary {
		direct[t] = true
	}
	queryGroups := [][]string{}
	uniqueWords := map[string]bool{}
	for _, word := range rawTokens(query) {
		if !uniqueWords[word] {
			uniqueWords[word] = true
			queryGroups = append(queryGroups, queryTerms(word))
		}
	}
	var out []RetrievalCandidate
	for i, d := range docs {
		c := RetrievalCandidate{Reference: d.Reference}
		tf := map[string]int{}
		for _, word := range tokens[i] {
			tf[word]++
		}
		for _, term := range terms {
			score := model.scoreTF(tf, len(tokens[i]), []string{term})
			if score == 0 {
				continue
			}
			weight := 0.35
			if direct[term] {
				weight = 1
			}
			c.Score += score * weight
			c.MatchedTerms = append(c.MatchedTerms, term)
		}
		if c.Score > 0 {
			covered := 0
			for _, group := range queryGroups {
				for _, term := range group {
					if tf[term] > 0 {
						covered++
						break
					}
				}
			}
			if len(queryGroups) > 0 {
				c.Score *= 1 + 0.25*float64(covered)/float64(len(queryGroups))
			}
			phrase := strings.ToLower(strings.TrimSpace(query))
			if len(primary) > 1 && strings.Contains(strings.ToLower(d.Text), phrase) {
				c.Score *= 1.15
			}
			out = append(out, c)
		}
	}
	return out
}

func useV2Ranking() bool { return os.Getenv("AGENT_BRAIN_MEMORY_V2_RANKING") != "off" }

// lexicalAdjustment bounds metadata influence. Git state never grants truth;
// durable decisions do not decay like temporary task snapshots.
func lexicalAdjustment(e store.Memory, now time.Time) float64 {
	importance := 1.0
	switch e.Priority {
	case PriorityCritical:
		importance += 0.10
	case PriorityBackground:
		importance -= 0.10
	}
	if e.Origin == OriginExplicit {
		importance += 0.05
	}
	if e.Verification == "verified_for_scope" {
		importance += 0.05
	}
	recency := recencyDecay(e.CapturedAt, now)
	switch e.Kind {
	case "decision", "convention":
		recency = math.Max(0.85, recency)
	case "fact":
		recency = math.Max(0.65, recency)
	default:
		recency = math.Max(0.35, recency)
	}
	return importance * recency
}

func contextScore(e store.Memory, sig gitstate.Signal, now time.Time) float64 {
	if !useV2Ranking() {
		return entryScore(e.Priority, e.Origin, sig, e.CapturedAt, now)
	}
	age := math.Max(recencyDecay(e.CapturedAt, now), priorityDecayFloor(e.Priority))
	switch e.Kind {
	case "decision", "convention":
		age = math.Max(age, 0.85)
	case "fact":
		age = math.Max(age, 0.65)
	default:
		age = math.Max(age, 0.35)
	}
	return priorityWeight(e.Priority) * originWeight(e.Origin) * freshnessWeight(sig) * age
}
