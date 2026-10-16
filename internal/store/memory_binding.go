package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
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

// CanonicalDir is dir made absolute with symlinks resolved.
func CanonicalDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return filepath.Clean(abs)
}

// CheckoutIdentity names the checkout dir belongs to: the root of its git
// checkout, or dir itself outside git. A shell that changes into a subfolder
// stays in the same checkout.
func CheckoutIdentity(dir string) string {
	dir = CanonicalDir(dir)
	if root := attribution.RepoRoot(dir); root != "" {
		return CanonicalDir(root)
	}
	return dir
}

// sameCheckout compares a stored checkout with a requested one. Bindings
// written before 0.5.6 stored the shell's folder, so both sides are reduced to
// their checkout identity.
func sameCheckout(stored, requested string) bool {
	return CheckoutIdentity(stored) == CheckoutIdentity(requested)
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
	if parent == "" {
		found, ok, err := enclosingBinding(tx, s, assistant, native, checkout)
		if err != nil {
			return SessionBinding{}, err
		}
		if ok {
			b = found
			b.Assistant, b.NativeID, b.Provenance = assistant, native, "native"
			return b, s.reopenBoundSession(tx, b.SessionID, transcript)
		}
	}
	err = s.findSessionBinding(tx, &b)
	if err == nil {
		if b.ParentHandle != parent {
			return SessionBinding{}, ErrAttribution
		}
		return b, s.reopenBoundSession(tx, b.SessionID, transcript)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SessionBinding{}, err
	}
	var parentID any
	if parent != "" {
		var id int64
		var parentCheckout string
		if err := tx.QueryRow(s.Rebind(`SELECT session_id,checkout FROM memory_session_bindings WHERE handle=? AND project_id=?`), parent, projectID).Scan(&id, &parentCheckout); err != nil || !sameCheckout(parentCheckout, checkout) {
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

func (s *Store) reopenBoundSession(tx *sql.Tx, sessionID int64, transcript string) error {
	if _, err := s.ExecTx(tx, `UPDATE sessions SET sync_dirty=1,transcript_path=COALESCE(NULLIF(?,''),transcript_path),ended_at=CASE WHEN end_reason='interrupted' THEN NULL ELSE ended_at END,end_reason=CASE WHEN end_reason='interrupted' THEN NULL ELSE end_reason END WHERE id=?`, transcript, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// enclosingBinding finds the top-level binding of this native session whose
// checkout is checkout itself or one of its parents, so a hook fired from a
// nested clone keeps the handle issued where the session started. The
// outermost match wins. Checkouts outside every bound folder stay independent.
func enclosingBinding(tx *sql.Tx, s *Store, assistant, native, checkout string) (SessionBinding, bool, error) {
	rows, err := tx.Query(s.Rebind(`SELECT handle,project_id,session_id,checkout FROM memory_session_bindings WHERE assistant=? AND native_id=? AND parent_handle IS NULL`), assistant, native)
	if err != nil {
		return SessionBinding{}, false, err
	}
	defer rows.Close()
	var best SessionBinding
	found := false
	for rows.Next() {
		var b SessionBinding
		if err := rows.Scan(&b.Handle, &b.ProjectID, &b.SessionID, &b.Checkout); err != nil {
			return SessionBinding{}, false, err
		}
		if !withinDir(b.Checkout, checkout) {
			continue
		}
		if !found || len(b.Checkout) < len(best.Checkout) || (len(b.Checkout) == len(best.Checkout) && b.Handle < best.Handle) {
			best, found = b, true
		}
	}
	return best, found, rows.Err()
}

func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// findSessionBinding fills b from the binding of b's native session in b's
// checkout. A row with the exact checkout wins; otherwise the oldest row whose
// stored folder lies in the same checkout (written before 0.5.6) is reused, so
// an upgrade does not split a running session again.
func (s *Store) findSessionBinding(tx *sql.Tx, b *SessionBinding) error {
	rows, err := tx.Query(s.Rebind(`SELECT handle,session_id,COALESCE(parent_handle,''),checkout FROM memory_session_bindings WHERE project_id=? AND assistant=? AND native_id=? ORDER BY CASE WHEN checkout=? THEN 0 ELSE 1 END,created_at,handle`), b.ProjectID, b.Assistant, b.NativeID, b.Checkout)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var handle, parent, checkout string
		var sessionID int64
		if err := rows.Scan(&handle, &sessionID, &parent, &checkout); err != nil {
			return err
		}
		if sameCheckout(checkout, b.Checkout) {
			b.Handle, b.SessionID, b.ParentHandle = handle, sessionID, parent
			return rows.Close()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return sql.ErrNoRows
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
	err := s.QueryRow(`SELECT handle,project_id,session_id,assistant,native_id,checkout,COALESCE(parent_handle,''),provenance FROM memory_session_bindings WHERE handle=? AND project_id=?`, handle, projectID).Scan(&b.Handle, &b.ProjectID, &b.SessionID, &b.Assistant, &b.NativeID, &b.Checkout, &b.ParentHandle, &b.Provenance)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionBinding{}, ErrAttribution
	}
	if err != nil {
		return SessionBinding{}, err
	}
	if !sameCheckout(b.Checkout, checkout) {
		return SessionBinding{}, ErrAttribution
	}
	return b, nil
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
