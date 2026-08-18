package store_test

import (
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func seedUsageSession(t *testing.T, st *store.Store) (projectID, sessionID int64) {
	t.Helper()
	var err error
	projectID, err = st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/usage", DisplayName: "usage"}, store.Now())
	if err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	sessionID, err = st.EnsureSession("usage-sess-1", projectID, store.Now(), "", "claude-code")
	if err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	return projectID, sessionID
}

func clearSessionDirty(t *testing.T, st *store.Store, sessionID int64) {
	t.Helper()
	if _, err := st.Exec(`UPDATE sessions SET sync_dirty = 0 WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("clear session dirty: %v", err)
	}
}

func sessionDirty(t *testing.T, st *store.Store, sessionID int64) bool {
	t.Helper()
	var dirty int
	if err := st.QueryRow(`SELECT sync_dirty FROM sessions WHERE id = ?`, sessionID).Scan(&dirty); err != nil {
		t.Fatalf("read session dirty: %v", err)
	}
	return dirty == 1
}

func TestRecordRetrievalUpsertIncrementsAndDirtiesSession(t *testing.T) {
	for _, b := range storetest.Both(t) {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			projectID, sessionID := seedUsageSession(t, st)

			if err := st.RecordRetrieval(sessionID, projectID, "personal", 42, "", "2026-07-18T00:00:00.000Z"); err != nil {
				t.Fatalf("first retrieval: %v", err)
			}
			clearSessionDirty(t, st, sessionID)

			if err := st.RecordRetrieval(sessionID, projectID, "personal", 42, "", "2026-07-18T00:05:00.000Z"); err != nil {
				t.Fatalf("second retrieval: %v", err)
			}

			var retrieved int64
			var firstAt, lastAt string
			var dirty int
			err := st.QueryRow(`
                SELECT retrieved, first_at, last_at, sync_dirty FROM memory_usage_events
                WHERE session_id = ? AND scope = 'personal' AND memory_id = 42`, sessionID).
				Scan(&retrieved, &firstAt, &lastAt, &dirty)
			if err != nil {
				t.Fatalf("read usage row: %v", err)
			}
			if retrieved != 2 {
				t.Errorf("retrieved = %d, want 2", retrieved)
			}
			if firstAt != "2026-07-18T00:00:00.000Z" {
				t.Errorf("first_at = %q, want unchanged initial timestamp", firstAt)
			}
			if lastAt != "2026-07-18T00:05:00.000Z" {
				t.Errorf("last_at = %q, want bumped to second timestamp", lastAt)
			}
			if dirty != 1 {
				t.Error("usage row sync_dirty not set on the second retrieval")
			}
			if !sessionDirty(t, st, sessionID) {
				t.Error("owning session not marked sync_dirty by RecordRetrieval")
			}
		})
	}
}

func TestRecordRetrievalPersonalAndTeamCoexist(t *testing.T) {
	for _, b := range storetest.Both(t) {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			projectID, sessionID := seedUsageSession(t, st)

			if err := st.RecordRetrieval(sessionID, projectID, "personal", 7, "", store.Now()); err != nil {
				t.Fatalf("personal retrieval: %v", err)
			}
			if err := st.RecordRetrieval(sessionID, projectID, "team", 0, "team-uid-1", store.Now()); err != nil {
				t.Fatalf("team retrieval: %v", err)
			}

			refs, err := st.SessionRetrievedRefs(sessionID)
			if err != nil {
				t.Fatalf("session retrieved refs: %v", err)
			}
			if len(refs) != 2 {
				t.Fatalf("refs = %+v, want 2 distinct rows", refs)
			}
			var sawPersonal, sawTeam bool
			for _, r := range refs {
				if r.Scope == "personal" && r.MemoryID == 7 && r.TeamUID == "" {
					sawPersonal = true
				}
				if r.Scope == "team" && r.MemoryID == 0 && r.TeamUID == "team-uid-1" {
					sawTeam = true
				}
			}
			if !sawPersonal || !sawTeam {
				t.Errorf("refs = %+v, missing expected personal/team rows", refs)
			}
		})
	}
}

func TestMarkCitedOnlyFlipsExistingRetrievalRows(t *testing.T) {
	for _, b := range storetest.Both(t) {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			projectID, sessionID := seedUsageSession(t, st)

			// No retrieval exists for id 99 yet: MarkCited must not create one.
			if err := st.MarkCited(sessionID, "personal", 99, "", store.Now()); err != nil {
				t.Fatalf("mark cited on unknown ref: %v", err)
			}
			var count int
			if err := st.QueryRow(`SELECT COUNT(*) FROM memory_usage_events WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("MarkCited created a row for a never-retrieved ref: count=%d", count)
			}

			if err := st.RecordRetrieval(sessionID, projectID, "personal", 99, "", store.Now()); err != nil {
				t.Fatalf("retrieve 99: %v", err)
			}
			clearSessionDirty(t, st, sessionID)

			if err := st.MarkCited(sessionID, "personal", 99, "", store.Now()); err != nil {
				t.Fatalf("mark cited: %v", err)
			}
			var cited int
			if err := st.QueryRow(`SELECT cited FROM memory_usage_events WHERE session_id = ? AND memory_id = 99`, sessionID).Scan(&cited); err != nil {
				t.Fatal(err)
			}
			if cited != 1 {
				t.Errorf("cited = %d, want 1", cited)
			}
			if !sessionDirty(t, st, sessionID) {
				t.Error("owning session not marked sync_dirty by MarkCited")
			}
		})
	}
}
