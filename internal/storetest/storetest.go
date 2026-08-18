// Package storetest provides test helpers for exercising the local store
// against both backends. It is imported only by test files; nothing in the
// production binary depends on it (so its "testing" import never ships).
package storetest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// PGEnv is the environment variable holding the DSN of a scratch PostgreSQL
// database for the dual-backend test matrix. Unset ⇒ Postgres tests skip.
const PGEnv = "AGENT_BRAIN_TEST_PG_DSN"

// SQLite opens a throwaway SQLite store in a temp dir.
func SQLite(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// Postgres opens an isolated PostgreSQL store: a private schema created for this
// test (via the connection's search_path) and dropped on cleanup, so parallel
// test packages never collide and each test starts on the baseline schema. It
// skips the test when PGEnv is unset.
func Postgres(t *testing.T) *store.Store {
	t.Helper()
	base := os.Getenv(PGEnv)
	if base == "" {
		t.Skipf("%s not set; skipping Postgres backend test", PGEnv)
	}
	schema := "abtest_" + randHex(t)

	boot, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open bootstrap pg conn: %v", err)
	}
	if _, err := boot.Exec("CREATE SCHEMA " + schema); err != nil {
		boot.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	boot.Close()

	st, err := store.OpenPostgres(withSearchPath(t, base, schema))
	if err != nil {
		dropSchema(base, schema)
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() {
		st.Close()
		dropSchema(base, schema)
	})
	return st
}

// PostgresDSN returns a DSN pointing at a fresh, isolated schema (created here,
// dropped on cleanup) for tests that drive the `storage use postgres` command
// with a real DSN string. Skips when PGEnv is unset.
func PostgresDSN(t *testing.T) string {
	t.Helper()
	base := os.Getenv(PGEnv)
	if base == "" {
		t.Skipf("%s not set; skipping Postgres backend test", PGEnv)
	}
	schema := "abtest_" + randHex(t)
	boot, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open bootstrap pg conn: %v", err)
	}
	if _, err := boot.Exec("CREATE SCHEMA " + schema); err != nil {
		boot.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	boot.Close()
	t.Cleanup(func() { dropSchema(base, schema) })
	return withSearchPath(t, base, schema)
}

// Backend names a store the matrix should run against.
type Backend struct {
	Name  string
	Store *store.Store
}

// Both returns the sqlite store plus the postgres store when PGEnv is set, for
// `for _, b := range storetest.Both(t)` matrix loops. Each store is freshly
// opened and cleaned up via t.Cleanup.
func Both(t *testing.T) []Backend {
	t.Helper()
	backends := []Backend{{Name: "sqlite", Store: SQLite(t)}}
	if os.Getenv(PGEnv) != "" {
		backends = append(backends, Backend{Name: "postgres", Store: Postgres(t)})
	}
	return backends
}

func withSearchPath(t *testing.T, base, schema string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
	}
	q := u.Query()
	q.Set("options", "-c search_path="+schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func dropSchema(base, schema string) {
	b, err := sql.Open("pgx", base)
	if err != nil {
		return
	}
	defer b.Close()
	_, _ = b.Exec("DROP SCHEMA " + schema + " CASCADE")
}

func randHex(t *testing.T) string {
	t.Helper()
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(buf[:])
}
