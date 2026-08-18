package stats

import (
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

type Filter struct {
	Since     string // inclusive lower bound (YYYY-MM-DD), "" = no bound
	Project   string // display-name filter, "" = all projects
	Group     string // "", "daily", or "weekly"
	SessionID string // session id (sync_uid) exact or prefix, "" = all
}

type Row struct {
	Period           string `json:"period,omitempty"`
	Project          string `json:"project"`
	Sessions         int64  `json:"sessions"`
	DurationSeconds  int64  `json:"duration_seconds"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

type Totals struct {
	Sessions         int64 `json:"sessions"`
	DurationSeconds  int64 `json:"duration_seconds"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// Query aggregates sessions per project (and per period when grouped). Open
// sessions count with their duration up to the last recorded event, so an
// in-flight session never inflates totals; summaries are always recomputed
// from sessions, never stored.
func Query(st *store.Store, f Filter) ([]Row, error) {
	d := dialectFor(st.Backend)
	period := "'all'"
	switch f.Group {
	case "daily":
		period = d.dayBucket("s.started_at")
	case "weekly":
		period = d.weekBucket("s.started_at")
	}

	// Session count and duration skip sub-sessions (a subagent runs inside its
	// parent's wall clock); token sums include them — subagent tokens are real
	// additional spend.
	rows, err := st.Query(`
        SELECT `+period+` AS period,
               p.display_name,
               COUNT(CASE WHEN s.parent_session_id IS NULL THEN 1 END),
               `+d.sumBigint("CASE WHEN s.parent_session_id IS NULL THEN "+d.max0(d.durationSeconds("s"))+" ELSE 0 END")+`,
               `+d.sumBigint("s.input_tokens")+`,
               `+d.sumBigint("s.output_tokens")+`,
               `+d.sumBigint("s.cache_read_tokens")+`,
               `+d.sumBigint("s.cache_write_tokens")+`
        FROM sessions s
        JOIN projects p ON p.id = s.project_id
        WHERE (? = '' OR s.started_at >= ?)
          AND (? = '' OR p.display_name = ?)
        GROUP BY period, p.display_name
        ORDER BY period, p.display_name`,
		f.Since, f.Since, f.Project, f.Project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Period, &r.Project, &r.Sessions, &r.DurationSeconds,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens); err != nil {
			return nil, err
		}
		if f.Group == "" {
			r.Period = ""
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func Sum(rows []Row) Totals {
	var t Totals
	for _, r := range rows {
		t.Sessions += r.Sessions
		t.DurationSeconds += r.DurationSeconds
		t.InputTokens += r.InputTokens
		t.OutputTokens += r.OutputTokens
		t.CacheReadTokens += r.CacheReadTokens
		t.CacheWriteTokens += r.CacheWriteTokens
	}
	return t
}
