package store

import "database/sql"

// ModelUsage is one model's token totals within a single session. A session
// that used several models (e.g. the user switched model mid-session) has one
// row per model; the session's scalar `model` column still names the dominant
// one for cost estimation and the project-level rollup.
type ModelUsage struct {
	Model string
	Usage Usage
}

// ReplaceModelUsage swaps a session's per-model rows for an absolute snapshot,
// used by the transcript/events backfill paths that recover whole-session
// totals. Delete-then-insert (in one transaction) keeps a re-parse idempotent
// and lets a model's totals shrink as well as grow, and marks the rows and the
// owning session sync-dirty so the corrected breakdown reaches the outbox.
// Empty-model rows are skipped: they carry no attributable cost.
func (s *Store) ReplaceModelUsage(sessionID int64, rows []ModelUsage) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.ExecTx(tx, `DELETE FROM session_model_usage WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	wrote := false
	for _, r := range rows {
		if r.Model == "" {
			continue
		}
		if _, err := s.ExecTx(tx, `
            INSERT INTO session_model_usage
                (session_id, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, sync_dirty)
            VALUES (?, ?, ?, ?, ?, ?, 1)`,
			sessionID, r.Model, r.Usage.Input, r.Usage.Output, r.Usage.CacheRead, r.Usage.CacheWrite); err != nil {
			return err
		}
		wrote = true
	}
	if wrote {
		if _, err := s.ExecTx(tx, `UPDATE sessions SET sync_dirty = 1 WHERE id = ?`, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// addModelUsage accumulates a per-model usage delta within the same
// transaction AddUsage runs, so the per-model breakdown stays consistent with
// the session's aggregate counters for live-usage assistants (Gemini, OpenCode)
// that report one model-call at a time.
func (s *Store) addModelUsage(tx *sql.Tx, sessionID int64, model string, delta Usage) error {
	if model == "" {
		return nil
	}
	_, err := s.ExecTx(tx, `
        INSERT INTO session_model_usage
            (session_id, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, sync_dirty)
        VALUES (?, ?, ?, ?, ?, ?, 1)
        ON CONFLICT(session_id, model) DO UPDATE SET
            input_tokens = session_model_usage.input_tokens + excluded.input_tokens,
            output_tokens = session_model_usage.output_tokens + excluded.output_tokens,
            cache_read_tokens = session_model_usage.cache_read_tokens + excluded.cache_read_tokens,
            cache_write_tokens = session_model_usage.cache_write_tokens + excluded.cache_write_tokens,
            sync_dirty = 1`,
		sessionID, model, delta.Input, delta.Output, delta.CacheRead, delta.CacheWrite)
	return err
}

// SessionModelUsage returns a session's per-model token breakdown, heaviest
// model first, for the CLI and dashboard session views.
func (s *Store) SessionModelUsage(sessionID int64) ([]ModelUsage, error) {
	rows, err := s.Query(`
        SELECT model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
        FROM session_model_usage WHERE session_id = ?
        ORDER BY (input_tokens + output_tokens + cache_read_tokens + cache_write_tokens) DESC, model`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelUsage
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Model, &m.Usage.Input, &m.Usage.Output, &m.Usage.CacheRead, &m.Usage.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
