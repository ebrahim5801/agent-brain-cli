package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the pure-Go "pgx" database/sql driver
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
)

// OpenPostgres opens the local store on a user-supplied PostgreSQL database
// (Advanced mode). It pings with a short timeout so an unreachable or
// misconfigured database surfaces here, at open, rather than mid-query — every
// caller already handles a failed store.Open() (hooks fail soft, interactive
// commands fail loud). The connection pool is kept small: hooks are short-lived
// processes and the store is single-writer by design.
func OpenPostgres(dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	s := &Store{DB: db, Backend: BackendPostgres, Path: config.RedactDSN(dsn)}
	if err := migrate(s.DB, s.Backend); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
