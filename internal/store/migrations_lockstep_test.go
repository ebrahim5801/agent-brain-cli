package store

import (
	"path/filepath"
	"testing"
)

// TestMigrationSlicesLockstep guards the "append to both slices together" rule.
// The Postgres baseline collapses SQLite versions 1..sqliteBaselineVersions into
// one DDL; from there every new migration is appended to both `migrations` and
// `migrationsPostgres`. So the number of SQLite migrations must always equal the
// baseline count plus the number of appended Postgres migrations — otherwise the
// two backends describe different schema versions.
func TestMigrationSlicesLockstep(t *testing.T) {
	if got, want := len(migrations), sqliteBaselineVersions+len(migrationsPostgres); got != want {
		t.Fatalf("migration slices out of lockstep: len(migrations)=%d, want sqliteBaselineVersions(%d)+len(migrationsPostgres)(%d)=%d\n"+
			"append every new migration to BOTH slices (see migrations_postgres.go)",
			got, sqliteBaselineVersions, len(migrationsPostgres), want)
	}
}

// TestMigrationStepsAlign checks that the per-backend step lists a shared runner
// walks end at the same schema version — the numbering invariant migrate() relies
// on when it records the Postgres baseline as versions 1..sqliteBaselineVersions.
func TestMigrationStepsAlign(t *testing.T) {
	sqliteSteps := migrationSteps(BackendSQLite)
	pgSteps := migrationSteps(BackendPostgres)

	lastVersion := func(steps []migrationStep) int {
		max := 0
		for _, s := range steps {
			for _, v := range s.versions {
				if v > max {
					max = v
				}
			}
		}
		return max
	}
	if sv, pv := lastVersion(sqliteSteps), lastVersion(pgSteps); sv != pv {
		t.Fatalf("backends reach different top schema version: sqlite=%d postgres=%d", sv, pv)
	}

	// The baseline step must record exactly versions 1..sqliteBaselineVersions.
	baseline := pgSteps[0]
	if len(baseline.versions) != sqliteBaselineVersions {
		t.Fatalf("baseline records %d versions, want %d", len(baseline.versions), sqliteBaselineVersions)
	}
	for i, v := range baseline.versions {
		if v != i+1 {
			t.Fatalf("baseline versions not 1..%d: got %v", sqliteBaselineVersions, baseline.versions)
		}
	}
}

// TestIdentityTablesTransferred guards the transfer against a table added to
// identityTables but forgotten in transferColumns: the delta pass would call
// copyTable with nil columns and panic, and the table's rows would never be
// copied on a storage switch.
func TestIdentityTablesTransferred(t *testing.T) {
	for _, table := range identityTables {
		if columnsFor(table) == nil {
			t.Errorf("identity table %q missing from transferColumns — its rows would be lost on migration", table)
		}
	}
}

// TestTransferColumnsCoverSchema guards against a column added to a table by a
// migration but forgotten in transferColumns: a storage switch would silently
// drop it, taking the data with it.
func TestTransferColumnsCoverSchema(t *testing.T) {
	st, err := OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, spec := range transferColumns {
		rows, err := st.DB.Query(`SELECT name FROM pragma_table_info(?)`, spec.table)
		if err != nil {
			t.Fatalf("table info %s: %v", spec.table, err)
		}
		declared := map[string]bool{}
		for _, c := range spec.cols {
			declared[c] = true
		}
		for rows.Next() {
			var col string
			if err := rows.Scan(&col); err != nil {
				t.Fatal(err)
			}
			if !declared[col] {
				t.Errorf("%s.%s missing from transferColumns — it would be dropped on a storage switch", spec.table, col)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
}
