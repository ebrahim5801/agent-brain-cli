package store

// RecordVerificationReview is append-only. A positive digest check confirms a
// source, not semantic validity; it never upgrades arbitrary content to verified.
func (s *Store) RecordVerificationReview(pid, id int64, sourceMatches bool) error {
	if !s.MemoryV2 {
		return ErrV2Required
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.lockProject(tx, pid); err != nil {
		return err
	}
	var owner int64
	if err := tx.QueryRow(s.Rebind(`SELECT project_id FROM memories WHERE id=?`), id).Scan(&owner); err != nil || owner != pid {
		return ErrMemoryNotFound
	}
	if sourceMatches {
		return tx.Commit()
	}
	if err := s.appendState(tx, pid, id, "", "verification_failed", "", 0, "local_verifier", Now()); err != nil {
		return err
	}
	return tx.Commit()
}
