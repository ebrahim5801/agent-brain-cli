package store_test

import (
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

// Migration 14's columns must exist and behave identically on both backends:
// the count defaults to 0 (NOT NULL) and the deadline is nullable, since the
// syncer distinguishes "never rejected" from "deferred until T".
func TestSyncRejectColumnsMatchAcrossBackends(t *testing.T) {
	for _, b := range storetest.Both(t) {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			at := store.Now()
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "dir", Identity: "/p", DisplayName: "p"}, at)
			if err != nil {
				t.Fatal(err)
			}
			sid, err := st.EnsureSession("s-1", pid, at, "", "claude-code")
			if err != nil {
				t.Fatal(err)
			}

			var count int64
			var retryAfter *string
			if err := st.DB.QueryRow(st.Rebind(
				`SELECT sync_reject_count, sync_retry_after FROM sessions WHERE id = ?`), sid).Scan(&count, &retryAfter); err != nil {
				t.Fatalf("select reject columns: %v", err)
			}
			if count != 0 {
				t.Fatalf("sync_reject_count default = %d, want 0", count)
			}
			if retryAfter != nil {
				t.Fatalf("sync_retry_after default = %v, want NULL", *retryAfter)
			}

			deadline := store.Now()
			if _, err := st.Exec(
				`UPDATE sessions SET sync_reject_count = 3, sync_retry_after = ? WHERE id = ?`, deadline, sid); err != nil {
				t.Fatalf("update reject columns: %v", err)
			}
			if err := st.DB.QueryRow(st.Rebind(
				`SELECT sync_reject_count, sync_retry_after FROM sessions WHERE id = ?`), sid).Scan(&count, &retryAfter); err != nil {
				t.Fatal(err)
			}
			if count != 3 || retryAfter == nil || *retryAfter != deadline {
				t.Fatalf("round-trip = %d/%v, want 3/%s", count, retryAfter, deadline)
			}

			// The partial index only helps if the comparison the outbox makes
			// is a plain string ordering on both dialects.
			var due int64
			if err := st.DB.QueryRow(st.Rebind(
				`SELECT COUNT(*) FROM sessions WHERE sync_retry_after IS NULL OR sync_retry_after <= ?`), deadline).Scan(&due); err != nil {
				t.Fatalf("due query: %v", err)
			}
			if due != 1 {
				t.Fatalf("due count = %d, want 1", due)
			}
		})
	}
}
