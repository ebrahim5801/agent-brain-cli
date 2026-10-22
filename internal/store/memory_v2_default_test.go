package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func openStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	st, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func schemaOf(t *testing.T, st *Store) int {
	t.Helper()
	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A brand-new SQLite store starts at Memory V2: there is nothing in it for the
// reviewed upgrade to protect.
func TestFreshSQLiteStoreStartsAtMemoryV2(t *testing.T) {
	t.Setenv("AGENT_BRAIN_MEMORY_V2_DEFAULT", "")
	st := openStoreAt(t, filepath.Join(t.TempDir(), "agent-brain.db"))
	defer st.Close()
	if !st.MemoryV2 || schemaOf(t, st) != memoryV2Version {
		t.Fatalf("fresh store: v2=%v schema=%d, want V2 at %d", st.MemoryV2, schemaOf(t, st), memoryV2Version)
	}
	if err := st.AssertMemoryCapability(); err != nil {
		t.Fatalf("fresh V2 store fails its capability check: %v", err)
	}
}

// Opening an existing pre-V2 store never upgrades it: only the explicit paths
// (memory enable, upgrade-v2 --apply) do, after a backup.
func TestExistingV1StoreStaysV1OnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-brain.db")
	t.Setenv("AGENT_BRAIN_MEMORY_V2_DEFAULT", "off")
	openStoreAt(t, path).Close()

	t.Setenv("AGENT_BRAIN_MEMORY_V2_DEFAULT", "")
	st := openStoreAt(t, path)
	defer st.Close()
	if st.MemoryV2 || schemaOf(t, st) != 15 {
		t.Fatalf("existing V1 store changed on open: v2=%v schema=%d", st.MemoryV2, schemaOf(t, st))
	}
}

func TestUpgradeMemoryV2WithBackupKeepsAPrivateCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-brain.db")
	t.Setenv("AGENT_BRAIN_MEMORY_V2_DEFAULT", "off")
	st := openStoreAt(t, path)
	defer st.Close()
	if _, err := st.DB.Exec(`INSERT INTO projects (identity_kind, identity, display_name, first_seen_at, last_seen_at) VALUES ('remote', 'id-1', 'demo', ?, ?)`, Now(), Now()); err != nil {
		t.Fatal(err)
	}

	backup, err := st.UpgradeMemoryV2WithBackup()
	if err != nil {
		t.Fatal(err)
	}
	if !st.MemoryV2 || schemaOf(t, st) != memoryV2Version {
		t.Fatalf("store not upgraded: v2=%v schema=%d", st.MemoryV2, schemaOf(t, st))
	}
	if filepath.Dir(backup) != dir || !strings.HasPrefix(filepath.Base(backup), "agent-brain.db.pre-v2-") || !strings.HasSuffix(backup, ".bak") {
		t.Fatalf("backup path = %q", backup)
	}
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %v, want 0600", info.Mode().Perm())
	}

	// The copy is the store as it was: schema 15, same rows.
	db, err := sql.Open("sqlite", "file:"+backup+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, projects int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM projects WHERE identity='id-1'`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if version != 15 || projects != 1 {
		t.Errorf("backup: schema %d with %d matching projects, want 15 and 1", version, projects)
	}

	// Already V2: nothing to do, no second copy.
	again, err := st.UpgradeMemoryV2WithBackup()
	if err != nil || again != "" {
		t.Fatalf("second upgrade = %q, %v; want no-op", again, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.bak"))
	if len(matches) != 1 {
		t.Errorf("backups = %v, want exactly one", matches)
	}
}

func TestUpgradeMemoryV2WithBackupRefusesPostgres(t *testing.T) {
	st := &Store{Backend: BackendPostgres}
	backup, err := st.UpgradeMemoryV2WithBackup()
	if err == nil || backup != "" || !strings.Contains(err.Error(), "pg_dump") {
		t.Fatalf("postgres upgrade = %q, %v; want a refusal naming pg_dump", backup, err)
	}
}
