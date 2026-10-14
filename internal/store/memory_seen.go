package store

import (
	"encoding/json"
	"errors"
)

func (s *Store) SessionSeenRevisions(sessionID, projectID int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	if !s.MemoryV2 {
		return out, nil
	}
	var raw string
	if err := s.QueryRow(`SELECT seen_revisions FROM memory_session_bindings WHERE session_id=? AND project_id=?`, sessionID, projectID).Scan(&raw); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RecordRetrievalRevision records the revision that was actually rendered, not
// whatever revision happens to be current when the usage transaction executes.
func (s *Store) RecordRetrievalRevision(sessionID, projectID int64, scope string, memoryID int64, teamUID string, revision int64, at string) error {
	if err := s.RecordRetrieval(sessionID, projectID, scope, memoryID, teamUID, at); err != nil {
		return err
	}
	if !s.MemoryV2 || scope != "personal" {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.lockProject(tx, projectID); err != nil {
		return err
	}
	var raw string
	if err := tx.QueryRow(s.Rebind(`SELECT seen_revisions FROM memory_session_bindings WHERE session_id=? AND project_id=?`), sessionID, projectID).Scan(&raw); err != nil {
		return err
	}
	seen := map[int64]int64{}
	if err := json.Unmarshal([]byte(raw), &seen); err != nil {
		return err
	}
	if memoryID == 0 || revision < 0 {
		return errors.New("invalid served revision")
	}
	if current, ok := seen[memoryID]; !ok || revision > current {
		seen[memoryID] = revision
	}
	data, err := json.Marshal(seen)
	if err != nil {
		return err
	}
	if _, err := s.ExecTx(tx, `UPDATE memory_session_bindings SET seen_revisions=? WHERE session_id=? AND project_id=?`, string(data), sessionID, projectID); err != nil {
		return err
	}
	return tx.Commit()
}
