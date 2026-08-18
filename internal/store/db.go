package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	_ "modernc.org/sqlite"
)

// TimeLayout is fixed-width UTC so timestamps sort lexicographically.
const TimeLayout = "2006-01-02T15:04:05.000Z"

func Now() string {
	return time.Now().UTC().Format(TimeLayout)
}

// Backend identifies which SQL dialect the store speaks. SQLite is the default
// (Basic mode); Postgres is the opt-in Advanced mode.
type Backend string

const (
	BackendSQLite   Backend = "sqlite"
	BackendPostgres Backend = "postgres"
)

type Store struct {
	DB      *sql.DB
	Backend Backend
	// Path is the SQLite file path, or the redacted DSN for display on Postgres.
	Path string
}

// Open resolves the backend and opens the local store:
//  1. AGENT_BRAIN_PG_DSN env var → Postgres with that DSN (keeps credentials
//     out of the config file; also how tests select Postgres).
//  2. config storage.backend == "postgres" → Postgres with storage.dsn.
//  3. otherwise → SQLite at DBPath(), exactly as Basic mode always has.
//
// There is deliberately no silent fallback to SQLite when a configured Postgres
// is unreachable: that would fork the data. An unreachable Postgres is an error
// the caller handles (hooks fail soft, interactive commands fail loud).
func Open() (*Store, error) {
	if dsn := os.Getenv("AGENT_BRAIN_PG_DSN"); dsn != "" {
		return OpenPostgres(dsn)
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.Storage != nil && cfg.Storage.Backend == string(BackendPostgres) {
		// An empty DSN would make pgx fall back to libpq defaults (localhost,
		// OS user) — a different database than the user configured, which is
		// exactly the data fork Open must never allow.
		if strings.TrimSpace(cfg.Storage.DSN) == "" {
			return nil, fmt.Errorf("config selects the postgres backend but storage.dsn is empty — run: agent-brain storage use postgres --dsn <dsn>")
		}
		return OpenPostgres(cfg.Storage.DSN)
	}
	path, err := DBPath()
	if err != nil {
		return nil, err
	}
	return OpenAt(path)
}

func OpenAt(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite serializes writers; a single connection avoids in-process lock churn.
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, Backend: BackendSQLite, Path: path}
	if err := migrate(s.DB, s.Backend); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}

// Rebind rewrites a `?`-placeholder query into this store's dialect. Exported
// for transactional call sites that execute against a *sql.Tx (which the
// wrapper methods below cannot reach): `tx.Exec(s.Rebind(q), ...)`.
func (s *Store) Rebind(query string) string {
	return rebind(s.Backend, query)
}

// Exec/Query/QueryRow are the single SQL execution funnel: every local-store
// query goes through them so the `?`→`$n` rewrite for Postgres happens in one
// place. On SQLite the rebind is the identity, so there is no hot-path cost.
func (s *Store) Exec(query string, args ...any) (sql.Result, error) {
	return s.DB.Exec(s.Rebind(query), normalizeArgs(args)...)
}

func (s *Store) Query(query string, args ...any) (*sql.Rows, error) {
	return s.DB.Query(s.Rebind(query), normalizeArgs(args)...)
}

func (s *Store) QueryRow(query string, args ...any) *sql.Row {
	return s.DB.QueryRow(s.Rebind(query), normalizeArgs(args)...)
}

// ExecTx rebinds and normalizes args for a query run inside a caller-managed
// transaction (the wrappers above operate on *sql.DB and cannot reach a
// *sql.Tx). Use it wherever a transactional statement binds a bool.
func (s *Store) ExecTx(tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	return tx.Exec(s.Rebind(query), normalizeArgs(args)...)
}
