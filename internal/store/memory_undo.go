package store

// RetractReplacement is an explicit restore operation. It preserves both the
// original relationship and the acceptance event, appending a retraction.
func (s *Store) RetractReplacement(projectID, id int64) error {
	if !s.MemoryV2 {
		return ErrV2Required
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.lockProject(tx, projectID); err != nil {
		return err
	}
	rows, err := tx.Query(s.Rebind(`SELECT r.id FROM memory_relations r WHERE r.project_id=? AND r.target_id=? AND r.relation_type='supersedes' AND (SELECT event_kind FROM memory_state_events e WHERE e.relation_id=r.id ORDER BY e.id DESC LIMIT 1)='accepted'`), projectID, id)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var rid string
		if err := rows.Scan(&rid); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, rid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return ErrMemoryNotFound
	}
	for _, rid := range ids {
		if err := s.appendState(tx, projectID, 0, rid, "retracted", "", 0, "local_user", Now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
