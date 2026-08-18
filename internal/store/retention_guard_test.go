package store

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// retentionDocPath is the shared retention matrix, relative to this package.
const retentionDocPath = "../../docs/compliance/retention.md"

var (
	reCreateTableName = regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?(\w+)`)
	reDocTable        = regexp.MustCompile("^\\|\\s*`(\\w+)`\\s*\\|")
)

// retentionExempt lists client tables that need no matrix row.
var retentionExempt = map[string]bool{"schema_migrations": true}

// TestRetentionMatrixCoversClientTables is the client-side FR-031 evidence: every
// table the SQLite migrations create has a row in the shared retention matrix.
// Adding a scratch CREATE TABLE to the client migrations fails the build until it
// is documented. This is the only client-tree change in feature 017.
func TestRetentionMatrixCoversClientTables(t *testing.T) {
	created := map[string]bool{}
	for _, m := range migrations {
		for _, mt := range reCreateTableName.FindAllStringSubmatch(m, -1) {
			created[strings.ToLower(mt[1])] = true
		}
	}
	if len(created) == 0 {
		t.Fatal("no CREATE TABLE statements parsed from client migrations")
	}

	documented := map[string]bool{}
	data, err := os.ReadFile(retentionDocPath)
	if err != nil {
		t.Fatalf("read %s: %v", retentionDocPath, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if m := reDocTable.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			documented[strings.ToLower(m[1])] = true
		}
	}

	for tbl := range created {
		if retentionExempt[tbl] {
			continue
		}
		if !documented[tbl] {
			t.Errorf("client SQLite table %q has no row in %s; document its retention rule (FR-031)", tbl, retentionDocPath)
		}
	}
}
