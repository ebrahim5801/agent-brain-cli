package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
)

type NewRelation struct {
	TargetID int64  `json:"target_id"`
	Type     string `json:"type"`
}

type MemoryEvidence struct {
	Type       string `json:"type"`
	Locator    string `json:"locator"`
	Digest     string `json:"digest"`
	Excerpt    string `json:"excerpt"`
	Status     string `json:"status"`
	VerifiedAt string `json:"verified_at,omitempty"`
	Action     string `json:"-"` // verifier-derived, never supplied by an MCP caller
}

func (s *Store) writeLifecycle(tx *sql.Tx, id int64, w V2Write, at string, r *V2WriteResult) error {
	evidenceID := ""
	actions := map[string]string{}
	for _, e := range w.Evidence {
		eid := uuid.NewString()
		if _, err := s.ExecTx(tx, `INSERT INTO memory_evidence(id,project_id,memory_id,source_type,locator,digest,excerpt,status,observed_at,verified_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, eid, w.Memory.ProjectID, id, e.Type, e.Locator, e.Digest, e.Excerpt, e.Status, at, e.VerifiedAt); err != nil {
			return err
		}
		if e.Type == "user_message" && e.Status == "verified_for_scope" && !w.DisableTransitions {
			evidenceID = eid
			actions[e.Action] = eid
		}
	}
	if evidenceID != "" {
		if err := s.appendState(tx, w.Memory.ProjectID, id, "", "verified", evidenceID, 0, w.Details.SourceHandle, at); err != nil {
			return err
		}
	}
	for _, rel := range w.Relations {
		if rel.Type != "contradicts" && rel.Type != "supports" && rel.Type != "derived_from" {
			return ErrCaptureConflict
		}
		var owner int64
		if err := tx.QueryRow(s.Rebind(`SELECT project_id FROM memories WHERE id=?`), rel.TargetID).Scan(&owner); err != nil || owner != w.Memory.ProjectID || rel.TargetID == id {
			return ErrAttribution
		}
		rid := uuid.NewString()
		if _, err := s.ExecTx(tx, `INSERT INTO memory_relations(id,project_id,source_id,target_id,relation_type,scope,rationale,proposer,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, rid, owner, id, rel.TargetID, rel.Type, w.Details.Scope, w.Details.Rationale, w.Details.SourceHandle, at); err != nil {
			return err
		}
		if err := s.appendState(tx, owner, 0, rid, "proposed", "", 0, w.Details.SourceHandle, at); err != nil {
			return err
		}
	}
	r.Rejected = map[int64]string{}
	seen := map[int64]bool{}
	for _, target := range w.Supersedes {
		if seen[target] {
			continue
		}
		seen[target] = true
		var pid int64
		var state, kind string
		err := tx.QueryRow(s.Rebind(`SELECT project_id,status,kind FROM memories WHERE id=?`), target).Scan(&pid, &state, &kind)
		if errors.Is(err, sql.ErrNoRows) || pid != w.Memory.ProjectID || target == id || state != MemoryActive {
			r.Rejected[target] = "not an active entry of this project"
			continue
		}
		if err != nil {
			return err
		}
		var targetScope string
		scopeErr := tx.QueryRow(s.Rebind(`SELECT scope FROM memory_details WHERE memory_id=? AND project_id=?`), target, pid).Scan(&targetScope)
		if scopeErr != nil && !errors.Is(scopeErr, sql.ErrNoRows) {
			return scopeErr
		}
		var retired int
		if err := tx.QueryRow(s.Rebind(`SELECT COUNT(*) FROM memory_relations r WHERE r.project_id=? AND r.target_id=? AND r.scope=? AND r.relation_type='supersedes' AND (SELECT event_kind FROM memory_state_events e WHERE e.relation_id=r.id ORDER BY e.id DESC LIMIT 1)='accepted'`), pid, target, targetScope).Scan(&retired); err != nil {
			return err
		}
		if retired > 0 {
			r.Rejected[target] = "entry is no longer the current decision head"
			continue
		}
		rid := uuid.NewString()
		eid := actions[fmt.Sprintf("replace:%d", target)]
		var revision int64
		if err := tx.QueryRow(s.Rebind(`SELECT COALESCE(MAX(e.id),0) FROM memory_state_events e LEFT JOIN memory_relations rel ON rel.id=e.relation_id WHERE e.project_id=? AND (e.memory_id=? OR rel.target_id=? OR rel.source_id=?)`), pid, target, target, target).Scan(&revision); err != nil {
			return err
		}
		var ef any
		if eid != "" {
			ef = eid
		}
		if _, err := s.ExecTx(tx, `INSERT INTO memory_relations(id,project_id,source_id,target_id,relation_type,scope,rationale,evidence_id,proposer,created_at) VALUES(?,?,?,?,'supersedes',?,?,?,?,?)`, rid, pid, id, target, w.Details.Scope, w.Details.Rationale, ef, w.Details.SourceHandle, at); err != nil {
			return err
		}
		if err := s.appendState(tx, pid, 0, rid, "proposed", eid, revision, w.Details.SourceHandle, at); err != nil {
			return err
		}
		if eid == "" || revision != w.ExpectedRevision || kind != "decision" || w.Memory.Kind != "decision" || targetScope != w.Details.Scope {
			r.Pending = append(r.Pending, target)
			continue
		}
		if err := s.appendState(tx, pid, 0, rid, "accepted", eid, revision, w.Details.SourceHandle, at); err != nil {
			return err
		}
		r.Accepted = append(r.Accepted, target)
	}
	for _, target := range w.Completes {
		if seen[target] {
			continue
		}
		seen[target] = true
		var pid int64
		var kind, state string
		err := tx.QueryRow(s.Rebind(`SELECT project_id,kind,status FROM memories WHERE id=?`), target).Scan(&pid, &kind, &state)
		if errors.Is(err, sql.ErrNoRows) || pid != w.Memory.ProjectID || kind != "task_state" || state != MemoryActive {
			r.Rejected[target] = "not an active task of this project"
			continue
		}
		if err != nil {
			return err
		}
		var taskState string
		if err := tx.QueryRow(s.Rebind(`SELECT COALESCE((SELECT event_kind FROM memory_state_events WHERE project_id=? AND memory_id=? AND event_kind IN ('completed','reopened') ORDER BY id DESC LIMIT 1),'')`), pid, target).Scan(&taskState); err != nil {
			return err
		}
		if taskState == "completed" {
			r.Rejected[target] = "task is already completed"
			continue
		}
		eid := actions[fmt.Sprintf("complete:%d", target)]
		if eid == "" {
			eid = actions["complete_subject:"+w.Details.Subject]
		}
		var revision int64
		if err := tx.QueryRow(s.Rebind(`SELECT COALESCE(MAX(e.id),0) FROM memory_state_events e LEFT JOIN memory_relations rel ON rel.id=e.relation_id WHERE e.project_id=? AND (e.memory_id=? OR rel.target_id=? OR rel.source_id=?)`), pid, target, target, target).Scan(&revision); err != nil {
			return err
		}
		var targetScope string
		scopeErr := tx.QueryRow(s.Rebind(`SELECT scope FROM memory_details WHERE memory_id=? AND project_id=?`), target, pid).Scan(&targetScope)
		if scopeErr != nil && !errors.Is(scopeErr, sql.ErrNoRows) {
			return scopeErr
		}
		if eid == "" || revision != w.ExpectedRevision || targetScope != w.Details.Scope {
			r.Pending = append(r.Pending, target)
			continue
		}
		if err := s.appendState(tx, pid, target, "", "completed", eid, revision, w.Details.SourceHandle, at); err != nil {
			return err
		}
		r.Completed = append(r.Completed, target)
	}
	if len(r.Completed) > 0 {
		if err := s.appendState(tx, w.Memory.ProjectID, id, "", "completed", evidenceID, 0, w.Details.SourceHandle, at); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) appendState(tx *sql.Tx, pid, mid int64, rid, kind, eid string, revision int64, actor, at string) error {
	var memoryID, relationID, evidenceID any
	if mid != 0 {
		memoryID = mid
	}
	if rid != "" {
		relationID = rid
	}
	if eid != "" {
		evidenceID = eid
	}
	_, err := s.ExecTx(tx, `INSERT INTO memory_state_events(project_id,memory_id,relation_id,event_kind,evidence_id,expected_revision,actor,occurred_at,operation_id) VALUES(?,?,?,?,?,?,?,?,?)`, pid, memoryID, relationID, kind, evidenceID, revision, actor, at, uuid.NewString())
	return err
}

type RelationView struct {
	ID                 string
	Rationale          string
	EvidenceID         string
	Proposer           string
	CreatedAt          string
	SourceID, TargetID int64
	Type, Scope, State string
}
type StateView struct {
	ID, MemoryID                            int64
	ExpectedRevision                        int64
	RelationID, Kind, At, EvidenceID, Actor string
}
type EvidenceView struct {
	ID       string `json:"id"`
	MemoryID int64  `json:"memory_id"`
	MemoryEvidence
}

type MemoryHistory struct {
	Evidence  []EvidenceView
	Entries   []Memory
	Relations []RelationView
	Events    []StateView
}

// MemorySnapshot reads original records and all overlays in one transaction.
// Returning an effective status never writes that status into a historical row.
func (s *Store) MemorySnapshot(projectID int64, scopes map[string]bool) (MemoryHistory, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return MemoryHistory{}, err
	}
	defer tx.Rollback()
	if s.Backend == BackendPostgres {
		if _, err := tx.Exec(`SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
			return MemoryHistory{}, err
		}
	}
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil {
		return MemoryHistory{}, err
	}
	if version > 16 || (version >= 16) != s.MemoryV2 {
		return MemoryHistory{}, ErrMemoryRestartRequired
	}
	result := MemoryHistory{}
	index := map[int64]int{}
	rows, err := tx.Query(s.Rebind(`SELECT `+memoryColumns+` FROM memories WHERE project_id=? ORDER BY captured_at DESC,id DESC`), projectID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			rows.Close()
			return result, err
		}
		index[m.ID] = len(result.Entries)
		result.Entries = append(result.Entries, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if !s.MemoryV2 {
		return result, tx.Commit()
	}
	rows, err = tx.Query(s.Rebind(`SELECT memory_id,rationale,subject_key,scope,content_digest,completeness,COALESCE(source_handle,'') FROM memory_details WHERE project_id=?`), projectID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var id int64
		var d MemoryDetails
		if err := rows.Scan(&id, &d.Rationale, &d.Subject, &d.Scope, &d.ContentDigest, &d.Completeness, &d.SourceHandle); err != nil {
			rows.Close()
			return result, err
		}
		if i, ok := index[id]; ok {
			result.Entries[i].Details = d
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.Query(s.Rebind(`SELECT id,memory_id,source_type,locator,digest,excerpt,status,verified_at FROM memory_evidence WHERE project_id=? ORDER BY observed_at,id`), projectID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var v EvidenceView
		if err := rows.Scan(&v.ID, &v.MemoryID, &v.Type, &v.Locator, &v.Digest, &v.Excerpt, &v.Status, &v.VerifiedAt); err != nil {
			rows.Close()
			return result, err
		}
		result.Evidence = append(result.Evidence, v)
		id, status, at := v.MemoryID, v.Status, v.VerifiedAt
		if i, ok := index[id]; ok {
			m := &result.Entries[i]
			h := sha256.Sum256([]byte(m.Content))
			if m.Details.ContentDigest == hex.EncodeToString(h[:]) && (status == "verified_for_scope" || m.Verification != "verified_for_scope") {
				m.Verification = status
				m.VerifiedAt = at
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.Query(s.Rebind(`SELECT id,source_id,target_id,relation_type,scope,rationale,COALESCE(evidence_id,''),proposer,created_at FROM memory_relations WHERE project_id=? ORDER BY created_at,id`), projectID)
	if err != nil {
		return result, err
	}
	relindex := map[string]int{}
	for rows.Next() {
		var r RelationView
		if err := rows.Scan(&r.ID, &r.SourceID, &r.TargetID, &r.Type, &r.Scope, &r.Rationale, &r.EvidenceID, &r.Proposer, &r.CreatedAt); err != nil {
			rows.Close()
			return result, err
		}
		r.State = "pending"
		relindex[r.ID] = len(result.Relations)
		result.Relations = append(result.Relations, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.Query(s.Rebind(`SELECT id,COALESCE(memory_id,0),COALESCE(relation_id,''),event_kind,occurred_at,COALESCE(evidence_id,''),actor,expected_revision FROM memory_state_events WHERE project_id=? ORDER BY id`), projectID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var e StateView
		if err := rows.Scan(&e.ID, &e.MemoryID, &e.RelationID, &e.Kind, &e.At, &e.EvidenceID, &e.Actor, &e.ExpectedRevision); err != nil {
			rows.Close()
			return result, err
		}
		result.Events = append(result.Events, e)
		if i, ok := relindex[e.RelationID]; ok {
			result.Relations[i].State = e.Kind
			if e.Kind == "proposed" {
				result.Relations[i].State = "pending"
			}
			r := result.Relations[i]
			if j, ok := index[r.TargetID]; ok {
				result.Entries[j].Revision = e.ID
			}
			if j, ok := index[r.SourceID]; ok {
				result.Entries[j].Revision = e.ID
			}
		}
		if i, ok := index[e.MemoryID]; ok {
			m := &result.Entries[i]
			m.Revision = e.ID
			if m.Status == MemoryActive || m.Status == "completed" {
				switch e.Kind {
				case "completed":
					m.Status = "completed"
				case "reopened":
					m.Status = MemoryActive
				case "verification_failed", "edited":
					m.Verification = "verification_failed"
				}
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, r := range result.Relations {
		if r.Scope != "" && !scopes[r.Scope] && !scopes["*"] {
			continue
		}
		target, ok := index[r.TargetID]
		if !ok {
			continue
		}
		source, ok := index[r.SourceID]
		if !ok {
			continue
		}
		if r.Type == "supersedes" && r.State == "accepted" && result.Entries[target].Status == MemoryActive {
			result.Entries[target].Status = MemorySuperseded
			result.Entries[target].SupersededBy = sql.NullInt64{Int64: r.SourceID, Valid: true}
		}
		if (r.Type == "supersedes" && (r.State == "pending" || r.State == "retracted")) || r.Type == "contradicts" {
			result.Entries[target].Conflicted = true
			result.Entries[source].Conflicted = true
			result.Entries[target].ConflictRefs += "#" + strconv.FormatInt(r.SourceID, 10) + " "
			result.Entries[source].ConflictRefs += "#" + strconv.FormatInt(r.TargetID, 10) + " "
		}
	}
	return result, tx.Commit()
}

// ReopenMemory is an explicit user operation, preserving previous completion
// and relationship events. It never deletes them or rewrites legacy content.
func (s *Store) ReopenMemory(projectID, id, revision int64) error {
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
	var pid int64
	if err := tx.QueryRow(s.Rebind(`SELECT project_id FROM memories WHERE id=?`), id).Scan(&pid); err != nil || pid != projectID {
		return ErrMemoryNotFound
	}
	var current int64
	if err := tx.QueryRow(s.Rebind(`SELECT COALESCE(MAX(e.id),0) FROM memory_state_events e LEFT JOIN memory_relations rel ON rel.id=e.relation_id WHERE e.project_id=? AND (e.memory_id=? OR rel.target_id=? OR rel.source_id=?)`), pid, id, id, id).Scan(&current); err != nil {
		return err
	}
	if revision != current {
		return ErrCaptureConflict
	}
	if err := s.appendState(tx, pid, id, "", "reopened", "", revision, "local_user", Now()); err != nil {
		return err
	}
	return tx.Commit()
}
