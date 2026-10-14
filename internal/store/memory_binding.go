package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
)

var ErrAttribution = errors.New("session attribution does not match project or checkout")
var ErrV2Required = errors.New("memory V2 schema is not active; use the explicit reviewed upgrade")

type SessionBinding struct {
	Handle       string `json:"handle"`
	ProjectID    int64  `json:"project_id"`
	SessionID    int64  `json:"session_id"`
	Assistant    string `json:"assistant"`
	NativeID     string `json:"native_id"`
	Checkout     string `json:"checkout"`
	ParentHandle string `json:"parent_handle,omitempty"`
	Provenance   string `json:"provenance"`
}

func CheckoutIdentity(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return filepath.Clean(abs)
}

// EnsureBoundSession serializes session creation across hook processes without
// guessing from recency. A reused native ID in another project is independent.
func (s *Store) EnsureBoundSession(projectID int64, assistant, native, checkout, transcript, parent string) (SessionBinding, error) {
	if !s.MemoryV2 {
		return SessionBinding{}, ErrV2Required
	}
	if assistant == "" || native == "" || len(native) > 512 || len(assistant) > 80 {
		return SessionBinding{}, ErrAttribution
	}
	checkout = CheckoutIdentity(checkout)
	tx, err := s.DB.Begin()
	if err != nil {
		return SessionBinding{}, err
	}
	defer tx.Rollback()
	if err := s.lockProject(tx, projectID); err != nil {
		return SessionBinding{}, err
	}
	b := SessionBinding{ProjectID: projectID, Assistant: assistant, NativeID: native, Checkout: checkout, ParentHandle: parent, Provenance: "native"}
	err = tx.QueryRow(s.Rebind(`SELECT handle,session_id,COALESCE(parent_handle,'') FROM memory_session_bindings WHERE project_id=? AND assistant=? AND native_id=? AND checkout=?`), projectID, assistant, native, checkout).Scan(&b.Handle, &b.SessionID, &b.ParentHandle)
	if err == nil {
		if b.ParentHandle != parent {
			return SessionBinding{}, ErrAttribution
		}
		if _, err := s.ExecTx(tx, `UPDATE sessions SET sync_dirty=1,transcript_path=COALESCE(NULLIF(?,''),transcript_path),ended_at=CASE WHEN end_reason='interrupted' THEN NULL ELSE ended_at END,end_reason=CASE WHEN end_reason='interrupted' THEN NULL ELSE end_reason END WHERE id=?`, transcript, b.SessionID); err != nil {
			return SessionBinding{}, err
		}
		return b, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SessionBinding{}, err
	}
	var parentID any
	if parent != "" {
		var id int64
		if err := tx.QueryRow(s.Rebind(`SELECT session_id FROM memory_session_bindings WHERE handle=? AND project_id=? AND checkout=?`), parent, projectID, checkout).Scan(&id); err != nil {
			return SessionBinding{}, ErrAttribution
		}
		parentID = id
	}
	b.Handle = uuid.NewString()
	at := Now()
	err = tx.QueryRow(s.Rebind(`INSERT INTO sessions(external_id,project_id,assistant,started_at,transcript_path,sync_uid,parent_session_id) VALUES(?,?,?,?,NULLIF(?,''),?,?) RETURNING id`), "v2:"+b.Handle, projectID, assistant, at, transcript, newSyncUID(), parentID).Scan(&b.SessionID)
	if err != nil {
		return SessionBinding{}, err
	}
	if _, err := s.ExecTx(tx, `INSERT INTO memory_session_bindings(handle,project_id,session_id,assistant,native_id,checkout,parent_handle,provenance,created_at) VALUES(?,?,?,?,?,?,NULLIF(?,''),'native',?)`, b.Handle, projectID, b.SessionID, assistant, native, checkout, parent, at); err != nil {
		return SessionBinding{}, err
	}
	return b, tx.Commit()
}

func (s *Store) lockProject(tx *sql.Tx, projectID int64) error {
	if s.Backend == BackendPostgres {
		if _, err := tx.Exec(`SET LOCAL synchronous_commit = on`); err != nil {
			return err
		}
	}
	q := `SELECT id FROM projects WHERE id=?`
	if s.Backend == BackendPostgres {
		q += ` FOR UPDATE`
	}
	var id int64
	if err := tx.QueryRow(s.Rebind(q), projectID).Scan(&id); err != nil {
		return ErrAttribution
	}
	return nil
}

func (s *Store) ResolveBinding(handle string, projectID int64, checkout string) (SessionBinding, error) {
	if !s.MemoryV2 {
		return SessionBinding{}, ErrV2Required
	}
	var b SessionBinding
	err := s.QueryRow(`SELECT handle,project_id,session_id,assistant,native_id,checkout,COALESCE(parent_handle,''),provenance FROM memory_session_bindings WHERE handle=? AND project_id=? AND checkout=?`, handle, projectID, CheckoutIdentity(checkout)).Scan(&b.Handle, &b.ProjectID, &b.SessionID, &b.Assistant, &b.NativeID, &b.Checkout, &b.ParentHandle, &b.Provenance)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrAttribution
	}
	return b, err
}

func (s *Store) SetBoundSummary(b SessionBinding, summary string) error {
	if b.SessionID == 0 {
		return ErrAttribution
	}
	res, err := s.Exec(`UPDATE sessions SET summary=? WHERE id=? AND project_id=?`, summary, b.SessionID, b.ProjectID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: summary", ErrAttribution)
	}
	return nil
}
