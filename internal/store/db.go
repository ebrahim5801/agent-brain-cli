package store

import (
	"database/sql"
	"errors"
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
	MemoryV2 bool // approved additive schema exists; never inferred from configuration
	DB       *sql.DB
	Backend  Backend
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Create new stores privately, including the WAL's inherited mode. Leave
	// permissions of an existing user-managed database unchanged.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if !os.IsExist(err) {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)&_txlock=immediate", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite serializes writers; a single connection avoids in-process lock churn.
	db.SetMaxOpenConns(1)
	if err := ensureWAL(db); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db, Backend: BackendSQLite, Path: path}
	if err := migrate(s.DB, s.Backend); err != nil {
		db.Close()
		return nil, err
	}
	v, err := s.SchemaVersion()
	if err != nil {
		db.Close()
		return nil, err
	}
	s.MemoryV2 = v >= 16
	return s, nil
}

// ensureWAL puts the database in WAL mode. The mode is stored in the file, so
// it is set here once rather than as a per-connection pragma: switching modes
// needs an exclusive lock, and SQLite answers SQLITE_BUSY at once instead of
// waiting out busy_timeout when another process holds the database, which
// several hooks starting together do. Until the switch succeeds the store
// works in rollback-journal mode, so a database that stays busy is not an error.
func ensureWAL(db *sql.DB) error {
	for attempt := 0; ; attempt++ {
		var mode string
		err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
		if err == nil && strings.EqualFold(mode, "wal") {
			return nil
		}
		if err == nil {
			err = db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode)
			if err == nil && strings.EqualFold(mode, "wal") {
				return nil
			}
		}
		if err != nil && !isBusy(err) {
			return err
		}
		if attempt == 20 {
			return nil
		}
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
}

// isBusy reports SQLITE_BUSY, including its extended codes.
func isBusy(err error) bool {
	var coded interface{ Code() int }
	return errors.As(err, &coded) && coded.Code()&0xff == 5
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
