package store

// MemorySession is a session that saved memories in a project.
type MemorySession struct {
	ID         int64
	ExternalID string
	StartedAt  string
}

// MemorySessions lists the sessions that saved memories in a project, newest first.
func (s *Store) MemorySessions(projectID int64) ([]MemorySession, error) {
	rows, err := s.Query(`SELECT DISTINCT s.id, s.external_id, s.started_at FROM sessions s
		JOIN memories m ON m.session_id = s.id WHERE m.project_id = ? ORDER BY s.started_at DESC, s.id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemorySession
	for rows.Next() {
		var ms MemorySession
		if err := rows.Scan(&ms.ID, &ms.ExternalID, &ms.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, ms)
	}
	return out, rows.Err()
}
