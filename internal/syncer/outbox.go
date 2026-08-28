// Package syncer ships locally buffered sessions to the cloud: it reads the
// SQLite outbox (unsynced or dirty rows whose project is linked), builds
// wire batches, delivers them with retries, and marks rows only after the
// server acknowledges — lossless and idempotent by construction (FR-016..021).
package syncer

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/wire"
	"github.com/google/uuid"
)

const (
	maxBatchSessions = 500
)

type outboxSession struct {
	rowID   int64
	session wire.Session
	// usageSignature is a snapshot of the session's memory usage aggregate at
	// read time (row count, total retrievals, citations). It is not part of
	// the wire payload; markSynced uses it to detect a concurrent usage change
	// (e.g. a retrieval from the MCP server process) that arrived after this
	// snapshot but before the ack, the same concurrent-update guard already
	// applied to ended_at/usage tokens.
	usageSignature usageSignature
	// modelSignature is the same guard for the per-model usage rows: a backfill
	// or live model-usage delta landing between read and ack leaves the rows
	// sync-dirty rather than being falsely marked clean.
	modelSignature modelSignature
}

type usageSignature struct {
	rows      int64
	retrieved int64
	cited     int64
}

type modelSignature struct {
	rows   int64
	tokens int64
}

func memoryUsageSignature(refs []wire.MemoryUsageRef) usageSignature {
	sig := usageSignature{rows: int64(len(refs))}
	for _, r := range refs {
		sig.retrieved += r.Retrieved
		if r.Cited {
			sig.cited++
		}
	}
	return sig
}

func modelUsageSignature(refs []wire.ModelUsageRef) modelSignature {
	sig := modelSignature{rows: int64(len(refs))}
	for _, r := range refs {
		sig.tokens += r.InputTokens + r.OutputTokens + r.CacheReadTokens + r.CacheWriteTokens
	}
	return sig
}

// eligibleSessions returns unsynced/dirty sessions belonging to linked
// projects, with their event counts aggregated, oldest first. Only sessions
// with an end or usage already recorded sync their current snapshot; open
// sessions sync too and are re-sent when they change (sync_dirty).
//
// dueOnly skips rows still inside their post-rejection backoff. The delivery
// path sets it so a permanently rejected project cannot occupy the whole
// ORDER BY id window and starve newer rows; queue-depth reporting clears it,
// because a deferred row is still queued and hiding it would make `status`
// claim an empty outbox while a project quietly fails.
func eligibleSessions(st *store.Store, cfg *config.Config, limit int, dueOnly bool) ([]outboxSession, error) {
	if len(cfg.Links) == 0 {
		return nil, nil
	}
	keyByIdentity := map[string]string{}
	var identityArgs []any
	var placeholders []string
	for linkKey, link := range cfg.Links {
		keyByIdentity[linkKey] = link.ProjectKey
		identityArgs = append(identityArgs, linkKey)
		placeholders = append(placeholders, "?")
	}

	// Ordering by id also sends a parent before its sub-sessions (the parent
	// row always has the smaller id); the server does not require it, but it
	// keeps the dashboard from briefly showing an orphaned sub-session.
	dueClause := ""
	if dueOnly {
		dueClause = "\n          AND (s.sync_retry_after IS NULL OR s.sync_retry_after <= ?)"
		identityArgs = append(identityArgs, store.Now())
	}

	query := fmt.Sprintf(`
        SELECT s.id, s.sync_uid, p.identity_kind || ':' || p.identity,
               s.assistant,
               s.started_at, COALESCE(s.ended_at, ''), COALESCE(s.end_reason, ''), COALESCE(s.model, ''),
               s.input_tokens, s.output_tokens, s.cache_read_tokens, s.cache_write_tokens,
               COALESCE(parent.sync_uid, ''), COALESCE(s.agent_type, '')
        FROM sessions s
        JOIN projects p ON p.id = s.project_id
        LEFT JOIN sessions parent ON parent.id = s.parent_session_id
        WHERE (s.synced_at IS NULL OR s.sync_dirty = 1)
          AND s.sync_uid IS NOT NULL
          AND p.identity_kind || ':' || p.identity IN (%s)%s
        ORDER BY s.id
        LIMIT %d`, strings.Join(placeholders, ","), dueClause, limit)

	rows, err := st.Query(query, identityArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []outboxSession
	for rows.Next() {
		var o outboxSession
		var identityKey string
		if err := rows.Scan(&o.rowID, &o.session.SyncUID, &identityKey,
			&o.session.Assistant,
			&o.session.StartedAt, &o.session.EndedAt, &o.session.EndReason, &o.session.Model,
			&o.session.Usage.InputTokens, &o.session.Usage.OutputTokens,
			&o.session.Usage.CacheReadTokens, &o.session.Usage.CacheWriteTokens,
			&o.session.ParentSyncUID, &o.session.AgentType); err != nil {
			return nil, err
		}
		o.session.ProjectKey = keyByIdentity[identityKey]
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		counts, err := eventCounts(st, out[i].rowID)
		if err != nil {
			return nil, err
		}
		out[i].session.EventCounts = counts

		usage, err := memoryUsage(st, out[i].rowID)
		if err != nil {
			return nil, err
		}
		out[i].session.MemoryUsage = usage
		out[i].usageSignature = memoryUsageSignature(usage)

		models, err := modelUsage(st, out[i].rowID)
		if err != nil {
			return nil, err
		}
		out[i].session.ModelUsage = models
		out[i].modelSignature = modelUsageSignature(models)
	}
	return out, nil
}

func eventCounts(st *store.Store, sessionRowID int64) (map[string]int64, error) {
	rows, err := st.Query(`SELECT kind, COUNT(*) FROM events WHERE session_id = ? GROUP BY kind`, sessionRowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var kind string
		var n int64
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		counts[kind] = n
	}
	return counts, rows.Err()
}

// memoryUsage loads a session's memory_usage_events rows as wire refs, for
// the same batch that carries its event counts.
func memoryUsage(st *store.Store, sessionRowID int64) ([]wire.MemoryUsageRef, error) {
	rows, err := st.Query(`
        SELECT scope, COALESCE(memory_id, 0), COALESCE(team_uid, ''), retrieved, cited
        FROM memory_usage_events WHERE session_id = ?`, sessionRowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wire.MemoryUsageRef
	for rows.Next() {
		var ref wire.MemoryUsageRef
		var cited int64
		if err := rows.Scan(&ref.Scope, &ref.MemoryID, &ref.TeamUID, &ref.Retrieved, &cited); err != nil {
			return nil, err
		}
		ref.Cited = cited != 0
		out = append(out, ref)
	}
	return out, rows.Err()
}

// modelUsage loads a session's session_model_usage rows as wire refs, for the
// same batch that carries its aggregate token totals.
func modelUsage(st *store.Store, sessionRowID int64) ([]wire.ModelUsageRef, error) {
	rows, err := st.Query(`
        SELECT model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
        FROM session_model_usage WHERE session_id = ?
        ORDER BY (input_tokens + output_tokens + cache_read_tokens + cache_write_tokens) DESC, model`, sessionRowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wire.ModelUsageRef
	for rows.Next() {
		var ref wire.ModelUsageRef
		if err := rows.Scan(&ref.Model, &ref.InputTokens, &ref.OutputTokens, &ref.CacheReadTokens, &ref.CacheWriteTokens); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// markSynced records the server's acknowledgement: the session and its events
// are marked in one transaction (mark-after-ack; an interruption can only
// under-mark, and re-sending is safe because the server dedupes).
//
// The dirty flag is cleared only when the row still matches the snapshot we
// delivered: a hook process that closed the session or backfilled usage after
// the batch was read (but before this ack) leaves sync_dirty = 1 so the newer
// state re-syncs. Blindly clearing it here would strand that update — the row
// receives no further writes, so it would never be re-sent (FR-017, invariant
// "exactly-once, no loss").
func markSynced(st *store.Store, acked []outboxSession) error {
	if len(acked) == 0 {
		return nil
	}
	tx, err := st.DB.Begin()
	if err != nil {
		return err
	}
	now := store.Now()
	for _, a := range acked {
		s := a.session
		if _, err := tx.Exec(st.Rebind(`
            UPDATE sessions SET synced_at = ?, sync_dirty = 0
            WHERE id = ?
              AND COALESCE(ended_at, '') = ? AND COALESCE(end_reason, '') = ? AND COALESCE(model, '') = ?
              AND input_tokens = ? AND output_tokens = ?
              AND cache_read_tokens = ? AND cache_write_tokens = ?
              AND (SELECT COUNT(*) FROM memory_usage_events WHERE session_id = sessions.id) = ?
              AND (SELECT COALESCE(SUM(retrieved), 0) FROM memory_usage_events WHERE session_id = sessions.id) = ?
              AND (SELECT COALESCE(SUM(cited), 0) FROM memory_usage_events WHERE session_id = sessions.id) = ?
              AND (SELECT COUNT(*) FROM session_model_usage WHERE session_id = sessions.id) = ?
              AND (SELECT COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens), 0) FROM session_model_usage WHERE session_id = sessions.id) = ?`),
			now, a.rowID,
			s.EndedAt, s.EndReason, s.Model,
			s.Usage.InputTokens, s.Usage.OutputTokens,
			s.Usage.CacheReadTokens, s.Usage.CacheWriteTokens,
			a.usageSignature.rows, a.usageSignature.retrieved, a.usageSignature.cited,
			a.modelSignature.rows, a.modelSignature.tokens); err != nil {
			tx.Rollback()
			return err
		}
		// Unconditional, unlike the snapshot-guarded UPDATE above: an ack means
		// the server took the row, so the backoff has served its purpose even
		// if a concurrent write left the session dirty for another pass.
		if err := clearRejectState(tx, st, a.rowID); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.Exec(st.Rebind(`UPDATE events SET synced_at = ? WHERE session_id = ? AND synced_at IS NULL`), now, a.rowID); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.Exec(st.Rebind(`
            UPDATE memory_usage_events SET synced_at = ?, sync_dirty = 0
            WHERE session_id = ? AND sync_dirty = 1`), now, a.rowID); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.Exec(st.Rebind(`
            UPDATE session_model_usage SET synced_at = ?, sync_dirty = 0
            WHERE session_id = ? AND sync_dirty = 1`), now, a.rowID); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// rejectBackoffBase and rejectBackoffMax bound the per-row retry delay. The
// base is deliberately longer than the daemon's 60s tick so a rejected row
// leaves the batch window after a single failure rather than being retried on
// every cycle; the cap keeps a project that regains access from waiting more
// than a few hours to catch up on its own.
const (
	rejectBackoffBase = 5 * time.Minute
	rejectBackoffMax  = 6 * time.Hour
)

// rejectBackoff is the delay after n consecutive rejections of the same row.
func rejectBackoff(n int64) time.Duration {
	d := rejectBackoffBase
	for i := int64(1); i < n && d < rejectBackoffMax; i++ {
		d *= 2
	}
	if d > rejectBackoffMax {
		d = rejectBackoffMax
	}
	return d
}

// markRejected defers rows the server refused. The row stays queued — nothing
// clears synced_at or sync_dirty — but sync_retry_after moves it out of the
// delivery window so a permanently failing project cannot fill every batch and
// starve newer sessions (the ORDER BY id window is finite).
func markRejected(st *store.Store, rejected []outboxSession) error {
	if len(rejected) == 0 {
		return nil
	}
	tx, err := st.DB.Begin()
	if err != nil {
		return err
	}
	for _, r := range rejected {
		var n int64
		if err := tx.QueryRow(st.Rebind(
			`SELECT sync_reject_count FROM sessions WHERE id = ?`), r.rowID).Scan(&n); err != nil {
			tx.Rollback()
			return err
		}
		n++
		retryAt := time.Now().UTC().Add(rejectBackoff(n)).Format(store.TimeLayout)
		if _, err := tx.Exec(st.Rebind(`
            UPDATE sessions SET sync_reject_count = ?, sync_retry_after = ?
            WHERE id = ?`), n, retryAt, r.rowID); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// clearRejectState resets the backoff once a row is finally accepted, so a
// project that recovers does not carry an inflated delay into its next hiccup.
func clearRejectState(tx *sql.Tx, st *store.Store, rowID int64) error {
	_, err := tx.Exec(st.Rebind(`
        UPDATE sessions SET sync_reject_count = 0, sync_retry_after = NULL
        WHERE id = ? AND (sync_reject_count <> 0 OR sync_retry_after IS NOT NULL)`), rowID)
	return err
}

// UnsyncedCount reports queue depth for heartbeat and status.
func UnsyncedCount(st *store.Store, cfg *config.Config) int64 {
	sessions, err := eligibleSessions(st, cfg, maxBatchSessions*10, false)
	if err != nil {
		return 0
	}
	return int64(len(sessions))
}

// machineNamespace derives the uuid5 namespace for backfilling pre-migration
// rows from the machine id, so every backfill on this machine agrees.
func machineNamespace(cfg *config.Config) (uuid.UUID, error) {
	if cfg.MachineID == "" {
		return uuid.UUID{}, fmt.Errorf("no machine id; run agent-brain login")
	}
	return uuid.Parse(cfg.MachineID)
}
