package store

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// UpgradeMemoryV2WithBackup upgrades a SQLite store to Memory V2 after writing
// a consistent private copy of it next to the database. It returns the copy's
// path, or "" when the store was already V2 and nothing was done. PostgreSQL
// stores are refused: they may be shared with teammates whose binaries must be
// aligned first, and backing them up needs pg_dump.
func (s *Store) UpgradeMemoryV2WithBackup() (string, error) {
	if s.MemoryV2 {
		return "", nil
	}
	if s.Backend != BackendSQLite {
		return "", fmt.Errorf("the PostgreSQL store is upgraded only by hand: back it up with pg_dump, align every teammate's agent-brain, then run `agent-brain memory upgrade-v2 --apply`")
	}
	backup, err := s.backupSQLite("pre-v2")
	if err != nil {
		return "", fmt.Errorf("back up the store before the Memory V2 upgrade: %w", err)
	}
	if err := s.UpgradeMemoryV2(); err != nil {
		return backup, err
	}
	return backup, nil
}

// backupSQLite writes a consistent copy of the open SQLite store to
// <db>.<label>-<UTC time>.bak with VACUUM INTO, which reads one snapshot and so
// is safe while other processes use the database (copying the main file alone
// can miss pages still in the WAL). The copy is created empty and private
// first, because VACUUM INTO would otherwise create it with the umask's mode.
func (s *Store) backupSQLite(label string) (string, error) {
	path := fmt.Sprintf("%s.%s-%s.bak", s.Path, label, time.Now().UTC().Format("20060102-150405"))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if _, err := s.DB.Exec(`VACUUM INTO ?`, path); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// MemoryV2UpgradeNote is what a command prints after upgrading, for the
// processes that opened the store before the upgrade.
func MemoryV2UpgradeNote(backup string) string {
	lines := []string{"Memory V2 is active. Historical memory rows were not rewritten."}
	if backup != "" {
		lines = append(lines, "A copy of the store from before the upgrade is at "+backup+".")
	}
	lines = append(lines, "Restart Claude Code (or reconnect agent-brain-memory in /mcp) so open sessions use it.")
	return strings.Join(lines, "\n")
}
