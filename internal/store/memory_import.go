package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
)

// MemoryExportFlags are the stored columns an export needs beyond the snapshot,
// which reports an effective status rather than the row's own.
type MemoryExportFlags struct {
	Status       string
	SupersededBy int64
	PersonalOnly bool
	Shared       bool // has a team UID: team sync carries it, not a file
}

func (s *Store) MemoryExportFlags(projectID int64) (map[int64]MemoryExportFlags, error) {
	rows, err := s.Query(`SELECT id,status,COALESCE(superseded_by,0),personal_only,team_uid IS NOT NULL FROM memories WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]MemoryExportFlags{}
	for rows.Next() {
		var id int64
		var f MemoryExportFlags
		var personal, shared intBool
		if err := rows.Scan(&id, &f.Status, &f.SupersededBy, &personal, &shared); err != nil {
			return nil, err
		}
		f.PersonalOnly, f.Shared = bool(personal), bool(shared)
		out[id] = f
	}
	return out, rows.Err()
}

// ImportEntry is one validated entry from an export file. Key is its ID on the
// exporting machine; SupersededBy and relations refer to keys, not local IDs.
type ImportEntry struct {
	Key                   int64
	Content, Kind, Origin string
	Priority, Status      string
	SupersededBy          int64
	Completed             bool
	Rationale, Subject    string
	Scope                 string // already local: "", branch:name or checkout:path
	Branch, Commit        string
	CapturedAt, UpdatedAt string
	Edited, PersonalOnly  bool
}

type ImportRelation struct {
	Source, Target     int64
	Type, State, Scope string
	Rationale          string
}

type ImportResult struct {
	ProjectID  int64
	Imported   int
	Duplicates int
	Explicit   int // imported entries a person stated
	Critical   int
	Relations  int
	// Pending counts accepted replacements of entries already on this machine,
	// recorded as proposals instead: an import never retires a local entry.
	Pending int
	// Without memory V2 these have nowhere to go.
	ScopedSkipped    int
	CompletedSkipped int
	RelationsSkipped int
	RationalesLost   int
}

const importActor = "import"

// ImportMemories adds entries and their links to a project in one transaction,
// creating the project when this machine has not seen it. An entry already
// present with the same kind, content and capture time (even a deleted one)
// is left alone, so importing the same file twice adds nothing. With dryRun
// the transaction is rolled back, so the result previews a real import.
func (s *Store) ImportMemories(p ProjectIdentity, entries []ImportEntry, relations []ImportRelation, completeness, at string, dryRun bool) (ImportResult, error) {
	var r ImportResult
	tx, err := s.DB.Begin()
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(s.Rebind(`
        INSERT INTO projects (identity_kind, identity, display_name, first_seen_at, last_seen_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(identity) DO UPDATE SET last_seen_at = excluded.last_seen_at
        RETURNING id`), p.Kind, p.Identity, p.DisplayName, at, at).Scan(&r.ProjectID)
	if err != nil {
		return r, err
	}
	if err := s.lockProject(tx, r.ProjectID); err != nil {
		return r, err
	}
	ids := map[int64]int64{}
	existing := map[int64]bool{}
	for _, e := range entries {
		if !s.MemoryV2 && e.Scope != "" {
			r.ScopedSkipped++
			continue
		}
		if !s.MemoryV2 && e.Completed {
			r.CompletedSkipped++
			continue
		}
		var id int64
		err := tx.QueryRow(s.Rebind(`SELECT id FROM memories WHERE project_id=? AND kind=? AND content=? AND captured_at=? ORDER BY id LIMIT 1`), r.ProjectID, e.Kind, e.Content, e.CapturedAt).Scan(&id)
		if err == nil {
			ids[e.Key], existing[e.Key] = id, true
			r.Duplicates++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
		var branch, commit any
		if e.Branch != "" {
			branch = e.Branch
		}
		if e.Commit != "" {
			commit = e.Commit
		}
		err = tx.QueryRow(s.Rebind(`
            INSERT INTO memories (project_id, content, kind, origin, priority, status, branch, commit_hash, captured_at, updated_at, edited, personal_only)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            RETURNING id`), normalizeArgs([]any{r.ProjectID, e.Content, e.Kind, e.Origin, e.Priority, e.Status, branch, commit, e.CapturedAt, e.UpdatedAt, e.Edited, e.PersonalOnly})...).Scan(&id)
		if err != nil {
			return r, err
		}
		if s.MemoryV2 {
			h := sha256.Sum256([]byte(e.Content))
			if _, err := s.ExecTx(tx, `INSERT INTO memory_details(memory_id,project_id,format_version,rationale,subject_key,scope,source_handle,content_digest,completeness) VALUES(?,?,?,?,?,?,NULL,?,?)`, id, r.ProjectID, 2, e.Rationale, e.Subject, e.Scope, hex.EncodeToString(h[:]), completeness); err != nil {
				return r, err
			}
			if e.Completed {
				if err := s.appendState(tx, r.ProjectID, id, "", "completed", "", 0, importActor, at); err != nil {
					return r, err
				}
			}
		} else if e.Rationale != "" {
			r.RationalesLost++
		}
		ids[e.Key] = id
		r.Imported++
		if e.Origin == "explicit" {
			r.Explicit++
		}
		if e.Priority == MemoryPriorityCritical {
			r.Critical++
		}
	}
	for _, e := range entries {
		id, ok := ids[e.Key]
		target, found := ids[e.SupersededBy]
		if !ok || existing[e.Key] || e.SupersededBy == 0 || !found {
			continue
		}
		if _, err := s.ExecTx(tx, `UPDATE memories SET superseded_by=? WHERE id=?`, target, id); err != nil {
			return r, err
		}
	}
	for _, rel := range relations {
		source, ok1 := ids[rel.Source]
		target, ok2 := ids[rel.Target]
		if !ok1 || !ok2 || (existing[rel.Source] && existing[rel.Target]) {
			continue
		}
		if !s.MemoryV2 {
			r.RelationsSkipped++
			continue
		}
		state := rel.State
		if rel.Type == "supersedes" && state == "accepted" && existing[rel.Target] {
			state = "pending"
			r.Pending++
		}
		event := state
		if state == "pending" {
			event = "proposed"
		}
		rid := uuid.NewString()
		if _, err := s.ExecTx(tx, `INSERT INTO memory_relations(id,project_id,source_id,target_id,relation_type,scope,rationale,proposer,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, rid, r.ProjectID, source, target, rel.Type, rel.Scope, rel.Rationale, importActor, at); err != nil {
			return r, err
		}
		if err := s.appendState(tx, r.ProjectID, 0, rid, event, "", 0, importActor, at); err != nil {
			return r, err
		}
		r.Relations++
	}
	if dryRun {
		return r, nil
	}
	return r, tx.Commit()
}
