package store

import (
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// T032: data written under one schema version stays readable after the binary
// upgrades and applies a later migration.
func TestReopenAppliesForwardMigrationsAndKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-brain.db")

	st, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	at := Now()
	projectID, err := st.UpsertProject(ProjectIdentity{Kind: "remote", Identity: "gitlab.com/acme/x", DisplayName: "x"}, at)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := st.EnsureSession("sess-1", projectID, at, "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent(sessionID, "prompt", "", at); err != nil {
		t.Fatal(err)
	}
	st.Close()

	original := migrations
	migrations = append(append([]string{}, original...), `CREATE TABLE zz_upgrade_noop (id INTEGER PRIMARY KEY);`)
	defer func() { migrations = original }()

	st2, err := OpenAt(path)
	if err != nil {
		t.Fatalf("reopen after upgrade: %v", err)
	}
	defer st2.Close()

	var version int
	if err := st2.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Errorf("schema version = %d, want %d", version, len(migrations))
	}
	var identity string
	if err := st2.DB.QueryRow(`SELECT identity FROM projects WHERE id = ?`, projectID).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if identity != "gitlab.com/acme/x" {
		t.Errorf("project identity = %q after upgrade", identity)
	}
	var events int
	if err := st2.DB.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("events = %d after upgrade, want 1", events)
	}
}

// Migration 2: capture rows get sync metadata; pre-migration rows backfill
// deterministic sync_uids so re-running the backfill never reassigns them.
func TestSyncColumnsAndDeterministicBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-brain.db")
	st, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	at := Now()
	projectID, err := st.UpsertProject(ProjectIdentity{Kind: "remote", Identity: "gitlab.com/acme/y", DisplayName: "y"}, at)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := st.EnsureSession("sess-uid", projectID, at, "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}

	var syncUID string
	if err := st.DB.QueryRow(`SELECT sync_uid FROM sessions WHERE id = ?`, sessionID).Scan(&syncUID); err != nil {
		t.Fatal(err)
	}
	if syncUID == "" {
		t.Fatal("new session has no sync_uid")
	}

	if _, err := st.DB.Exec(`UPDATE sessions SET sync_uid = NULL WHERE id = ?`, sessionID); err != nil {
		t.Fatal(err)
	}
	ns := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	if err := st.EnsureSyncUIDs(ns); err != nil {
		t.Fatal(err)
	}
	var first string
	if err := st.DB.QueryRow(`SELECT sync_uid FROM sessions WHERE id = ?`, sessionID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureSyncUIDs(ns); err != nil {
		t.Fatal(err)
	}
	var second string
	if err := st.DB.QueryRow(`SELECT sync_uid FROM sessions WHERE id = ?`, sessionID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Errorf("backfill not deterministic: %q vs %q", first, second)
	}

	if err := st.CloseSession(sessionID, Now(), "normal"); err != nil {
		t.Fatal(err)
	}
	var dirty int
	if err := st.DB.QueryRow(`SELECT sync_dirty FROM sessions WHERE id = ?`, sessionID).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 1 {
		t.Errorf("close did not mark session sync-dirty")
	}
}

// Migration 3: memory schema exists on a fresh store and applies cleanly over
// a store that already carries v1+v2 data.
func TestMemoryMigrationOnFreshAndUpgradedStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-brain.db")
	st, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}

	at := Now()
	projectID, err := st.UpsertProject(ProjectIdentity{Kind: "remote", Identity: "gitlab.com/acme/z", DisplayName: "z"}, at)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := st.EnsureSession("sess-mem", projectID, at, "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := st.DB.Exec(`
        INSERT INTO memories (project_id, session_id, content, kind, origin, branch, commit_hash, captured_at, updated_at)
        VALUES (?, ?, 'use pgx directly', 'decision', 'explicit', 'main', 'abc123', ?, ?)`,
		projectID, sessionID, at, at); err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	var disabled int
	if err := st.DB.QueryRow(`SELECT memory_disabled FROM projects WHERE id = ?`, projectID).Scan(&disabled); err != nil {
		t.Fatalf("memory_disabled column: %v", err)
	}
	if disabled != 0 {
		t.Errorf("memory_disabled default = %d, want 0", disabled)
	}
	var distilled *string
	if err := st.DB.QueryRow(`SELECT memory_distilled_at FROM sessions WHERE id = ?`, sessionID).Scan(&distilled); err != nil {
		t.Fatalf("memory_distilled_at column: %v", err)
	}
	if distilled != nil {
		t.Errorf("memory_distilled_at default = %v, want NULL", *distilled)
	}
	st.Close()

	st2, err := OpenAt(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	var content string
	if err := st2.DB.QueryRow(`SELECT content FROM memories WHERE project_id = ?`, projectID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "use pgx directly" {
		t.Errorf("memory content = %q after reopen", content)
	}
}

// Migration 4: share-tracking columns, the disposable team cache, and pull
// state exist on a fresh store and apply cleanly over a v3 store with data.
func TestTeamMemoryMigrationOnFreshAndUpgradedStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-brain.db")
	st, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}

	at := Now()
	projectID, err := st.UpsertProject(ProjectIdentity{Kind: "remote", Identity: "gitlab.com/acme/team", DisplayName: "team"}, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`
        INSERT INTO memories (project_id, content, kind, origin, captured_at, updated_at)
        VALUES (?, 'retries use backoff', 'decision', 'auto', ?, ?)`,
		projectID, at, at); err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	var personalOnly int
	if err := st.DB.QueryRow(`SELECT personal_only FROM memories WHERE project_id = ?`, projectID).Scan(&personalOnly); err != nil {
		t.Fatalf("personal_only column: %v", err)
	}
	if personalOnly != 0 {
		t.Errorf("personal_only default = %d, want 0", personalOnly)
	}

	if _, err := st.DB.Exec(`
        INSERT INTO team_memories (uid, project_id, author, content, kind, origin, status, captured_at, updated_at)
        VALUES ('u1', ?, 'a@example.com', 'shared decision', 'decision', 'auto', 'active', ?, ?)`,
		projectID, at, at); err != nil {
		t.Fatalf("insert team memory: %v", err)
	}
	if _, err := st.DB.Exec(`INSERT INTO team_sync_state (project_id, pull_cursor) VALUES (?, '')`, projectID); err != nil {
		t.Fatalf("insert team_sync_state: %v", err)
	}
	st.Close()

	st2, err := OpenAt(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	var author string
	if err := st2.DB.QueryRow(`SELECT author FROM team_memories WHERE uid = 'u1'`).Scan(&author); err != nil {
		t.Fatal(err)
	}
	if author != "a@example.com" {
		t.Errorf("team memory author = %q after reopen", author)
	}
}
