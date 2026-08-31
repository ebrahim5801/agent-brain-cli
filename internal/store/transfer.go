package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// TableCount reports a table's row count on each side after a transfer, for the
// per-table verification step. A mismatch aborts the switch with the config
// untouched.
type TableCount struct {
	Table    string
	SQLite   int64
	Postgres int64
}

// copyBatchRows caps how many rows go into one multi-row INSERT. 500 rows ×
// the widest table (~18 columns) stays well under Postgres's 65535-parameter
// limit.
const copyBatchRows = 500

// deltaAttempts bounds the post-copy catch-up loop. If new rows keep arriving
// (an assistant session is actively writing), the switch aborts and asks the
// user to stop active sessions rather than loop forever.
const deltaAttempts = 5

// testHookAfterSnapshot, when non-nil, runs between the snapshot copy and the
// delta pass. Tests use it to write a row a concurrent hook process would have
// written, which the snapshot cannot contain, so the delta pass itself is
// exercised.
var testHookAfterSnapshot func()

// transferColumns lists the columns copied per table, in FK dependency order.
// Explicit lists (never SELECT *) mean a future migration that adds a column to
// only one backend's schema fails the lockstep guard rather than silently
// mis-copying. schema_migrations is deliberately absent: the target wrote its
// own baseline.
var transferColumns = []struct {
	table string
	cols  []string
}{
	{"projects", []string{"id", "identity_kind", "identity", "display_name", "first_seen_at", "last_seen_at", "memory_disabled"}},
	// parent_session_id is a self-FK, but parents always carry smaller ids than
	// their sub-sessions (the parent row exists before any subagent is
	// captured), so the id-ordered copy satisfies the FK in one pass.
	{"sessions", []string{"id", "external_id", "project_id", "assistant", "started_at", "ended_at", "end_reason", "model",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "transcript_path",
		"sync_uid", "synced_at", "sync_dirty", "memory_distilled_at", "memory_checkpointed_at", "memory_prompt_injected_at",
		"sync_reject_count", "sync_retry_after", "summary", "parent_session_id", "agent_type", "agent_prompt"}},
	{"events", []string{"id", "session_id", "kind", "detail", "occurred_at", "sync_uid", "synced_at"}},
	{"session_model_usage", []string{"id", "session_id", "model", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_write_tokens", "synced_at", "sync_dirty"}},
	// memories is copied two-pass (superseded_by is a self-FK); see copyMemories.
	{"memories", []string{"id", "project_id", "session_id", "content", "kind", "origin", "priority", "status", "superseded_by",
		"branch", "commit_hash", "captured_at", "updated_at", "edited", "personal_only", "team_uid", "shared_at", "share_error"}},
	{"memory_usage_events", []string{"id", "session_id", "project_id", "scope", "memory_id", "team_uid",
		"retrieved", "cited", "first_at", "last_at", "synced_at", "sync_dirty"}},
	{"team_memories", []string{"uid", "project_id", "author", "author_former", "content", "kind", "origin", "priority", "status",
		"contradicts", "flagged", "mine", "branch", "commit_hash", "captured_at", "updated_at"}},
	{"team_sync_state", []string{"project_id", "pull_cursor", "endpoint"}},
	{"memory_team_supersedes", []string{"memory_id", "team_uid"}},
	{"diagnostics", []string{"id", "occurred_at", "component", "message"}},
}

// DataTables lists agent-brain's data tables in a stable display order (the
// transfer/verification order). Used by `storage status` for per-table counts
// and by the switch to enforce FR-010 (refuse a non-empty target).
func DataTables() []string {
	out := make([]string, len(transferColumns))
	for i, t := range transferColumns {
		out[i] = t.table
	}
	return out
}

// CountRows returns the row count of one of agent-brain's own tables. The table
// name comes from DataTables (a fixed allowlist), never user input.
func (s *Store) CountRows(table string) (int64, error) {
	var n int64
	err := s.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
	return n, err
}

// SchemaVersion reports the highest applied migration version (0 if none).
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

// HasAgentBrainData reports whether any of agent-brain's own tables hold rows —
// the FR-010 check that refuses to migrate into a populated target.
func (s *Store) HasAgentBrainData() (bool, string, int64, error) {
	for _, t := range DataTables() {
		n, err := s.CountRows(t)
		if err != nil {
			return false, "", 0, err
		}
		if n > 0 {
			return true, t, n, nil
		}
	}
	return false, "", 0, nil
}

// TransferTo copies every agent-brain table from this SQLite store to an
// already-migrated Postgres store, ids preserved. The source is read inside one
// snapshot transaction for cross-table consistency; a bounded delta pass then
// catches rows written concurrently (active hook processes) so nothing is
// stranded in the SQLite backup (Principle I). It returns per-table counts for
// verification and never modifies the SQLite data.
func (src *Store) TransferTo(dst *Store) ([]TableCount, error) {
	if src.Backend != BackendSQLite || dst.Backend != BackendPostgres {
		return nil, fmt.Errorf("TransferTo copies sqlite→postgres, got %s→%s", src.Backend, dst.Backend)
	}

	// Snapshot: a read transaction on the single SQLite connection gives every
	// table SELECT the same view. Rollback (never commit) — the source is
	// read-only in practice and must stay logically untouched (FR-004).
	snap, err := src.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer snap.Rollback()

	copiedMax := map[string]int64{} // per identity table: highest id copied so far

	for _, t := range transferColumns {
		if t.table == "memories" {
			maxID, err := copyMemories(snap, dst, t.cols)
			if err != nil {
				return nil, fmt.Errorf("copy memories: %w", err)
			}
			copiedMax["memories"] = maxID
			continue
		}
		maxID, err := copyTable(snap, dst, t.table, t.cols, "")
		if err != nil {
			return nil, fmt.Errorf("copy %s: %w", t.table, err)
		}
		if isIdentityTable(t.table) {
			copiedMax[t.table] = maxID
		}
	}

	if err := snap.Rollback(); err != nil && err != sql.ErrTxDone {
		return nil, err
	}

	if testHookAfterSnapshot != nil {
		testHookAfterSnapshot()
	}

	// Delta pass: re-read live SQLite outside the snapshot; copy identity-table
	// rows written during the main copy (id beyond what we captured). Repeat
	// until counts stabilize or the attempt budget is exhausted.
	stable := false
	for attempt := 0; attempt < deltaAttempts; attempt++ {
		copiedAny := false
		for _, table := range identityTables {
			cols := columnsFor(table)
			where := fmt.Sprintf("id > %d", copiedMax[table])
			var maxID int64
			var err error
			if table == "memories" {
				maxID, err = copyMemoriesWhere(src.DB, dst, cols, where)
			} else {
				maxID, err = copyTable(src.DB, dst, table, cols, where)
			}
			if err != nil {
				return nil, fmt.Errorf("delta copy %s: %w", table, err)
			}
			if maxID > copiedMax[table] {
				copiedMax[table] = maxID
				copiedAny = true
			}
		}
		counts, err := tableCounts(src, dst)
		if err != nil {
			return nil, err
		}
		if countsMatch(counts) {
			stable = true
			break
		}
		if !copiedAny {
			// Counts differ but no new identity rows to chase: a non-identity
			// table (or an in-place delta the id-cursor can't see) changed.
			break
		}
	}

	// Advance sequences past the preserved ids so future Postgres inserts don't
	// collide — done whether or not counts converged, so a target left for retry
	// is still internally consistent.
	if err := resetSequences(dst); err != nil {
		return nil, err
	}
	counts, err := tableCounts(src, dst)
	if err != nil {
		return nil, err
	}
	if !stable {
		return counts, fmt.Errorf("row counts did not stabilize after %d attempts — stop active assistant sessions and retry", deltaAttempts)
	}
	return counts, nil
}

// querier is the read side shared by the snapshot tx and the live *sql.DB.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// copyTable streams rows from the SQLite source into Postgres in batches,
// preserving ids. Returns the highest id observed (0 for non-identity tables or
// an empty table). Scanning is generic ([]any) so no per-table struct is
// needed; []byte text is normalized to string so pgx binds it as text.
func copyTable(src querier, dst *Store, table string, cols []string, where string) (int64, error) {
	q := "SELECT " + strings.Join(cols, ", ") + " FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	// Id order guarantees parents precede children for sessions'
	// parent_session_id self-FK (a parent always has the smaller id).
	if isIdentityTable(table) && cols[0] == "id" {
		q += " ORDER BY id"
	}
	rows, err := src.Query(q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	idCol := isIdentityTable(table) && cols[0] == "id"
	var maxID int64
	batch := make([][]any, 0, copyBatchRows)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := insertBatch(dst, table, cols, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return 0, err
		}
		for i := range vals {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
		}
		if idCol {
			if id, ok := vals[0].(int64); ok && id > maxID {
				maxID = id
			}
		}
		batch = append(batch, vals)
		if len(batch) >= copyBatchRows {
			if err := flush(); err != nil {
				return 0, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := flush(); err != nil {
		return 0, err
	}
	return maxID, nil
}

// insertBatch writes one multi-row INSERT. dst.Exec rebinds the `?` placeholders
// to `$n` for Postgres.
func insertBatch(dst *Store, table string, cols []string, batch [][]any) error {
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(table)
	b.WriteString(" (")
	b.WriteString(strings.Join(cols, ", "))
	b.WriteString(") VALUES ")
	args := make([]any, 0, len(batch)*len(cols))
	for r, row := range batch {
		if r > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		for c := range cols {
			if c > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('?')
			args = append(args, row[c])
		}
		b.WriteByte(')')
	}
	_, err := dst.Exec(b.String(), args...)
	return err
}

// copyMemories copies the memories table in two passes: first every row with
// superseded_by forced NULL (so no self-FK is violated mid-copy), then an UPDATE
// pass that sets superseded_by from the source. Returns the highest id copied.
func copyMemories(src querier, dst *Store, cols []string) (int64, error) {
	return copyMemoriesWhere(src, dst, cols, "")
}

func copyMemoriesWhere(src querier, dst *Store, cols []string, where string) (int64, error) {
	sbIdx := indexOf(cols, "superseded_by")
	idIdx := indexOf(cols, "id")

	q := "SELECT " + strings.Join(cols, ", ") + " FROM memories"
	if where != "" {
		q += " WHERE " + where
	}
	rows, err := src.Query(q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type sbLink struct{ id, superseded int64 }
	var links []sbLink
	var maxID int64
	batch := make([][]any, 0, copyBatchRows)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := insertBatch(dst, "memories", cols, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return 0, err
		}
		for i := range vals {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
		}
		id, _ := vals[idIdx].(int64)
		if id > maxID {
			maxID = id
		}
		if sb, ok := vals[sbIdx].(int64); ok {
			links = append(links, sbLink{id: id, superseded: sb})
			vals[sbIdx] = nil // pass one: break the self-FK
		}
		batch = append(batch, vals)
		if len(batch) >= copyBatchRows {
			if err := flush(); err != nil {
				return 0, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := flush(); err != nil {
		return 0, err
	}

	// Pass two: restore the supersede chain now that every target row exists.
	for _, l := range links {
		if _, err := dst.Exec(`UPDATE memories SET superseded_by = ? WHERE id = ?`, l.superseded, l.id); err != nil {
			return 0, err
		}
	}
	return maxID, nil
}

// resetSequences advances each identity table's sequence past the copied
// max(id) so future inserts on Postgres don't collide with preserved ids.
func resetSequences(dst *Store) error {
	for _, table := range identityTables {
		if _, err := dst.Exec(
			`SELECT setval(pg_get_serial_sequence(?, 'id'), (SELECT COALESCE(MAX(id), 1) FROM `+table+`))`,
			table); err != nil {
			return fmt.Errorf("reset %s sequence: %w", table, err)
		}
	}
	return nil
}

// tableCounts reads live COUNT(*) from both stores for every transferred table.
func tableCounts(src, dst *Store) ([]TableCount, error) {
	out := make([]TableCount, 0, len(transferColumns))
	for _, t := range transferColumns {
		var sc, pc int64
		if err := src.QueryRow(`SELECT COUNT(*) FROM ` + t.table).Scan(&sc); err != nil {
			return nil, err
		}
		if err := dst.QueryRow(`SELECT COUNT(*) FROM ` + t.table).Scan(&pc); err != nil {
			return nil, err
		}
		out = append(out, TableCount{Table: t.table, SQLite: sc, Postgres: pc})
	}
	return out, nil
}

func countsMatch(counts []TableCount) bool {
	for _, c := range counts {
		if c.SQLite != c.Postgres {
			return false
		}
	}
	return true
}

func isIdentityTable(table string) bool {
	for _, t := range identityTables {
		if t == table {
			return true
		}
	}
	return false
}

func columnsFor(table string) []string {
	for _, t := range transferColumns {
		if t.table == table {
			return t.cols
		}
	}
	return nil
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}
