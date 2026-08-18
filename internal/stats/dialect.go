package stats

import "github.com/ebrahim5801/agent-brain-cli/internal/store"

// dialect renders the three SQL fragments that differ between SQLite and
// Postgres. Everything else in the stats queries is portable; keeping the
// divergence in named fragments (data, not scattered if-branches inside SQL
// strings) is the whole point of the funnel design.
type dialect struct{ backend store.Backend }

func dialectFor(b store.Backend) dialect { return dialect{backend: b} }

// epoch yields integer epoch-seconds for a TEXT timestamp expression. The store
// layout is valid ISO 8601, so Postgres casts it to timestamptz for arithmetic.
func (d dialect) epoch(expr string) string {
	if d.backend == store.BackendPostgres {
		return "extract(epoch from (" + expr + ")::timestamptz)::bigint"
	}
	return "CAST(strftime('%s', " + expr + ") AS INTEGER)"
}

// max0 clamps a scalar expression to a floor of 0 (SQLite scalar MAX vs
// Postgres GREATEST).
func (d dialect) max0(expr string) string {
	if d.backend == store.BackendPostgres {
		return "GREATEST(0, " + expr + ")"
	}
	return "MAX(0, " + expr + ")"
}

// dayBucket renders the day grouping label (YYYY-MM-DD). SQLite's date() takes
// the TEXT timestamp directly; Postgres formats the cast timestamptz.
func (d dialect) dayBucket(col string) string {
	if d.backend == store.BackendPostgres {
		return `to_char((` + col + `)::timestamptz, 'YYYY-MM-DD')`
	}
	return "date(" + col + ")"
}

// weekBucket renders the year-week grouping label. NOTE: the label differs at
// year boundaries (SQLite %W week-of-year vs Postgres ISO week), an accepted
// divergence (FR-007) — dual-backend tests pin per-dialect expectations rather
// than asserting byte-equal output.
func (d dialect) weekBucket(col string) string {
	if d.backend == store.BackendPostgres {
		return `to_char((` + col + `)::timestamptz, 'IYYY-"W"IW')`
	}
	return "strftime('%Y-W%W', " + col + ")"
}

// sumBigint sums an integer expression with a 0 floor. Postgres SUM over BIGINT
// yields numeric, which does not scan into a Go int64; the ::bigint cast makes
// the result scan identically on both backends.
func (d dialect) sumBigint(expr string) string {
	if d.backend == store.BackendPostgres {
		return "COALESCE(SUM(" + expr + "), 0)::bigint"
	}
	return "COALESCE(SUM(" + expr + "), 0)"
}

// durationSeconds builds the session-duration expression — epoch-seconds of the
// session's end (or last event, or start) minus its start — for a session alias.
func (d dialect) durationSeconds(s string) string {
	end := "COALESCE(" + s + ".ended_at, (SELECT MAX(e.occurred_at) FROM events e WHERE e.session_id = " + s + ".id), " + s + ".started_at)"
	return d.epoch(end) + " - " + d.epoch(s+".started_at")
}
