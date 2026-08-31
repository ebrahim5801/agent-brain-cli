package memory

import (
	"database/sql"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

var rankNow = time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

func entry(id int64, origin, capturedAt string) store.Memory {
	return store.Memory{
		ID: id, Origin: origin, CapturedAt: capturedAt,
		Branch:     sql.NullString{},
		CommitHash: sql.NullString{},
	}
}

func at(ago time.Duration) string {
	return rankNow.Add(-ago).Format(store.TimeLayout)
}

// nonRepoComparer yields unknown freshness for every entry, isolating the
// origin and recency factors.
func nonRepoComparer(t *testing.T) *gitstate.Comparer {
	t.Helper()
	return gitstate.NewComparer(t.TempDir(), 0)
}

func TestOriginWeight(t *testing.T) {
	if originWeight(OriginExplicit) != 1.5 || originWeight(OriginAuto) != 1.0 {
		t.Error("origin weights drifted from R5")
	}
}

func TestFreshnessWeight(t *testing.T) {
	cases := map[gitstate.Signal]float64{
		gitstate.Current:         1.0,
		gitstate.MovedOn:         0.8,
		gitstate.DifferentBranch: 0.6,
		gitstate.Unverifiable:    0.5,
		gitstate.Unknown:         0.5,
	}
	for sig, want := range cases {
		if got := freshnessWeight(sig); got != want {
			t.Errorf("freshnessWeight(%s) = %v, want %v", sig, got, want)
		}
	}
}

func TestRecencyDecay(t *testing.T) {
	if got := recencyDecay(at(0), rankNow); got != 1.0 {
		t.Errorf("decay(now) = %v, want 1.0", got)
	}
	got := recencyDecay(at(14*24*time.Hour), rankNow)
	if got < 0.499 || got > 0.501 {
		t.Errorf("decay(half-life) = %v, want ~0.5", got)
	}
	if got := recencyDecay("garbage", rankNow); got != 0.5 {
		t.Errorf("decay(unparsable) = %v, want 0.5 fallback", got)
	}
	// Future timestamps (clock skew) clamp to no decay rather than boosting.
	if got := recencyDecay(rankNow.Add(time.Hour).Format(store.TimeLayout), rankNow); got != 1.0 {
		t.Errorf("decay(future) = %v, want 1.0", got)
	}
}

func TestRankOrdering(t *testing.T) {
	entries := []store.Memory{
		entry(1, OriginAuto, at(30*24*time.Hour)),     // old auto: lowest
		entry(2, OriginExplicit, at(time.Hour)),       // fresh explicit: highest
		entry(3, OriginAuto, at(time.Hour)),           // fresh auto: middle
		entry(4, OriginExplicit, at(30*24*time.Hour)), // old explicit
	}
	ranked := Rank(entries, nonRepoComparer(t), rankNow)
	if len(ranked) != 4 {
		t.Fatalf("len = %d", len(ranked))
	}
	if ranked[0].Entry.ID != 2 || ranked[1].Entry.ID != 3 {
		t.Errorf("order = [%d %d %d %d], want 2,3 first", ranked[0].Entry.ID, ranked[1].Entry.ID, ranked[2].Entry.ID, ranked[3].Entry.ID)
	}
	if ranked[3].Entry.ID != 1 {
		t.Errorf("old auto should rank last, got %d", ranked[3].Entry.ID)
	}
	for _, r := range ranked {
		if r.Freshness.Signal != gitstate.Unknown {
			t.Errorf("non-repo freshness = %s, want unknown", r.Freshness.Signal)
		}
	}
}

func TestRankTieBreaksNewestFirst(t *testing.T) {
	same := at(time.Hour)
	entries := []store.Memory{
		entry(1, OriginAuto, same),
		entry(2, OriginAuto, same),
	}
	ranked := Rank(entries, nonRepoComparer(t), rankNow)
	if ranked[0].Entry.ID != 2 {
		t.Errorf("tie should break to higher ID first, got %d", ranked[0].Entry.ID)
	}
}

func TestPriorityWeights(t *testing.T) {
	for _, tc := range []struct {
		priority string
		want     float64
	}{
		{PriorityCritical, 2.0},
		{PriorityNormal, 1.0},
		{PriorityBackground, 0.5},
		{"", 1.0},
		{"urgent", 1.0},
	} {
		if got := priorityWeight(tc.priority); got != tc.want {
			t.Errorf("priorityWeight(%q) = %v, want %v", tc.priority, got, tc.want)
		}
	}
}

// The floor is the substantive half of the feature: a critical entry must stop
// aging out of the pack, while staying rankable against fresher work.
func TestCriticalDecayFloorOutranksStaleNormal(t *testing.T) {
	now := time.Now()
	sixMonthsAgo := now.Add(-180 * 24 * time.Hour).Format(store.TimeLayout)
	twoWeeksAgo := now.Add(-14 * 24 * time.Hour).Format(store.TimeLayout)

	oldCritical := entryScore(PriorityCritical, OriginAuto, gitstate.Current, sixMonthsAgo, now)
	freshNormal := entryScore(PriorityNormal, OriginAuto, gitstate.Current, twoWeeksAgo, now)
	if oldCritical <= freshNormal {
		t.Errorf("6-month critical %v did not outrank 2-week normal %v", oldCritical, freshNormal)
	}

	// Floored, not exempt: a fresh critical entry still beats a stale one, so
	// current work is not permanently buried under old critical entries.
	freshCritical := entryScore(PriorityCritical, OriginAuto, gitstate.Current, now.Format(store.TimeLayout), now)
	if freshCritical <= oldCritical {
		t.Errorf("fresh critical %v did not outrank 6-month critical %v", freshCritical, oldCritical)
	}
}

// Rows written before the column existed, and team entries pulled from a server
// that predates the field, must rank exactly as they did before.
func TestEmptyPriorityRanksAsNormal(t *testing.T) {
	now := time.Now()
	at := now.Add(-3 * 24 * time.Hour).Format(store.TimeLayout)
	if got, want := entryScore("", OriginExplicit, gitstate.MovedOn, at, now),
		entryScore(PriorityNormal, OriginExplicit, gitstate.MovedOn, at, now); got != want {
		t.Errorf("empty priority scored %v, normal scored %v", got, want)
	}
}
