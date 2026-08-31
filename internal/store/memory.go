package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/ebrahim5801/agent-brain-cli/wire"
)

// Memory lifecycle: active | superseded | deleted. Every transition is soft and
// reversible — a deleted or superseded entry stops being served immediately
// (spec 003 FR-016) but is retained so `memory restore` can bring it back; no
// developer action loses the underlying content.
const (
	MemoryActive     = "active"
	MemorySuperseded = "superseded"
	MemoryDeleted    = "deleted"
)

type Memory struct {
	ID           int64
	ProjectID    int64
	SessionID    sql.NullInt64
	Content      string
	Kind         string
	Origin       string
	Priority     string
	Status       string
	SupersededBy sql.NullInt64
	Branch       sql.NullString
	CommitHash   sql.NullString
	CapturedAt   string
	UpdatedAt    string
	Edited       bool
}

var ErrMemoryNotFound = errors.New("memory entry not found")

// The priority vocabulary is defined once, in wire, because both sides of the
// team-sync wire validate against it. These aliases exist so store callers need
// not spell the literals; a rename in wire must not leave a second copy here.
const (
	MemoryPriorityCritical   = wire.MemoryPriorityCritical
	MemoryPriorityNormal     = wire.MemoryPriorityNormal
	MemoryPriorityBackground = wire.MemoryPriorityBackground
)

const memoryColumns = `id, project_id, session_id, content, kind, origin, priority, status, superseded_by, branch, commit_hash, captured_at, updated_at, edited`

func scanMemory(row interface{ Scan(...any) error }) (Memory, error) {
	var m Memory
	var edited intBool
	err := row.Scan(&m.ID, &m.ProjectID, &m.SessionID, &m.Content, &m.Kind, &m.Origin,
		&m.Priority, &m.Status, &m.SupersededBy, &m.Branch, &m.CommitHash, &m.CapturedAt, &m.UpdatedAt, &edited)
	m.Edited = bool(edited)
	return m, err
}

type NewMemory struct {
	ProjectID int64
	SessionID int64 // 0 when the save happens outside a tracked session
	Content   string
	Kind      string
	Origin    string
	Priority  string
	Branch    string // "" = detached or no repo
	Commit    string // "" = no repo
	HasRepo   bool
	// PersonalOnly marks an entry as personal context, never eligible for
	// contribution to a team pool (FR-005).
	PersonalOnly bool
	// ShareLive means the project's memory-sharing consent is granted and not
	// paused; a team_uid is minted so the entry becomes contribution-eligible.
	// Pausing means no team_uid, so a later resume never retroactively shares.
	ShareLive bool
}

func (s *Store) InsertMemory(m NewMemory, at string) (int64, error) {
	var sessionID any
	if m.SessionID != 0 {
		sessionID = m.SessionID
	}
	var branch, commit any
	if m.HasRepo {
		branch, commit = m.Branch, m.Commit
	}
	var teamUID any
	if m.ShareLive && !m.PersonalOnly {
		teamUID = uuid.NewString()
	}
	priority := m.Priority
	if priority == "" {
		priority = MemoryPriorityNormal
	}
	// RETURNING id unifies both backends (modernc.org/sqlite supports it), so
	// this path has no LastInsertId() divergence to special-case per dialect.
	var id int64
	err := s.QueryRow(`
        INSERT INTO memories (project_id, session_id, content, kind, origin, priority, branch, commit_hash, captured_at, updated_at, personal_only, team_uid)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        RETURNING id`,
		m.ProjectID, sessionID, m.Content, m.Kind, m.Origin, priority, branch, commit, at, at, m.PersonalOnly, teamUID).Scan(&id)
	return id, err
}

// PendingMemory is a distilled entry queued for contribution to the team pool.
type PendingMemory struct {
	UID        string
	Content    string
	Kind       string
	Origin     string
	Priority   string
	CapturedAt string
	Branch     sql.NullString
	CommitHash sql.NullString
	// Supersedes holds the team_uids of every already-shared entry this one
	// replaced (so the server can retire each predecessor). team_uids are uuids,
	// so a comma join is unambiguous.
	Supersedes []string
}

// PendingContributions returns the project's share queue: entries with a minted
// team_uid not yet acked, never personal-only. Supersedes lists the team_uids
// this entry retires, from two sources unioned: the author's own local
// supersede chain (children whose superseded_by points here and were themselves
// shared) and explicit team handles the author passed to memory_save
// (memory_team_supersedes — the cross-author path). The server applies the
// same-author/cross-author rule to each uid.
func (s *Store) PendingContributions(projectID int64) ([]PendingMemory, error) {
	// group_concat (SQLite) and string_agg (Postgres) both comma-join the
	// superseded team_uids into one scalar; uuids never contain commas.
	agg := "group_concat(u)"
	if s.Backend == BackendPostgres {
		agg = "string_agg(u, ',')"
	}
	rows, err := s.Query(`
        SELECT m.team_uid, m.content, m.kind, m.origin, m.priority, m.captured_at, m.branch, m.commit_hash,
               (SELECT `+agg+` FROM (
                    SELECT old.team_uid AS u FROM memories old
                      WHERE old.superseded_by = m.id AND old.team_uid IS NOT NULL
                    UNION
                    SELECT mts.team_uid AS u FROM memory_team_supersedes mts
                      WHERE mts.memory_id = m.id
               ) AS supersede_uids)
        FROM memories m
        WHERE m.project_id = ? AND m.personal_only = 0 AND m.status = 'active'
          AND m.team_uid IS NOT NULL AND m.shared_at IS NULL
        ORDER BY m.captured_at, m.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingMemory
	for rows.Next() {
		var p PendingMemory
		var supersedes sql.NullString
		if err := rows.Scan(&p.UID, &p.Content, &p.Kind, &p.Origin, &p.Priority, &p.CapturedAt, &p.Branch, &p.CommitHash, &supersedes); err != nil {
			return nil, err
		}
		if supersedes.Valid && supersedes.String != "" {
			p.Supersedes = strings.Split(supersedes.String, ",")
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkShared records the server ack for a contributed entry (idempotent: called
// on both 'stored' and 'duplicate'). It clears any share_error from an earlier
// withheld cycle: the entry made it out after all.
func (s *Store) MarkShared(teamUID, at string) error {
	_, err := s.Exec(`UPDATE memories SET shared_at = ?, share_error = NULL WHERE team_uid = ? AND shared_at IS NULL`, at, teamUID)
	return err
}

// SetShareError records why an egress-time check withheld a queued entry from
// contribution this cycle. The entry stays queued and is re-evaluated every
// cycle, so shortening it (or marking it personal_only) resolves the error.
func (s *Store) SetShareError(teamUID, reason string) error {
	_, err := s.Exec(`UPDATE memories SET share_error = ? WHERE team_uid = ?`, reason, teamUID)
	return err
}

// MarkShareRejected records a server-side terminal rejection: the entry leaves
// the pending queue (shared_at set, so it never re-egresses) but keeps a
// visible reason, so `agent-brain status` reports it as permanently rejected —
// distinct from a recoverable capacity hold, which stays queued (FR-014, US4).
func (s *Store) MarkShareRejected(teamUID, reason, at string) error {
	_, err := s.Exec(`UPDATE memories SET shared_at = ?, share_error = ? WHERE team_uid = ? AND shared_at IS NULL`,
		at, reason, teamUID)
	return err
}

// ShareErrorEntry surfaces a withheld entry in `agent-brain status`.
type ShareErrorEntry struct {
	ID     int64
	Reason string
}

// ShareErrors lists the project's queued entries withheld from contribution,
// with the recorded reason.
func (s *Store) ShareErrors(projectID int64) ([]ShareErrorEntry, error) {
	rows, err := s.Query(`SELECT id, share_error FROM memories
        WHERE project_id = ? AND share_error IS NOT NULL ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShareErrorEntry
	for rows.Next() {
		var e ShareErrorEntry
		if err := rows.Scan(&e.ID, &e.Reason); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UnsharedMemories lists a project's active, non-personal-only entries that have
// never been queued for sharing — the selective-seeding candidates offered on
// first grant (FR-016, R13).
func (s *Store) UnsharedMemories(projectID int64) ([]Memory, error) {
	rows, err := s.Query(`SELECT `+memoryColumns+`
        FROM memories
        WHERE project_id = ? AND status = 'active' AND personal_only = 0 AND team_uid IS NULL
        ORDER BY captured_at DESC, id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MintShareUID makes an existing entry contribution-eligible by minting a
// team_uid, only if it has none and is not personal-only (selective seeding).
func (s *Store) MintShareUID(id int64) error {
	res, err := s.Exec(`UPDATE memories SET team_uid = ? WHERE id = ? AND team_uid IS NULL AND personal_only = 0`,
		uuid.NewString(), id)
	return oneRow(res, err)
}

func (s *Store) GetMemory(id int64) (Memory, error) {
	m, err := scanMemory(s.QueryRow(`SELECT `+memoryColumns+` FROM memories WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, ErrMemoryNotFound
	}
	return m, err
}

// ListMemories returns a project's entries newest-first, active first when
// superseded entries are included.
func (s *Store) ListMemories(projectID int64, includeSuperseded bool) ([]Memory, error) {
	q := `SELECT ` + memoryColumns + ` FROM memories WHERE project_id = ? AND status = 'active' ORDER BY captured_at DESC, id DESC`
	if includeSuperseded {
		q = `SELECT ` + memoryColumns + ` FROM memories WHERE project_id = ?
             ORDER BY CASE status WHEN 'active' THEN 0 ELSE 1 END, captured_at DESC, id DESC`
	}
	rows, err := s.Query(q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) UpdateMemoryContent(id int64, content, at string) error {
	res, err := s.Exec(`UPDATE memories SET content = ?, edited = 1, updated_at = ? WHERE id = ?`, content, at, id)
	return oneRow(res, err)
}

// MemoryPriorityCounts returns the project's active entries per priority. It
// backs the calibration check: self-assigned priority is only useful while
// critical stays rare, and the share is the only way to notice that it has not.
func (s *Store) MemoryPriorityCounts(projectID int64) (map[string]int, error) {
	rows, err := s.Query(`
        SELECT priority, COUNT(*) FROM memories
        WHERE project_id = ? AND status = 'active'
        GROUP BY priority`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var priority string
		var n int
		if err := rows.Scan(&priority, &n); err != nil {
			return nil, err
		}
		out[priority] += n
	}
	return out, rows.Err()
}

// MemoryShared reports whether an entry has already been contributed to the
// team pool. A reclassification after that point is local only: the pool copy
// keeps the priority it was contributed with.
func (s *Store) MemoryShared(id int64) (bool, error) {
	var sharedAt sql.NullString
	err := s.QueryRow(`SELECT shared_at FROM memories WHERE id = ?`, id).Scan(&sharedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrMemoryNotFound
	}
	if err != nil {
		return false, err
	}
	return sharedAt.Valid, nil
}

// UpdateMemoryPriority reclassifies an entry. It does not set edited: that flag
// marks content the developer rewrote, and priority is classification, not
// content.
func (s *Store) UpdateMemoryPriority(id int64, priority, at string) error {
	res, err := s.Exec(`UPDATE memories SET priority = ?, updated_at = ? WHERE id = ?`, priority, at, id)
	return oneRow(res, err)
}

// SupersedeMemory marks an active entry replaced by a newer one. Only active
// entries of the same project supersede, so a stale or cross-project ID from
// the assistant is rejected rather than silently corrupting another entry.
func (s *Store) SupersedeMemory(id, byID, projectID int64, at string) error {
	res, err := s.Exec(`
        UPDATE memories SET status = ?, superseded_by = ?, updated_at = ?
        WHERE id = ? AND project_id = ? AND status = ?`,
		MemorySuperseded, byID, at, id, projectID, MemoryActive)
	return oneRow(res, err)
}

// RestoreMemory re-activates an entry that was superseded or deleted, undoing
// either soft transition.
func (s *Store) RestoreMemory(id int64, at string) error {
	res, err := s.Exec(`UPDATE memories SET status = ?, superseded_by = NULL, updated_at = ? WHERE id = ? AND status IN (?, ?)`,
		MemoryActive, at, id, MemorySuperseded, MemoryDeleted)
	return oneRow(res, err)
}

// DeleteMemory soft-deletes an entry: it stops being served or contributed
// immediately (FR-016) but the row is retained so `memory restore` can recover
// it. The content is never physically dropped by a developer delete.
func (s *Store) DeleteMemory(id int64, at string) error {
	res, err := s.Exec(`UPDATE memories SET status = ?, updated_at = ? WHERE id = ? AND status != ?`,
		MemoryDeleted, at, id, MemoryDeleted)
	return oneRow(res, err)
}

// WipeProjectMemories soft-deletes every active or superseded entry of a
// project, returning how many were affected. The rows are retained (restorable
// per entry) rather than physically removed.
func (s *Store) WipeProjectMemories(projectID int64, at string) (int64, error) {
	res, err := s.Exec(`UPDATE memories SET status = ?, updated_at = ? WHERE project_id = ? AND status != ?`,
		MemoryDeleted, at, projectID, MemoryDeleted)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) SetProjectMemoryDisabled(projectID int64, disabled bool) error {
	res, err := s.Exec(`UPDATE projects SET memory_disabled = ? WHERE id = ?`, disabled, projectID)
	return oneRow(res, err)
}

func (s *Store) ProjectMemoryDisabled(projectID int64) (bool, error) {
	var disabled intBool
	err := s.QueryRow(`SELECT memory_disabled FROM projects WHERE id = ?`, projectID).Scan(&disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return bool(disabled), err
}

// MarkSessionDistilled claims a distillation pass for a session. The gate
// re-arms when a prompt event arrived after the last distillation (the
// distillation continuation emits tool_use but never prompt, so the Stop right
// after a distillation still passes through). It reports false when there is
// nothing new to distill, so concurrent Stop hooks race safely: exactly one
// caller wins per arming.
func (s *Store) MarkSessionDistilled(sessionID int64, at string) (bool, error) {
	res, err := s.Exec(`
        UPDATE sessions SET memory_distilled_at = ?
        WHERE id = ?
          AND (memory_distilled_at IS NULL
               OR EXISTS (SELECT 1 FROM events e
                          WHERE e.session_id = sessions.id
                            AND e.kind = 'prompt'
                            AND e.occurred_at > sessions.memory_distilled_at))`, at, sessionID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// MarkSessionCheckpointed claims a mid-session checkpoint for a session when at
// least threshold tool_use events have occurred since the last checkpoint (or
// ever, if none yet). Like MarkSessionDistilled it is an atomic conditional
// UPDATE, so concurrent tool-use hooks race safely: exactly one caller wins per
// arming. threshold 1 (used for pre-compact) fires whenever any tool_use has
// happened since the last checkpoint.
func (s *Store) MarkSessionCheckpointed(sessionID int64, at string, threshold int) (bool, error) {
	res, err := s.Exec(`
        UPDATE sessions SET memory_checkpointed_at = ?
        WHERE id = ?
          AND (SELECT COUNT(*) FROM events e
               WHERE e.session_id = sessions.id
                 AND e.kind = 'tool_use'
                 AND (sessions.memory_checkpointed_at IS NULL
                      OR e.occurred_at > sessions.memory_checkpointed_at)) >= ?`,
		at, sessionID, threshold)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// MarkSessionPromptInjected claims a per-turn prompt injection for adapters
// that cannot inject before the prompt and fall back to their tool-use hook
// (Cursor/Copilot). Like MarkSessionDistilled it re-arms when a prompt event
// arrived after the last claim, so it succeeds on exactly the first tool-use of
// each turn — one memory refresh per prompt, never once per tool call. Atomic
// conditional UPDATE: concurrent tool-use hooks race safely, exactly one wins.
func (s *Store) MarkSessionPromptInjected(sessionID int64, at string) (bool, error) {
	res, err := s.Exec(`
        UPDATE sessions SET memory_prompt_injected_at = ?
        WHERE id = ?
          AND (memory_prompt_injected_at IS NULL
               OR EXISTS (SELECT 1 FROM events e
                          WHERE e.session_id = sessions.id
                            AND e.kind = 'prompt'
                            AND e.occurred_at > sessions.memory_prompt_injected_at))`, at, sessionID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SessionActivity reports whether a session crossed the minimal-activity
// threshold for distillation (≥ 1 prompt and ≥ 1 tool_use event).
func (s *Store) SessionActivity(sessionID int64) (prompts, toolUses int64, err error) {
	err = s.QueryRow(`
        SELECT
            COUNT(CASE WHEN kind = 'prompt' THEN 1 END),
            COUNT(CASE WHEN kind = 'tool_use' THEN 1 END)
        FROM events WHERE session_id = ?`, sessionID).Scan(&prompts, &toolUses)
	return
}

func oneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrMemoryNotFound
	}
	if n > 1 {
		return fmt.Errorf("expected 1 row, changed %d", n)
	}
	return nil
}
