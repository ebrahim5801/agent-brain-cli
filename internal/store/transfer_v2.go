package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// transferV2 holds SQLite's writer reservation through copy and verification:
// mutable capture leases/receipts cannot be missed by an insert-only delta.
func (src *Store) transferV2(dst *Store) ([]TableCount, error) {
	if !dst.MemoryV2 {
		return nil, fmt.Errorf("target needs an explicit Memory V2 additive upgrade before transfer")
	}
	tx, err := src.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var counts []TableCount
	for _, spec := range transferColumns {
		switch spec.table {
		case "memories":
			_, err = copyMemories(tx, dst, spec.cols)
		case "memory_session_bindings":
			err = copyBindings(tx, dst, spec.cols)
		default:
			_, err = copyTable(tx, dst, spec.table, spec.cols, "")
		}
		if err != nil {
			return counts, fmt.Errorf("copy %s: %w", spec.table, err)
		}
	}
	if err := resetSequences(dst); err != nil {
		return counts, err
	}
	for _, spec := range transferColumns {
		var sc, dc int64
		if err := tx.QueryRow(`SELECT COUNT(*) FROM ` + spec.table).Scan(&sc); err != nil {
			return counts, err
		}
		if err := dst.QueryRow(`SELECT COUNT(*) FROM ` + spec.table).Scan(&dc); err != nil {
			return counts, err
		}
		counts = append(counts, TableCount{Table: spec.table, SQLite: sc, Postgres: dc})
		a, err := tableDigest(tx, spec.table, spec.cols)
		if err != nil {
			return counts, err
		}
		b, err := tableDigest(dst.DB, spec.table, spec.cols)
		if err != nil {
			return counts, err
		}
		if sc != dc || a != b {
			return counts, fmt.Errorf("%s content or count mismatch; target is incomplete, source unchanged", spec.table)
		}
	}
	return counts, tx.Rollback()
}

func tableDigest(q querier, table string, cols []string) (string, error) {
	rows, err := q.Query(`SELECT ` + strings.Join(cols, ",") + ` FROM ` + table)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var digests []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range ptr {
			ptr[i] = &vals[i]
		}
		if err := rows.Scan(ptr...); err != nil {
			return "", err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		raw, err := json.Marshal(vals)
		if err != nil {
			return "", err
		}
		h := sha256.Sum256(raw)
		digests = append(digests, hex.EncodeToString(h[:]))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	sort.Strings(digests)
	h := sha256.Sum256([]byte(strings.Join(digests, "\n")))
	return hex.EncodeToString(h[:]), nil
}

func copyBindings(src *sql.Tx, dst *Store, cols []string) error {
	rows, err := src.Query(`SELECT ` + strings.Join(cols, ",") + ` FROM memory_session_bindings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	parentCol := indexOf(cols, "parent_handle")
	handleCol := indexOf(cols, "handle")
	type link struct{ child, parent string }
	var links []link
	var batch [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range ptr {
			ptr[i] = &vals[i]
		}
		if err := rows.Scan(ptr...); err != nil {
			return err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		if parent, ok := vals[parentCol].(string); ok {
			links = append(links, link{child: vals[handleCol].(string), parent: parent})
			vals[parentCol] = nil
		}
		batch = append(batch, vals)
		if len(batch) >= copyBatchRows {
			if err := insertBatch(dst, "memory_session_bindings", cols, batch); err != nil {
				return err
			}
			batch = nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(batch) > 0 {
		if err := insertBatch(dst, "memory_session_bindings", cols, batch); err != nil {
			return err
		}
	}
	for _, l := range links {
		if _, err := dst.Exec(`UPDATE memory_session_bindings SET parent_handle=? WHERE handle=?`, l.parent, l.child); err != nil {
			return err
		}
	}
	return nil
}
