package memory

import (
	"math"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// R5 ranking: score = priority weight × origin weight × freshness weight ×
// recency decay. Deterministic and transparent — no retrieval infrastructure at
// this scale.
const recencyHalfLife = 14 * 24 * time.Hour

// criticalDecayFloor bounds how far a critical entry's recency term can fall.
// Without it a critical entry still ages out (2× of 0.012 at three months is
// still nothing); with it, its worst case is roughly a two-week-old normal
// entry's score, permanently. It stops aging out without becoming unrankable,
// so fresher critical work still outranks stale critical work.
const criticalDecayFloor = 0.35

func priorityWeight(p string) float64 {
	switch p {
	case PriorityCritical:
		return 2.0
	case PriorityBackground:
		return 0.5
	default: // normal, and anything written before the column existed
		return 1.0
	}
}

func priorityDecayFloor(p string) float64 {
	if p == PriorityCritical {
		return criticalDecayFloor
	}
	return 0
}

// entryScore is the single definition of the ranking formula, shared by Rank
// and by the merged personal+team pack items.
func entryScore(priority, origin string, sig gitstate.Signal, capturedAt string, now time.Time) float64 {
	return priorityWeight(priority) * originWeight(origin) * freshnessWeight(sig) *
		math.Max(recencyDecay(capturedAt, now), priorityDecayFloor(priority))
}

func originWeight(origin string) float64 {
	if origin == OriginExplicit {
		return 1.5
	}
	return 1.0
}

func freshnessWeight(sig gitstate.Signal) float64 {
	switch sig {
	case gitstate.Current:
		return 1.0
	case gitstate.MovedOn:
		return 0.8
	case gitstate.DifferentBranch:
		return 0.6
	default: // unverifiable, unknown
		return 0.5
	}
}

func recencyDecay(capturedAt string, now time.Time) float64 {
	t, err := time.Parse(store.TimeLayout, capturedAt)
	if err != nil {
		return 0.5
	}
	age := now.Sub(t)
	if age < 0 {
		age = 0
	}
	return math.Pow(0.5, age.Hours()/recencyHalfLife.Hours())
}

// Ranked pairs an entry with its computed freshness and score.
type Ranked struct {
	Entry     store.Memory
	Freshness gitstate.Freshness
	Score     float64
}

// Rank computes freshness for each entry against the comparer's current
// state and sorts by score descending; ties break newest-first, then by
// higher ID (insertion order within a timestamp).
func Rank(entries []store.Memory, cmp *gitstate.Comparer, now time.Time) []Ranked {
	ranked := make([]Ranked, 0, len(entries))
	for _, e := range entries {
		f := cmp.Compare(e.Branch.String, e.CommitHash.String)
		score := entryScore(e.Priority, e.Origin, f.Signal, e.CapturedAt, now)
		ranked = append(ranked, Ranked{Entry: e, Freshness: f, Score: score})
	}
	sortRanked(ranked)
	return ranked
}

func sortRanked(r []Ranked) {
	// Insertion sort keeps this dependency-free and stable enough for the
	// small N here; slices.SortFunc would be equivalent.
	for i := 1; i < len(r); i++ {
		for j := i; j > 0 && rankedLess(r[j], r[j-1]); j-- {
			r[j], r[j-1] = r[j-1], r[j]
		}
	}
}

func rankedLess(a, b Ranked) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.Entry.CapturedAt != b.Entry.CapturedAt {
		return a.Entry.CapturedAt > b.Entry.CapturedAt
	}
	return a.Entry.ID > b.Entry.ID
}
