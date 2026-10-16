package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ebrahim5801/agent-brain-cli/internal/redact"
	"regexp"
	"time"

	"github.com/google/uuid"
)

var ErrCaptureConflict = errors.New("capture manifest, payload, or lease conflicts with persisted state")

var captureKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,119}$`)

func validCaptureKey(key string) bool { return captureKey.MatchString(key) && redact.Apply(key) == key }

type CaptureBatch struct {
	Retries        int64         `json:"retries"`
	NextRetry      string        `json:"next_retry,omitempty"`
	ID             string        `json:"batch_id"`
	ProjectID      int64         `json:"project_id"`
	SourceHandle   string        `json:"source_handle"`
	ExecutorHandle string        `json:"executor_handle"`
	Trigger        string        `json:"trigger"`
	Watermark      int64         `json:"watermark"`
	State          string        `json:"state"`
	Generation     int64         `json:"generation"`
	LeaseUntil     string        `json:"lease_until"`
	ManifestReady  bool          `json:"manifest_ready"`
	Items          []CaptureItem `json:"items"`
}
type CaptureItem struct {
	Key         string `json:"item_key"`
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
	MemoryID    int64  `json:"memory_id,omitempty"`
}

func (s *Store) RequestCapture(b SessionBinding, trigger string, watermark int64, now time.Time) (CaptureBatch, error) {
	if !s.MemoryV2 {
		return CaptureBatch{}, ErrV2Required
	}
	if _, err := s.ResolveBinding(b.Handle, b.ProjectID, b.Checkout); err != nil {
		return CaptureBatch{}, err
	}
	if trigger != "stop" && trigger != "periodic" && trigger != "checkpoint" && trigger != "recovery" {
		return CaptureBatch{}, ErrCaptureConflict
	}
	id := uuid.NewString()
	at := now.UTC().Format(TimeLayout)
	_, err := s.Exec(`INSERT INTO memory_capture_batches(id,project_id,source_handle,trigger,watermark,state,created_at,updated_at) VALUES(?,?,?,?,?,'pending',?,?) ON CONFLICT(source_handle,trigger,watermark) DO NOTHING`, id, b.ProjectID, b.Handle, trigger, watermark, at, at)
	if err != nil {
		return CaptureBatch{}, err
	}
	err = s.QueryRow(`SELECT id FROM memory_capture_batches WHERE source_handle=? AND trigger=? AND watermark=?`, b.Handle, trigger, watermark).Scan(&id)
	if err != nil {
		return CaptureBatch{}, err
	}
	return s.CaptureStatus(id, b.ProjectID)
}

func (s *Store) CaptureStatus(id string, projectID int64) (CaptureBatch, error) {
	if !s.MemoryV2 {
		return CaptureBatch{}, ErrV2Required
	}
	var b CaptureBatch
	var ready intBool
	err := s.QueryRow(`SELECT id,project_id,source_handle,COALESCE(executor_handle,''),trigger,watermark,state,generation,lease_until,manifest_ready,retries,next_retry FROM memory_capture_batches WHERE id=? AND project_id=?`, id, projectID).Scan(&b.ID, &b.ProjectID, &b.SourceHandle, &b.ExecutorHandle, &b.Trigger, &b.Watermark, &b.State, &b.Generation, &b.LeaseUntil, &ready, &b.Retries, &b.NextRetry)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrAttribution
	}
	if err != nil {
		return b, err
	}
	b.ManifestReady = bool(ready)
	rows, err := s.Query(`SELECT item_key,operation_id,state,COALESCE(memory_id,0) FROM memory_write_receipts WHERE batch_id=? AND project_id=? ORDER BY item_key`, id, projectID)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var i CaptureItem
		if err := rows.Scan(&i.Key, &i.OperationID, &i.State, &i.MemoryID); err != nil {
			return b, err
		}
		b.Items = append(b.Items, i)
	}
	return b, rows.Err()
}

func (s *Store) BeginCapture(id string, b SessionBinding, keys []string, now time.Time) (CaptureBatch, error) {
	if _, err := s.ResolveBinding(b.Handle, b.ProjectID, b.Checkout); err != nil {
		return CaptureBatch{}, err
	}
	if len(keys) > 100 {
		return CaptureBatch{}, ErrCaptureConflict
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if !validCaptureKey(k) || seen[k] {
			return CaptureBatch{}, ErrCaptureConflict
		}
		seen[k] = true
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return CaptureBatch{}, err
	}
	defer tx.Rollback()
	if err := s.lockProject(tx, b.ProjectID); err != nil {
		return CaptureBatch{}, err
	}
	var state, executor, lease, nextRetry string
	var gen int64
	var ready intBool
	err = tx.QueryRow(s.Rebind(`SELECT state,COALESCE(executor_handle,''),lease_until,generation,manifest_ready,next_retry FROM memory_capture_batches WHERE id=? AND project_id=?`), id, b.ProjectID).Scan(&state, &executor, &lease, &gen, &ready, &nextRetry)
	if err != nil {
		return CaptureBatch{}, ErrAttribution
	}
	at := now.UTC().Format(TimeLayout)
	if bool(ready) {
		rows, e := tx.Query(s.Rebind(`SELECT item_key FROM memory_write_receipts WHERE batch_id=?`), id)
		if e != nil {
			return CaptureBatch{}, e
		}
		count := 0
		match := true
		for rows.Next() {
			var k string
			if e := rows.Scan(&k); e != nil {
				rows.Close()
				return CaptureBatch{}, e
			}
			count++
			match = match && seen[k]
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return CaptureBatch{}, e
		}
		if !match || count != len(keys) {
			return CaptureBatch{}, ErrCaptureConflict
		}
	} else {
		for _, k := range keys {
			if _, err := s.ExecTx(tx, `INSERT INTO memory_write_receipts(project_id,operation_id,batch_id,item_key,state,created_at) VALUES(?,?,?,?,'pending',?)`, b.ProjectID, uuid.NewString(), id, k, at); err != nil {
				return CaptureBatch{}, err
			}
		}
	}
	if state == "persisted" || state == "no_durable_changes" {
		if err := tx.Commit(); err != nil {
			return CaptureBatch{}, err
		}
		return s.CaptureStatus(id, b.ProjectID)
	}
	if lease > at && executor != b.Handle {
		return CaptureBatch{}, ErrCaptureConflict
	}
	if state == "retry_wait" && nextRetry > at {
		return CaptureBatch{}, ErrCaptureConflict
	}
	if executor != b.Handle || lease <= at {
		gen++
	}
	if _, err := s.ExecTx(tx, `UPDATE memory_capture_batches SET state='in_progress',executor_handle=?,generation=?,lease_until=?,manifest_ready=1,updated_at=? WHERE id=? AND project_id=?`, b.Handle, gen, now.Add(2*time.Minute).UTC().Format(TimeLayout), at, id, b.ProjectID); err != nil {
		return CaptureBatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return CaptureBatch{}, err
	}
	return s.CaptureStatus(id, b.ProjectID)
}

func (s *Store) CompleteCapture(id string, b SessionBinding, generation int64, noChanges bool, now time.Time) (CaptureBatch, error) {
	if _, err := s.ResolveBinding(b.Handle, b.ProjectID, b.Checkout); err != nil {
		return CaptureBatch{}, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return CaptureBatch{}, err
	}
	defer tx.Rollback()
	if err := s.lockProject(tx, b.ProjectID); err != nil {
		return CaptureBatch{}, err
	}
	var state, executor, lease string
	var gen int64
	var ready intBool
	err = tx.QueryRow(s.Rebind(`SELECT state,COALESCE(executor_handle,''),generation,lease_until,manifest_ready FROM memory_capture_batches WHERE id=? AND project_id=?`), id, b.ProjectID).Scan(&state, &executor, &gen, &lease, &ready)
	if err != nil {
		return CaptureBatch{}, ErrAttribution
	}
	if gen != generation || executor != b.Handle {
		return CaptureBatch{}, ErrCaptureConflict
	}
	if state == "persisted" || state == "no_durable_changes" {
		if (state == "no_durable_changes") != noChanges {
			return CaptureBatch{}, ErrCaptureConflict
		}
		if err := tx.Commit(); err != nil {
			return CaptureBatch{}, err
		}
		return s.CaptureStatus(id, b.ProjectID)
	}
	if !bool(ready) || lease <= now.UTC().Format(TimeLayout) {
		return CaptureBatch{}, ErrCaptureConflict
	}
	var total, pending int
	if err := tx.QueryRow(s.Rebind(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN state='pending' THEN 1 ELSE 0 END),0) FROM memory_write_receipts WHERE batch_id=? AND project_id=?`), id, b.ProjectID).Scan(&total, &pending); err != nil {
		return CaptureBatch{}, err
	}
	if pending > 0 || (total == 0) != noChanges {
		return CaptureBatch{}, ErrCaptureConflict
	}
	state = "persisted"
	if noChanges {
		state = "no_durable_changes"
	}
	if _, err := s.ExecTx(tx, `UPDATE memory_capture_batches SET state=?,lease_until='',updated_at=? WHERE id=? AND project_id=?`, state, now.UTC().Format(TimeLayout), id, b.ProjectID); err != nil {
		return CaptureBatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return CaptureBatch{}, err
	}
	return s.CaptureStatus(id, b.ProjectID)
}

func (s *Store) PendingCaptures(projectID int64) ([]CaptureBatch, error) {
	if !s.MemoryV2 {
		return nil, nil
	}
	rows, err := s.Query(`SELECT id FROM memory_capture_batches WHERE project_id=? AND state NOT IN ('persisted','no_durable_changes') ORDER BY created_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []CaptureBatch
	for _, id := range ids {
		b, err := s.CaptureStatus(id, projectID)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

type MemoryDetails struct {
	Rationale     string `json:"rationale,omitempty"`
	Subject       string `json:"subject_key,omitempty"`
	Scope         string `json:"scope,omitempty"`
	SourceHandle  string `json:"source_handle,omitempty"`
	Completeness  string `json:"completeness"`
	ContentDigest string `json:"content_digest"`
}
type V2Write struct {
	Memory             NewMemory
	Details            MemoryDetails
	OperationID        string
	BatchID            string
	Generation         int64
	PayloadDigest      string
	TeamUIDs           []string
	TeamAccepted       []string
	TeamRejected       map[string]string
	Relations          []NewRelation
	Evidence           []MemoryEvidence
	Supersedes         []int64
	Completes          []int64
	ExpectedRevision   int64
	DisableTransitions bool
	ExecutorHandle     string
}
type V2WriteResult struct {
	SupersededTeam []string          `json:"superseded_team,omitempty"`
	RejectedTeam   map[string]string `json:"rejected_team,omitempty"`
	ID             int64             `json:"memory_id"`
	OperationID    string            `json:"operation_id"`
	Outcome        string            `json:"outcome"`
	Accepted       []int64           `json:"superseded,omitempty"`
	Completed      []int64           `json:"completed,omitempty"`
	Pending        []int64           `json:"pending,omitempty"`
	Rejected       map[int64]string  `json:"rejected,omitempty"`
}

// InsertMemoryV2 persists an item and its receipt together. PostgreSQL project
// locking and SQLite BEGIN IMMEDIATE serialize concurrent retries.
func (s *Store) InsertMemoryV2(w V2Write, now time.Time) (V2WriteResult, error) {
	if !s.MemoryV2 {
		return V2WriteResult{}, ErrV2Required
	}
	if !validCaptureKey(w.OperationID) {
		return V2WriteResult{}, ErrCaptureConflict
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return V2WriteResult{}, err
	}
	defer tx.Rollback()
	if err := s.lockProject(tx, w.Memory.ProjectID); err != nil {
		return V2WriteResult{}, err
	}
	if w.Memory.SessionID != 0 {
		var pid int64
		if err := tx.QueryRow(s.Rebind(`SELECT project_id FROM sessions WHERE id=?`), w.Memory.SessionID).Scan(&pid); err != nil || pid != w.Memory.ProjectID {
			return V2WriteResult{}, ErrAttribution
		}
	}
	var state, digest, batch, result string
	err = tx.QueryRow(s.Rebind(`SELECT state,payload_digest,COALESCE(batch_id,''),result_json FROM memory_write_receipts WHERE project_id=? AND operation_id=?`), w.Memory.ProjectID, w.OperationID).Scan(&state, &digest, &batch, &result)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return V2WriteResult{}, err
	}
	if err == nil && state == "persisted" {
		if digest != w.PayloadDigest || batch != w.BatchID {
			return V2WriteResult{}, ErrCaptureConflict
		}
		var r V2WriteResult
		if err := json.Unmarshal([]byte(result), &r); err != nil {
			return r, err
		}
		return r, tx.Commit()
	}
	// Independent checkpoint manifests may rediscover an identical observation.
	// The canonical digest includes source, scope, rationale and evidence, so
	// a genuinely different observation is preserved rather than text-deduped.
	var existingResult string
	canRediscover := w.Details.SourceHandle != "" && len(w.Evidence) > 0
	for _, e := range w.Evidence {
		if e.Status == "unverified" {
			canRediscover = false
		}
	}
	duplicateErr := sql.ErrNoRows
	if canRediscover {
		duplicateErr = tx.QueryRow(s.Rebind(`SELECT result_json FROM memory_write_receipts WHERE project_id=? AND payload_digest=? AND state='persisted' ORDER BY created_at,operation_id LIMIT 1`), w.Memory.ProjectID, w.PayloadDigest).Scan(&existingResult)
	}
	if duplicateErr != nil && !errors.Is(duplicateErr, sql.ErrNoRows) {
		return V2WriteResult{}, duplicateErr
	}
	if w.BatchID != "" {
		if err != nil || batch != w.BatchID {
			return V2WriteResult{}, ErrCaptureConflict
		}
		var executor, bstate, lease string
		var gen int64
		if err := tx.QueryRow(s.Rebind(`SELECT COALESCE(executor_handle,''),state,generation,lease_until FROM memory_capture_batches WHERE id=? AND project_id=?`), w.BatchID, w.Memory.ProjectID).Scan(&executor, &bstate, &gen, &lease); err != nil {
			return V2WriteResult{}, err
		}
		if executor != w.ExecutorHandle || bstate != "in_progress" || gen != w.Generation || lease <= now.UTC().Format(TimeLayout) {
			return V2WriteResult{}, ErrCaptureConflict
		}
	}
	if w.Details.SourceHandle != "" {
		var sid int64
		if err := tx.QueryRow(s.Rebind(`SELECT session_id FROM memory_session_bindings WHERE handle=? AND project_id=?`), w.Details.SourceHandle, w.Memory.ProjectID).Scan(&sid); err != nil || sid != w.Memory.SessionID {
			return V2WriteResult{}, ErrAttribution
		}
	}
	at := now.UTC().Format(TimeLayout)
	if duplicateErr == nil {
		var r V2WriteResult
		if err := json.Unmarshal([]byte(existingResult), &r); err != nil {
			return r, err
		}
		r.OperationID = w.OperationID
		raw, err := json.Marshal(r)
		if err != nil {
			return r, err
		}
		if _, err := s.ExecTx(tx, `INSERT INTO memory_write_receipts(project_id,operation_id,batch_id,payload_digest,state,memory_id,result_json,created_at) VALUES(?,?,NULLIF(?,''),?,'persisted',?,?,?) ON CONFLICT(project_id,operation_id) DO UPDATE SET payload_digest=excluded.payload_digest,state='persisted',memory_id=excluded.memory_id,result_json=excluded.result_json`, w.Memory.ProjectID, w.OperationID, w.BatchID, w.PayloadDigest, r.ID, string(raw), at); err != nil {
			return r, err
		}
		return r, tx.Commit()
	}
	id, err := insertMemory(func(q string, args ...any) *sql.Row { return tx.QueryRow(s.Rebind(q), normalizeArgs(args)...) }, w.Memory, at)
	if err != nil {
		return V2WriteResult{}, err
	}
	if _, err := s.ExecTx(tx, `INSERT INTO memory_details(memory_id,project_id,format_version,rationale,subject_key,scope,source_handle,content_digest,completeness) VALUES(?,?,2,?,?,?,NULLIF(?,''),?,?)`, id, w.Memory.ProjectID, w.Details.Rationale, w.Details.Subject, w.Details.Scope, w.Details.SourceHandle, w.Details.ContentDigest, w.Details.Completeness); err != nil {
		return V2WriteResult{}, err
	}
	for _, uid := range w.TeamUIDs {
		if _, err := s.ExecTx(tx, `INSERT INTO memory_team_supersedes(memory_id,team_uid) VALUES(?,?) ON CONFLICT(memory_id,team_uid) DO NOTHING`, id, uid); err != nil {
			return V2WriteResult{}, err
		}
	}
	r := V2WriteResult{ID: id, OperationID: w.OperationID, Outcome: "persisted", SupersededTeam: w.TeamAccepted, RejectedTeam: w.TeamRejected}
	if err := s.writeLifecycle(tx, id, w, at, &r); err != nil {
		return V2WriteResult{}, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	if _, err := s.ExecTx(tx, `INSERT INTO memory_write_receipts(project_id,operation_id,batch_id,payload_digest,state,memory_id,result_json,created_at) VALUES(?,?,NULLIF(?,''),?,'persisted',?,?,?) ON CONFLICT(project_id,operation_id) DO UPDATE SET payload_digest=excluded.payload_digest,state='persisted',memory_id=excluded.memory_id,result_json=excluded.result_json`, w.Memory.ProjectID, w.OperationID, w.BatchID, w.PayloadDigest, id, string(raw), at); err != nil {
		return r, err
	}
	if err := tx.Commit(); err != nil {
		return V2WriteResult{}, fmt.Errorf("persistence outcome uncertain; retry operation %s: %w", w.OperationID, err)
	}
	return r, nil
}

// DeferCapture releases a worker lease without losing receipts or pretending
// completion. Only fixed failure categories cross this boundary; no transcript
// or error body is persisted. Explicit recovery may retry needs_context later.
func (s *Store) DeferCapture(id string, b SessionBinding, generation int64, reason string, now time.Time) (CaptureBatch, error) {
	if reason != "context_unavailable" && reason != "persistence_retry" {
		return CaptureBatch{}, ErrCaptureConflict
	}
	if _, err := s.ResolveBinding(b.Handle, b.ProjectID, b.Checkout); err != nil {
		return CaptureBatch{}, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return CaptureBatch{}, err
	}
	defer tx.Rollback()
	if err := s.lockProject(tx, b.ProjectID); err != nil {
		return CaptureBatch{}, err
	}
	var state, executor string
	var gen, retries int64
	if err := tx.QueryRow(s.Rebind(`SELECT state,COALESCE(executor_handle,''),generation,retries FROM memory_capture_batches WHERE id=? AND project_id=?`), id, b.ProjectID).Scan(&state, &executor, &gen, &retries); err != nil {
		return CaptureBatch{}, ErrAttribution
	}
	// A batch nobody has begun can be deferred directly, so an agent can always
	// acknowledge a capture request it cannot carry out.
	unbegun := state == "pending" && executor == ""
	if !(unbegun || state == "in_progress" && executor == b.Handle) || gen != generation {
		return CaptureBatch{}, ErrCaptureConflict
	}
	next := ""
	state = "needs_context"
	if reason == "persistence_retry" {
		state = "retry_wait"
		delay := 30 * time.Second
		for n := int64(0); n < retries && delay < 5*time.Minute; n++ {
			delay *= 2
		}
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
		next = now.Add(delay).UTC().Format(TimeLayout)
	}
	if _, err := s.ExecTx(tx, `UPDATE memory_capture_batches SET state=?,executor_handle=?,lease_until='',retries=retries+1,next_retry=?,updated_at=? WHERE id=? AND project_id=?`, state, b.Handle, next, now.UTC().Format(TimeLayout), id, b.ProjectID); err != nil {
		return CaptureBatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return CaptureBatch{}, err
	}
	return s.CaptureStatus(id, b.ProjectID)
}
