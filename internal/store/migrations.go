package store

import (
	"database/sql"
	"fmt"
)

// Forward-only migrations; index in this slice + 1 is the schema version.
var migrations = []string{
	`
CREATE TABLE projects (
    id            INTEGER PRIMARY KEY,
    identity_kind TEXT NOT NULL,
    identity      TEXT NOT NULL UNIQUE,
    display_name  TEXT NOT NULL,
    first_seen_at TEXT NOT NULL,
    last_seen_at  TEXT NOT NULL
);

CREATE TABLE sessions (
    id                 INTEGER PRIMARY KEY,
    external_id        TEXT NOT NULL UNIQUE,
    project_id         INTEGER NOT NULL REFERENCES projects(id),
    assistant          TEXT NOT NULL DEFAULT 'claude-code',
    started_at         TEXT NOT NULL,
    ended_at           TEXT,
    end_reason         TEXT,
    model              TEXT,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    transcript_path    TEXT
);
CREATE INDEX idx_sessions_project_started ON sessions(project_id, started_at);
CREATE INDEX idx_sessions_open ON sessions(ended_at) WHERE ended_at IS NULL;

CREATE TABLE events (
    id          INTEGER PRIMARY KEY,
    session_id  INTEGER NOT NULL REFERENCES sessions(id),
    kind        TEXT NOT NULL,
    detail      TEXT,
    occurred_at TEXT NOT NULL
);
CREATE INDEX idx_events_session_time ON events(session_id, occurred_at);

CREATE TABLE diagnostics (
    id          INTEGER PRIMARY KEY,
    occurred_at TEXT NOT NULL,
    component   TEXT NOT NULL,
    message     TEXT NOT NULL
);
`,
	`
ALTER TABLE sessions ADD COLUMN sync_uid TEXT;
ALTER TABLE sessions ADD COLUMN synced_at TEXT;
ALTER TABLE sessions ADD COLUMN sync_dirty INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN sync_uid TEXT;
ALTER TABLE events ADD COLUMN synced_at TEXT;

CREATE UNIQUE INDEX idx_sessions_sync_uid ON sessions(sync_uid) WHERE sync_uid IS NOT NULL;
CREATE UNIQUE INDEX idx_events_sync_uid ON events(sync_uid) WHERE sync_uid IS NOT NULL;
CREATE INDEX idx_sessions_unsynced ON sessions(project_id) WHERE synced_at IS NULL OR sync_dirty = 1;
CREATE INDEX idx_events_unsynced ON events(session_id) WHERE synced_at IS NULL;
`,
	`
CREATE TABLE memories (
    id            INTEGER PRIMARY KEY,
    project_id    INTEGER NOT NULL REFERENCES projects(id),
    session_id    INTEGER REFERENCES sessions(id),
    content       TEXT NOT NULL,
    kind          TEXT NOT NULL,
    origin        TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active',
    superseded_by INTEGER REFERENCES memories(id),
    branch        TEXT,
    commit_hash   TEXT,
    captured_at   TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    edited        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_memories_project_active ON memories(project_id, captured_at DESC) WHERE status = 'active';
CREATE INDEX idx_memories_session ON memories(session_id) WHERE session_id IS NOT NULL;

ALTER TABLE projects ADD COLUMN memory_disabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN memory_distilled_at TEXT;
`,
	`
ALTER TABLE memories ADD COLUMN personal_only INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memories ADD COLUMN team_uid TEXT;
ALTER TABLE memories ADD COLUMN shared_at TEXT;
CREATE INDEX idx_memories_share_pending ON memories(project_id)
    WHERE team_uid IS NOT NULL AND shared_at IS NULL;

CREATE TABLE team_memories (
    uid            TEXT PRIMARY KEY,
    project_id     INTEGER NOT NULL REFERENCES projects(id),
    author         TEXT NOT NULL,
    author_former  INTEGER NOT NULL DEFAULT 0,
    content        TEXT NOT NULL,
    kind           TEXT NOT NULL,
    origin         TEXT NOT NULL,
    status         TEXT NOT NULL,
    contradicts    TEXT,
    flagged        INTEGER NOT NULL DEFAULT 0,
    mine           INTEGER NOT NULL DEFAULT 0,
    branch         TEXT,
    commit_hash    TEXT,
    captured_at    TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
CREATE INDEX idx_team_memories_project_active ON team_memories(project_id, captured_at DESC)
    WHERE status = 'active';

CREATE TABLE team_sync_state (
    project_id  INTEGER PRIMARY KEY REFERENCES projects(id),
    pull_cursor TEXT NOT NULL DEFAULT ''
);
`,
	`
ALTER TABLE memories ADD COLUMN share_error TEXT;
`,
	`
CREATE TABLE memory_team_supersedes (
    memory_id INTEGER NOT NULL REFERENCES memories(id),
    team_uid  TEXT NOT NULL,
    PRIMARY KEY (memory_id, team_uid)
);
`,
	`
ALTER TABLE team_sync_state ADD COLUMN endpoint TEXT NOT NULL DEFAULT '';
`,
	`
ALTER TABLE sessions ADD COLUMN summary TEXT;
`,
	`
ALTER TABLE sessions ADD COLUMN parent_session_id INTEGER REFERENCES sessions(id);
ALTER TABLE sessions ADD COLUMN agent_type TEXT;
ALTER TABLE sessions ADD COLUMN agent_prompt TEXT;
CREATE INDEX idx_sessions_parent ON sessions(parent_session_id) WHERE parent_session_id IS NOT NULL;
`,
	`
ALTER TABLE sessions ADD COLUMN memory_checkpointed_at TEXT;
`,
	`
CREATE TABLE memory_usage_events (
    id         INTEGER PRIMARY KEY,
    session_id INTEGER NOT NULL REFERENCES sessions(id),
    project_id INTEGER NOT NULL REFERENCES projects(id),
    scope      TEXT NOT NULL,
    memory_id  INTEGER,
    team_uid   TEXT,
    retrieved  INTEGER NOT NULL DEFAULT 0,
    cited      INTEGER NOT NULL DEFAULT 0,
    first_at   TEXT NOT NULL,
    last_at    TEXT NOT NULL,
    synced_at  TEXT,
    sync_dirty INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_memusage_session_ref
    ON memory_usage_events(session_id, scope, COALESCE(memory_id, 0), COALESCE(team_uid, ''));
CREATE INDEX idx_memusage_unsynced ON memory_usage_events(session_id)
    WHERE synced_at IS NULL OR sync_dirty = 1;
`,
	`
CREATE TABLE session_model_usage (
    id                 INTEGER PRIMARY KEY,
    session_id         INTEGER NOT NULL REFERENCES sessions(id),
    model              TEXT NOT NULL,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    synced_at          TEXT,
    sync_dirty         INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_session_model_usage_ref ON session_model_usage(session_id, model);
CREATE INDEX idx_session_model_usage_unsynced ON session_model_usage(session_id)
    WHERE synced_at IS NULL OR sync_dirty = 1;
`,
	`
ALTER TABLE sessions ADD COLUMN memory_prompt_injected_at TEXT;
`,
}

// migrate applies forward migrations for the given backend. SQLite walks the
// `migrations` slice one statement-group at a time. Postgres records a single
// collapsed baseline as versions 1..8 and then appends future migrations in
// lockstep with SQLite (see migrations_postgres.go); numbering stays aligned so
// one version number always means one schema state across both backends.
func migrate(db *sql.DB, backend Backend) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
        version    BIGINT PRIMARY KEY,
        applied_at TEXT NOT NULL
    )`); err != nil {
		return err
	}
	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	steps := migrationSteps(backend)
	insertVersion := rebind(backend, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`)
	for i := current; i < len(steps); i++ {
		step := steps[i]
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(step.ddl); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %v: %w", step.versions, err)
		}
		for _, v := range step.versions {
			if _, err := tx.Exec(insertVersion, v, Now()); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %v: %w", step.versions, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %v: %w", step.versions, err)
		}
	}
	return nil
}

// migrationStep is one applied unit: a DDL blob plus the schema_migrations
// version rows it records. SQLite steps record one version each; the Postgres
// baseline records versions 1..8 in a single step.
type migrationStep struct {
	ddl      string
	versions []int
}

// migrationSteps returns the ordered migration units for a backend. The slice
// is walked by index against the current MAX(version): SQLite's Nth step is
// version N, and Postgres's baseline occupies the first `baseline` versions so
// that appended entries line up with SQLite from version 9 on.
func migrationSteps(backend Backend) []migrationStep {
	if backend == BackendPostgres {
		steps := make([]migrationStep, 0, 1+len(migrationsPostgres))
		steps = append(steps, migrationStep{ddl: postgresBaseline, versions: baselineVersions()})
		for i, ddl := range migrationsPostgres {
			steps = append(steps, migrationStep{ddl: ddl, versions: []int{sqliteBaselineVersions + 1 + i}})
		}
		return steps
	}
	steps := make([]migrationStep, len(migrations))
	for i, ddl := range migrations {
		steps[i] = migrationStep{ddl: ddl, versions: []int{i + 1}}
	}
	return steps
}

// sqliteBaselineVersions is the number of SQLite migration steps collapsed into
// the single Postgres baseline (SQLite schema versions 1..8).
const sqliteBaselineVersions = 8

func baselineVersions() []int {
	vs := make([]int, sqliteBaselineVersions)
	for i := range vs {
		vs[i] = i + 1
	}
	return vs
}
