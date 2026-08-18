package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// wrote. The storage commands print with fmt.Print* directly, so this is how we
// assert their output.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func runStorageStatus(t *testing.T) (string, error) {
	t.Helper()
	return captureStdout(t, func() error {
		cmd := newStorageStatusCmd()
		return cmd.RunE(cmd, nil)
	})
}

// TestStorageStatusBasicSQLite covers the default backend: Basic label, the
// FR-006 discoverability line, and — since the sandbox config has no account or
// entitlement section — FR-009 (works logged-out, no gating).
func TestStorageStatusBasicSQLite(t *testing.T) {
	sandbox(t)
	out, err := runStorageStatus(t)
	if err != nil {
		t.Fatalf("storage status: %v", err)
	}
	if !strings.Contains(out, "Basic (SQLite)") {
		t.Errorf("missing Basic label:\n%s", out)
	}
	if !strings.Contains(out, "Advanced mode stores your data in PostgreSQL instead") {
		t.Errorf("missing discoverability line:\n%s", out)
	}
	// FR-009: no account/entitlement in config, yet the command succeeded.
	cfg, _ := config.Load()
	if cfg.SignedIn() || cfg.Entitlement != nil {
		t.Fatal("sandbox config unexpectedly has account/entitlement")
	}
}

func TestStorageUseSQLiteWhenAlreadyBasic(t *testing.T) {
	sandbox(t)
	out, err := captureStdout(t, runUseSQLite)
	if err != nil {
		t.Fatalf("use sqlite: %v", err)
	}
	if !strings.Contains(out, "Already on Basic") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestStorageUsePostgresRequiresDSN(t *testing.T) {
	sandbox(t)
	if err := runUsePostgres("  "); err == nil {
		t.Fatal("expected an error when --dsn is empty")
	} else if !strings.Contains(err.Error(), "--dsn") {
		t.Errorf("error should mention --dsn, got: %v", err)
	}
}

// TestStorageUsePostgresRefusesWhenAlreadyPostgres: the migration source is
// always the SQLite backup, so re-running the switch while on Postgres would
// silently abandon the Postgres-era data — it must refuse, config untouched,
// without leaking the stored DSN's password.
func TestStorageUsePostgresRefusesWhenAlreadyPostgres(t *testing.T) {
	sandbox(t)
	const oldDSN = "postgres://u:secret@db.example:5432/old"
	if _, err := config.Update(func(c *config.Config) error {
		c.Storage = &config.Storage{Backend: string(store.BackendPostgres), DSN: oldDSN}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err := runUsePostgres("postgres://u:p@db.example:5432/new")
	if err == nil {
		t.Fatal("expected refusal when already on postgres")
	}
	if !strings.Contains(err.Error(), "already on PostgreSQL") {
		t.Errorf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaked the stored DSN password: %v", err)
	}
	cfg, _ := config.Load()
	if cfg.Storage == nil || cfg.Storage.DSN != oldDSN {
		t.Errorf("config changed despite refusal: %+v", cfg.Storage)
	}
}

// TestOpenRejectsEmptyPostgresDSN: a config that selects postgres without a DSN
// must fail loud, not fall through to pgx's libpq defaults (a different
// database than the user configured).
func TestOpenRejectsEmptyPostgresDSN(t *testing.T) {
	sandbox(t)
	t.Setenv("AGENT_BRAIN_PG_DSN", "")
	if _, err := config.Update(func(c *config.Config) error {
		c.Storage = &config.Storage{Backend: string(store.BackendPostgres)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(); err == nil || !strings.Contains(err.Error(), "storage.dsn is empty") {
		t.Fatalf("expected empty-DSN error, got: %v", err)
	}
}

// TestStorageUsePostgresSwitchStatusRevert exercises the full round trip against
// a real Postgres (gated): fresh switch → status shows Advanced with the
// password redacted → revert flips back to Basic.
func TestStorageUsePostgresSwitchStatusRevert(t *testing.T) {
	sandbox(t)
	dsn := storetest.PostgresDSN(t) // skips when the DSN env is unset

	if _, err := captureStdout(t, func() error { return runUsePostgres(dsn) }); err != nil {
		t.Fatalf("use postgres: %v", err)
	}
	cfg, _ := config.Load()
	if cfg.Storage == nil || cfg.Storage.Backend != string(store.BackendPostgres) {
		t.Fatalf("config not switched to postgres: %+v", cfg.Storage)
	}

	out, err := runStorageStatus(t)
	if err != nil {
		t.Fatalf("status on postgres: %v", err)
	}
	if !strings.Contains(out, "Advanced (PostgreSQL)") {
		t.Errorf("status missing Advanced label:\n%s", out)
	}
	// The redaction must not echo the DSN password in plaintext.
	if strings.Contains(out, ":dev@") || strings.Contains(out, "password=dev") {
		t.Errorf("status leaked the DSN password:\n%s", out)
	}

	out, err = captureStdout(t, runUseSQLite)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if !strings.Contains(out, "Reverted to Basic") || !strings.Contains(out, "stays in PostgreSQL") {
		t.Errorf("revert output wrong:\n%s", out)
	}
	cfg, _ = config.Load()
	if cfg.Storage != nil {
		t.Errorf("storage section not cleared on revert: %+v", cfg.Storage)
	}
}

// TestStorageUsePostgresRefusesNonEmptyTarget covers FR-010: a target already
// holding agent-brain rows is refused, config untouched.
func TestStorageUsePostgresRefusesNonEmptyTarget(t *testing.T) {
	sandbox(t)
	dsn := storetest.PostgresDSN(t)

	// Pre-populate the target: open it and seed a project row.
	seed, err := store.OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	if _, err := seed.UpsertProject(store.ProjectIdentity{Kind: "repo_root", Identity: "/pre", DisplayName: "pre"}, store.Now()); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	seed.Close()

	err = runUsePostgres(dsn)
	if err == nil {
		t.Fatal("expected FR-010 refusal for a non-empty target")
	}
	if !strings.Contains(err.Error(), "already contains agent-brain data") {
		t.Errorf("unexpected error: %v", err)
	}
	// Config must be untouched.
	cfg, _ := config.Load()
	if cfg.Storage != nil {
		t.Errorf("config flipped despite refusal: %+v", cfg.Storage)
	}
}
