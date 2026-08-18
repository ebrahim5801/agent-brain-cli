// Package diag is the collector's never-fail error sink. Logging must never
// panic, block, or surface an error to the caller: a diagnostics failure in
// the hook path would otherwise disturb the assistant session it observes.
package diag

import (
	"database/sql"
	"fmt"
	"os"
	"time"
)

const keepEntries = 200

type Logger struct {
	DB *sql.DB
	// Rebind rewrites `?` placeholders into the store's dialect. Nil means the
	// SQLite identity (the DELETE below uses LIMIT inside a subquery, which
	// Postgres accepts, so only the placeholders differ across backends).
	Rebind       func(string) string
	FallbackPath string
}

func (l *Logger) rebind(q string) string {
	if l != nil && l.Rebind != nil {
		return l.Rebind(q)
	}
	return q
}

func (l *Logger) Log(component, format string, args ...any) {
	defer func() { _ = recover() }()
	msg := fmt.Sprintf(format, args...)
	at := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if l != nil && l.DB != nil {
		_, err := l.DB.Exec(l.rebind(`INSERT INTO diagnostics (occurred_at, component, message) VALUES (?, ?, ?)`), at, component, msg)
		if err == nil {
			_, _ = l.DB.Exec(l.rebind(`DELETE FROM diagnostics WHERE id NOT IN (SELECT id FROM diagnostics ORDER BY id DESC LIMIT ?)`), keepEntries)
			return
		}
	}
	if l != nil && l.FallbackPath != "" {
		if f, err := os.OpenFile(l.FallbackPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "%s %s: %s\n", at, component, msg)
			f.Close()
		}
	}
}
