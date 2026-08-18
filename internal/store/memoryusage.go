package store

// MemoryUsageRef is one retrieval row of a session, identifying which memory
// (personal or team) was served — the allow-list the citation pass checks a
// referenced id/uid against.
type MemoryUsageRef struct {
	Scope    string
	MemoryID int64
	TeamUID  string
}

// RecordRetrieval upserts a retrieval for one served memory entry within a
// session: a first sighting inserts the row with retrieved = 1, a repeat
// increments it. The owning session is marked sync_dirty in the same
// transaction — retrieval can originate in the MCP server process, which never
// otherwise touches the session row, so without this the retrieval would never
// reach the sync outbox.
func (s *Store) RecordRetrieval(sessionID, projectID int64, scope string, memoryID int64, teamUID string, at string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.ExecTx(tx, `
        INSERT INTO memory_usage_events (session_id, project_id, scope, memory_id, team_uid, retrieved, first_at, last_at)
        VALUES (?, ?, ?, NULLIF(?, 0), NULLIF(?, ''), 1, ?, ?)
        ON CONFLICT(session_id, scope, COALESCE(memory_id, 0), COALESCE(team_uid, '')) DO UPDATE SET
            retrieved = memory_usage_events.retrieved + 1,
            last_at = excluded.last_at,
            sync_dirty = 1`,
		sessionID, projectID, scope, memoryID, teamUID, at, at); err != nil {
		return err
	}
	if _, err := s.ExecTx(tx, `UPDATE sessions SET sync_dirty = 1 WHERE id = ?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkCited flips cited on an existing retrieval row. It never creates a row:
// a citation without a matching retrieval this session is not recorded (the
// false-positive guard lives with the caller, which checks
// SessionRetrievedRefs before calling this). Idempotent — re-marking an
// already-cited row is a no-op that leaves the session untouched.
func (s *Store) MarkCited(sessionID int64, scope string, memoryID int64, teamUID string, at string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := s.ExecTx(tx, `
        UPDATE memory_usage_events SET cited = 1, last_at = ?, sync_dirty = 1
        WHERE session_id = ? AND scope = ? AND COALESCE(memory_id, 0) = ? AND COALESCE(team_uid, '') = ? AND cited = 0`,
		at, sessionID, scope, memoryID, teamUID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tx.Commit()
	}
	if _, err := s.ExecTx(tx, `UPDATE sessions SET sync_dirty = 1 WHERE id = ?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// SessionRetrievedRefs returns every memory a session retrieved, for the
// citation pass's intersection guard.
func (s *Store) SessionRetrievedRefs(sessionID int64) ([]MemoryUsageRef, error) {
	rows, err := s.Query(`
        SELECT scope, COALESCE(memory_id, 0), COALESCE(team_uid, '')
        FROM memory_usage_events WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryUsageRef
	for rows.Next() {
		var r MemoryUsageRef
		if err := rows.Scan(&r.Scope, &r.MemoryID, &r.TeamUID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
