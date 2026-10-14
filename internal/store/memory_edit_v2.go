package store

// Explicit local edit/delete controls are retained. They are never invoked by
// automatic lifecycle management. A correction revision invalidates stale
// verification and causes the next refresh to carry the changed state.
func (s *Store) updateMemoryV2(id int64, content, at string, remove bool) error {
	m, err := s.GetMemory(id)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.lockProject(tx, m.ProjectID); err != nil {
		return err
	}
	if remove {
		res, err := s.ExecTx(tx, `UPDATE memories SET status='deleted',updated_at=? WHERE id=? AND status<>'deleted'`, at, id)
		if err := oneRow(res, err); err != nil {
			return err
		}
	} else {
		res, err := s.ExecTx(tx, `UPDATE memories SET content=?,updated_at=?,edited=1 WHERE id=?`, content, at, id)
		if err := oneRow(res, err); err != nil {
			return err
		}
	}
	if err := s.appendState(tx, m.ProjectID, id, "", "edited", "", 0, "local_user", at); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) restoreMemoryV2(id int64, at string) error {
	m, err := s.GetMemory(id)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.lockProject(tx, m.ProjectID); err != nil {
		return err
	}
	rows, err := tx.Query(s.Rebind(`SELECT r.id FROM memory_relations r WHERE r.project_id=? AND r.target_id=? AND r.relation_type='supersedes' AND (SELECT event_kind FROM memory_state_events e WHERE e.relation_id=r.id ORDER BY e.id DESC LIMIT 1)='accepted'`), m.ProjectID, id)
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
	var last string
	if err := tx.QueryRow(s.Rebind(`SELECT COALESCE((SELECT event_kind FROM memory_state_events WHERE project_id=? AND memory_id=? AND event_kind IN ('completed','reopened') ORDER BY id DESC LIMIT 1),'')`), m.ProjectID, id).Scan(&last); err != nil {
		return err
	}
	if len(ids) == 0 && m.Status == MemoryActive && last != "completed" {
		return ErrMemoryNotFound
	}
	if m.Status != MemoryActive {
		if _, err := s.ExecTx(tx, `UPDATE memories SET status='active',superseded_by=NULL,updated_at=? WHERE id=?`, at, id); err != nil {
			return err
		}
	}
	for _, rid := range ids {
		if err := s.appendState(tx, m.ProjectID, 0, rid, "retracted", "", 0, "local_user", at); err != nil {
			return err
		}
	}
	if err := s.appendState(tx, m.ProjectID, id, "", "reopened", "", 0, "local_user", at); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) wipeMemoriesV2(pid int64, at string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := s.lockProject(tx, pid); err != nil {
		return 0, err
	}
	rows, err := tx.Query(s.Rebind(`SELECT id FROM memories WHERE project_id=? AND status<>'deleted'`), pid)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if _, err := s.ExecTx(tx, `UPDATE memories SET status='deleted',updated_at=? WHERE project_id=? AND status<>'deleted'`, at, pid); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.appendState(tx, pid, id, "", "edited", "", 0, "local_user", at); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}
