package store

import (
	"database/sql"
	"errors"

	"github.com/ebrahim5801/agent-brain-cli/wire"
)

// ErrAmbiguousTeamHandle is returned when a rendered team handle prefix matches
// more than one cached entry — the caller must present a longer handle.
var ErrAmbiguousTeamHandle = errors.New("team handle matches more than one entry")

// TeamMemoryRow mirrors a server pool entry in the local read-through cache.
// The cache is disposable: dropping it loses nothing (a full pull from cursor
// ” rebuilds it from cloud truth).
type TeamMemoryRow struct {
	UID          string
	ProjectID    int64
	Author       string
	AuthorFormer bool
	Content      string
	Kind         string
	Origin       string
	Priority     string
	Status       string // active | superseded | deleted
	Contradicts  string // uid of the conflicting entry, "" if none
	Flagged      bool
	Mine         bool
	Branch       string
	CommitHash   string
	CapturedAt   string
	UpdatedAt    string
}

// priorityOrDefault normalizes a server-supplied priority. Empty is the
// older-server case. A value outside the vocabulary is coerced rather than
// stored: the Postgres client backend has a CHECK on the column, so storing one
// would abort the whole ApplyPull transaction and every later pull would replay
// the same batch and fail identically — a permanently stuck sync rather than a
// degraded one. The server validates its own writers; this is the reader's side
// of a boundary the client does not control.
func priorityOrDefault(p string) string {
	for _, valid := range wire.MemoryPriorities {
		if p == valid {
			return p
		}
	}
	return MemoryPriorityNormal
}

// ApplyPull applies a batch of pulled entries to the cache in one transaction.
// Tombstones (status 'deleted') are physically removed — the cache needs no
// history, and a deleted entry must never be served again (FR-011).
func (s *Store) ApplyPull(projectID int64, rows []TeamMemoryRow) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		if r.Status == "deleted" {
			if _, err := tx.Exec(s.Rebind(`DELETE FROM team_memories WHERE uid = ?`), r.UID); err != nil {
				return err
			}
			continue
		}
		if _, err := s.ExecTx(tx, `
            INSERT INTO team_memories
                (uid, project_id, author, author_former, content, kind, origin, priority, status,
                 contradicts, flagged, mine, branch, commit_hash, captured_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(uid) DO UPDATE SET
                author = excluded.author,
                author_former = excluded.author_former,
                content = excluded.content,
                kind = excluded.kind,
                origin = excluded.origin,
                priority = excluded.priority,
                status = excluded.status,
                contradicts = excluded.contradicts,
                flagged = excluded.flagged,
                mine = excluded.mine,
                branch = excluded.branch,
                commit_hash = excluded.commit_hash,
                captured_at = excluded.captured_at,
                updated_at = excluded.updated_at`,
			r.UID, projectID, r.Author, r.AuthorFormer, r.Content, r.Kind, r.Origin, priorityOrDefault(r.Priority), r.Status,
			nullIfEmpty(r.Contradicts), r.Flagged, r.Mine, nullIfEmpty(r.Branch), nullIfEmpty(r.CommitHash),
			r.CapturedAt, r.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListTeamMemories returns the project's active cached entries newest-first,
// for serving alongside personal memory. The reader's own contributions are
// excluded — they are already served from the personal memories table, and
// serving both would duplicate every line a sharing author writes.
func (s *Store) ListTeamMemories(projectID int64) ([]TeamMemoryRow, error) {
	rows, err := s.Query(`
        SELECT uid, project_id, author, author_former, content, kind, origin, priority, status,
               COALESCE(contradicts, ''), flagged, mine,
               COALESCE(branch, ''), COALESCE(commit_hash, ''), captured_at, updated_at
        FROM team_memories
        WHERE project_id = ? AND status = 'active' AND mine = 0
        ORDER BY captured_at DESC, uid`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TeamMemoryRow
	for rows.Next() {
		var r TeamMemoryRow
		var authorFormer, flagged, mine intBool
		if err := rows.Scan(&r.UID, &r.ProjectID, &r.Author, &authorFormer, &r.Content, &r.Kind, &r.Origin,
			&r.Priority, &r.Status, &r.Contradicts, &flagged, &mine, &r.Branch, &r.CommitHash, &r.CapturedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.AuthorFormer, r.Flagged, r.Mine = bool(authorFormer), bool(flagged), bool(mine)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResolveTeamHandle maps a rendered team-entry handle (a uid prefix, see
// memory.TeamHandle) back to the full team_uid of an active cached entry in the
// project. Returns ErrMemoryNotFound when nothing matches and
// ErrAmbiguousTeamHandle when the prefix is not unique. The lookup is not
// filtered by `mine`: a member may reference their own team entry (same-author
// supersede) or a teammate's (cross-author contradiction); the server applies
// the authorship rule either way.
func (s *Store) ResolveTeamHandle(projectID int64, handle string) (string, error) {
	rows, err := s.Query(`
        SELECT uid FROM team_memories
        WHERE project_id = ? AND status = 'active' AND uid LIKE ? || '%'
        LIMIT 2`, projectID, handle)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var uids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return "", err
		}
		uids = append(uids, uid)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(uids) {
	case 0:
		return "", ErrMemoryNotFound
	case 1:
		return uids[0], nil
	default:
		return "", ErrAmbiguousTeamHandle
	}
}

// AddTeamSupersede records that a local entry supersedes (or, cross-author,
// contradicts) a cached team entry identified by team_uid. The mapping is
// unioned into the entry's contribution Supersedes list, so the server's
// same-author/cross-author rule applies end to end. Idempotent per pair.
func (s *Store) AddTeamSupersede(memoryID int64, teamUID string) error {
	_, err := s.Exec(`
        INSERT INTO memory_team_supersedes (memory_id, team_uid) VALUES (?, ?)
        ON CONFLICT(memory_id, team_uid) DO NOTHING`, memoryID, teamUID)
	return err
}

// PullCursor returns the project's opaque pull cursor (” means full pool).
func (s *Store) PullCursor(projectID int64) (string, error) {
	var cursor string
	err := s.QueryRow(`SELECT pull_cursor FROM team_sync_state WHERE project_id = ?`, projectID).Scan(&cursor)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return cursor, err
}

func (s *Store) SetPullCursor(projectID int64, cursor string) error {
	_, err := s.Exec(`
        INSERT INTO team_sync_state (project_id, pull_cursor) VALUES (?, ?)
        ON CONFLICT(project_id) DO UPDATE SET pull_cursor = excluded.pull_cursor`,
		projectID, cursor)
	return err
}

// PullEndpoint returns the memory endpoint the cached pool and cursor belong to
// (016). Empty means the vendor cloud (or no cache yet).
func (s *Store) PullEndpoint(projectID int64) (string, error) {
	var ep string
	err := s.QueryRow(`SELECT endpoint FROM team_sync_state WHERE project_id = ?`, projectID).Scan(&ep)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return ep, err
}

// StampPullEndpoint records which endpoint the cached pool belongs to, creating
// the sync-state row with an empty cursor if absent. Used after an endpoint
// change triggers a full-pool resync (016, T022).
func (s *Store) StampPullEndpoint(projectID int64, endpoint string) error {
	_, err := s.Exec(`
        INSERT INTO team_sync_state (project_id, pull_cursor, endpoint) VALUES (?, '', ?)
        ON CONFLICT(project_id) DO UPDATE SET endpoint = excluded.endpoint`,
		projectID, endpoint)
	return err
}

// DropTeamCache clears a project's cached pool and pull cursor (used when a 403
// revokes access — serving must stop reflecting stale team state).
func (s *Store) DropTeamCache(projectID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.Rebind(`DELETE FROM team_memories WHERE project_id = ?`), projectID); err != nil {
		return err
	}
	if _, err := tx.Exec(s.Rebind(`DELETE FROM team_sync_state WHERE project_id = ?`), projectID); err != nil {
		return err
	}
	return tx.Commit()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
