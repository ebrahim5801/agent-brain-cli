package store

import "time"

// LivenessHorizon is how long an open session may sit without events before
// reconciliation closes it as interrupted (at its last event's timestamp, so
// duration never inflates past actual activity).
const LivenessHorizon = 4 * time.Hour

type staleSession struct {
	id             int64
	lastActivity   string
	transcriptPath string
	assistant      string
}

// Backfill lets the caller supply transcript parsing without this package
// depending on any assistant adapter. It receives the session's assistant so
// the caller can pick that assistant's parser — a transcript must never be
// read with another assistant's format. It returns usage, the dominant model,
// the per-model breakdown, and whether anything was extracted.
type Backfill func(assistant, transcriptPath string) (Usage, string, []ModelUsage, bool)

// ReconcileStale closes open sessions whose last activity predates the
// liveness horizon. Runs on session-start, session-end, and user-facing
// commands only — never on the hot path.
func (s *Store) ReconcileStale(backfill Backfill) (int, error) {
	cutoff := time.Now().UTC().Add(-LivenessHorizon).Format(TimeLayout)
	rows, err := s.Query(`
        SELECT s.id,
               COALESCE(MAX(e.occurred_at), s.started_at) AS last_activity,
               COALESCE(s.transcript_path, ''),
               s.assistant
        FROM sessions s
        LEFT JOIN events e ON e.session_id = s.id
        WHERE s.ended_at IS NULL
        GROUP BY s.id
        HAVING COALESCE(MAX(e.occurred_at), s.started_at) < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	var stale []staleSession
	for rows.Next() {
		var st staleSession
		if err := rows.Scan(&st.id, &st.lastActivity, &st.transcriptPath, &st.assistant); err != nil {
			rows.Close()
			return 0, err
		}
		stale = append(stale, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	closed := 0
	for _, st := range stale {
		if err := s.CloseSession(st.id, st.lastActivity, "interrupted"); err != nil {
			return closed, err
		}
		closed++
		if backfill != nil && st.transcriptPath != "" {
			if u, model, models, ok := backfill(st.assistant, st.transcriptPath); ok {
				if err := s.SetUsage(st.id, model, u); err != nil {
					return closed, err
				}
				if err := s.ReplaceModelUsage(st.id, models); err != nil {
					return closed, err
				}
			}
		}
	}
	return closed, nil
}
