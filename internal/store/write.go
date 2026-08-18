package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type ProjectIdentity struct {
	Kind        string
	Identity    string
	DisplayName string
}

type Usage struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// newSyncUID is time-ordered (UUIDv7) so cloud-side storage clusters by
// arrival; uniqueness is what the sync pipeline's idempotency hangs on.
func newSyncUID() string {
	if u, err := uuid.NewV7(); err == nil {
		return u.String()
	}
	return uuid.NewString()
}

func (s *Store) UpsertProject(p ProjectIdentity, at string) (int64, error) {
	var id int64
	// Refresh identity_kind on conflict: a directory becomes a repo_root when it
	// is `git init`ed at the same path (identity unchanged, kind changes). Link,
	// sync, and team-memory all key by identity_kind||':'||identity, so a frozen
	// kind would silently orphan the row from its own link. The row id is stable,
	// so existing sessions stay attached.
	err := s.QueryRow(`
        INSERT INTO projects (identity_kind, identity, display_name, first_seen_at, last_seen_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(identity) DO UPDATE SET
            last_seen_at = excluded.last_seen_at,
            identity_kind = excluded.identity_kind,
            display_name = excluded.display_name
        RETURNING id`,
		p.Kind, p.Identity, p.DisplayName, at, at).Scan(&id)
	return id, err
}

// EnsureSession creates the session if unknown (events may arrive for sessions
// the collector never saw start, e.g. installed mid-session). A session closed
// as interrupted is reopened by new activity; a normal close stays closed.
// Reopening marks the session sync-dirty so an already-synced snapshot is
// re-sent with its corrected state.
func (s *Store) EnsureSession(externalID string, projectID int64, startedAt, transcriptPath, assistant string) (int64, error) {
	var id int64
	err := s.QueryRow(`
        INSERT INTO sessions (external_id, project_id, started_at, transcript_path, assistant, sync_uid)
        VALUES (?, ?, ?, NULLIF(?, ''), ?, ?)
        ON CONFLICT(external_id) DO UPDATE SET
            transcript_path = COALESCE(NULLIF(excluded.transcript_path, ''), sessions.transcript_path),
            ended_at   = CASE WHEN sessions.end_reason = 'interrupted' THEN NULL ELSE sessions.ended_at END,
            end_reason = CASE WHEN sessions.end_reason = 'interrupted' THEN NULL ELSE sessions.end_reason END,
            sync_dirty = CASE WHEN sessions.end_reason = 'interrupted' THEN 1 ELSE sessions.sync_dirty END
        RETURNING id`,
		externalID, projectID, startedAt, transcriptPath, assistant, newSyncUID()).Scan(&id)
	return id, err
}

// SetSessionSummary stores the agent-written summary on the newest session of
// a project — the one the Stop-hook distillation prompt is running in. The
// summary stays in the local store only; it is content and never syncs.
// Sub-sessions are skipped: a subagent can be the newest row but is never the
// session the distillation prompt runs in.
func (s *Store) SetSessionSummary(projectID int64, summary string) (bool, error) {
	res, err := s.Exec(`
        UPDATE sessions SET summary = ?
        WHERE id = (SELECT id FROM sessions WHERE project_id = ? AND parent_session_id IS NULL
                    ORDER BY started_at DESC, id DESC LIMIT 1)`,
		summary, projectID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// LatestSessionID returns the newest session of a project — the one an
// in-session MCP save is running in, by the same heuristic SetSessionSummary
// uses. Returns 0 when the project has no session yet (a save outside a
// tracked session).
func (s *Store) LatestSessionID(projectID int64) (int64, error) {
	var id int64
	err := s.QueryRow(`
        SELECT id FROM sessions WHERE project_id = ? AND parent_session_id IS NULL
        ORDER BY started_at DESC, id DESC LIMIT 1`,
		projectID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// SubSession is one subagent run captured under a parent session. The summary
// (task description + final report) and the prompt (the task text the parent
// assistant wrote when spawning the agent) are content and stay local, like
// the parent's distilled summary; everything else syncs.
type SubSession struct {
	ExternalID     string
	ProjectID      int64
	ParentID       int64
	Assistant      string
	AgentType      string
	Model          string
	StartedAt      string
	EndedAt        string
	TranscriptPath string
	Summary        string
	Prompt         string
	Usage          Usage
}

// UpsertSubSession records a subagent run idempotently. Re-scans of an
// unchanged agent transcript leave sync_dirty alone so an acked snapshot is
// not re-sent; a grown transcript (more usage, later end) re-dirties the row.
func (s *Store) UpsertSubSession(ss SubSession) (int64, error) {
	var id int64
	err := s.QueryRow(`
        INSERT INTO sessions (external_id, project_id, parent_session_id, assistant, agent_type,
                              started_at, ended_at, end_reason, model, transcript_path, summary, agent_prompt,
                              input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
                              sync_uid, sync_dirty)
        VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), 'normal', NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
                ?, ?, ?, ?, ?, 1)
        ON CONFLICT(external_id) DO UPDATE SET
            ended_at = COALESCE(NULLIF(excluded.ended_at, ''), sessions.ended_at),
            model = COALESCE(NULLIF(excluded.model, ''), sessions.model),
            transcript_path = COALESCE(NULLIF(excluded.transcript_path, ''), sessions.transcript_path),
            summary = COALESCE(NULLIF(excluded.summary, ''), sessions.summary),
            agent_prompt = COALESCE(NULLIF(excluded.agent_prompt, ''), sessions.agent_prompt),
            input_tokens = excluded.input_tokens,
            output_tokens = excluded.output_tokens,
            cache_read_tokens = excluded.cache_read_tokens,
            cache_write_tokens = excluded.cache_write_tokens,
            sync_dirty = CASE WHEN sessions.input_tokens <> excluded.input_tokens
                               OR sessions.output_tokens <> excluded.output_tokens
                               OR sessions.cache_read_tokens <> excluded.cache_read_tokens
                               OR sessions.cache_write_tokens <> excluded.cache_write_tokens
                               OR COALESCE(sessions.ended_at, '') <> COALESCE(NULLIF(excluded.ended_at, ''), sessions.ended_at, '')
                              THEN 1 ELSE sessions.sync_dirty END
        RETURNING id`,
		ss.ExternalID, ss.ProjectID, ss.ParentID, ss.Assistant, ss.AgentType,
		ss.StartedAt, ss.EndedAt, ss.Model, ss.TranscriptPath, ss.Summary, ss.Prompt,
		ss.Usage.Input, ss.Usage.Output, ss.Usage.CacheRead, ss.Usage.CacheWrite,
		newSyncUID()).Scan(&id)
	return id, err
}

// MarkProjectSessionsDirty queues every session of a local project for
// re-sync. Used after (re)linking: the platform may never have seen these
// sessions (fresh link, rebind, or a server that lost its data), and
// re-sending is idempotent — the server upserts by (account, sync_uid).
func (s *Store) MarkProjectSessionsDirty(identityKind, identity string) (int64, error) {
	res, err := s.Exec(`
        UPDATE sessions SET sync_dirty = 1
        WHERE project_id IN (SELECT id FROM projects WHERE identity_kind = ? AND identity = ?)`,
		identityKind, identity)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) AppendEvent(sessionID int64, kind, detail, at string) error {
	_, err := s.Exec(`INSERT INTO events (session_id, kind, detail, occurred_at, sync_uid) VALUES (?, ?, NULLIF(?, ''), ?, ?)`,
		sessionID, kind, detail, at, newSyncUID())
	return err
}

func (s *Store) CloseSession(sessionID int64, endedAt, reason string) error {
	_, err := s.Exec(`UPDATE sessions SET ended_at = ?, end_reason = ?, sync_dirty = 1 WHERE id = ?`,
		endedAt, reason, sessionID)
	return err
}

// AddUsage accumulates a per-model-call usage delta onto a session (Gemini's
// AfterModel payloads arrive one call at a time). It never resets counters —
// SetUsage owns absolute totals (transcript backfill); AddUsage owns
// incremental deltas. The model is set only when provided, preserving a known
// model over a later empty report.
func (s *Store) AddUsage(sessionID int64, model string, delta Usage) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.ExecTx(tx, `
        UPDATE sessions SET
            model = COALESCE(NULLIF(?, ''), model),
            input_tokens = input_tokens + ?,
            output_tokens = output_tokens + ?,
            cache_read_tokens = cache_read_tokens + ?,
            cache_write_tokens = cache_write_tokens + ?,
            sync_dirty = 1
        WHERE id = ?`,
		model, delta.Input, delta.Output, delta.CacheRead, delta.CacheWrite, sessionID); err != nil {
		return err
	}
	if err := s.addModelUsage(tx, sessionID, model, delta); err != nil {
		return err
	}
	return tx.Commit()
}

// SetModel records the model an assistant reported without touching token
// counters — used by assistants (e.g. Cursor) that name the model in their
// hook payloads but never report usage. An unchanged model is a no-op so a
// payload naming the model on every event doesn't re-dirty a synced session.
func (s *Store) SetModel(sessionID int64, model string) error {
	if model == "" {
		return nil
	}
	_, err := s.Exec(`
        UPDATE sessions SET model = ?, sync_dirty = 1
        WHERE id = ? AND (model IS NULL OR model <> ?)`,
		model, sessionID, model)
	return err
}

func (s *Store) SetUsage(sessionID int64, model string, u Usage) error {
	_, err := s.Exec(`
        UPDATE sessions SET
            model = COALESCE(NULLIF(?, ''), model),
            input_tokens = ?, output_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?,
            sync_dirty = 1
        WHERE id = ?`,
		model, u.Input, u.Output, u.CacheRead, u.CacheWrite, sessionID)
	return err
}

// EnsureSyncUIDs backfills sync_uid for rows captured before migration 2,
// deterministically from the machine namespace so repeated or concurrent
// backfills assign the same uid to the same row. Called by the syncer, which
// owns the machine identity; the hook path never runs this.
func (s *Store) EnsureSyncUIDs(namespace uuid.UUID) error {
	for _, table := range []string{"sessions", "events"} {
		rows, err := s.Query(fmt.Sprintf(`SELECT id FROM %s WHERE sync_uid IS NULL`, table))
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			uid := uuid.NewSHA1(namespace, []byte(fmt.Sprintf("%s:%d", table, id))).String()
			if _, err := s.Exec(fmt.Sprintf(`UPDATE %s SET sync_uid = ? WHERE id = ? AND sync_uid IS NULL`, table), uid, id); err != nil {
				return err
			}
		}
	}
	return nil
}
