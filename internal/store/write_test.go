package store

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// A directory that becomes a git repo keeps the same identity string but changes
// kind; the stored row must track the new kind (same row id) so link/sync keyed
// by identity_kind||identity don't orphan it.
func TestUpsertProjectRefreshesKindOnConflict(t *testing.T) {
	st := openTestStore(t)
	at := Now()

	dirID, err := st.UpsertProject(ProjectIdentity{Kind: "directory", Identity: "/home/u/proj", DisplayName: "proj"}, at)
	if err != nil {
		t.Fatal(err)
	}

	repoID, err := st.UpsertProject(ProjectIdentity{Kind: "repo_root", Identity: "/home/u/proj", DisplayName: "proj"}, Now())
	if err != nil {
		t.Fatal(err)
	}
	if repoID != dirID {
		t.Fatalf("expected the same project row (id %d), got %d", dirID, repoID)
	}

	var kind string
	if err := st.DB.QueryRow(`SELECT identity_kind FROM projects WHERE id = ?`, dirID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "repo_root" {
		t.Errorf("identity_kind not refreshed on conflict: got %q, want repo_root", kind)
	}
}

// A terminal server rejection must drop the entry from the pending queue yet
// stay visible in status as a permanent rejection (distinct from a hold).
func TestMarkShareRejectedIsVisibleAndDequeued(t *testing.T) {
	st := openTestStore(t)
	at := Now()
	projectID, err := st.UpsertProject(ProjectIdentity{Kind: "remote", Identity: "gitlab.com/acme/rej", DisplayName: "rej"}, at)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := st.EnsureSession("sess-rej", projectID, at, "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := st.InsertMemory(NewMemory{
		ProjectID: projectID, SessionID: sessionID,
		Content: "durable decision", Kind: "decision", Origin: "auto",
		ShareLive: true,
	}, at); err != nil {
		t.Fatal(err)
	}

	pending, err := st.PendingContributions(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 entry queued for contribution, got %d", len(pending))
	}
	uid := pending[0].UID

	if err := st.MarkShareRejected(uid, "rejected by the server (invalid) and will not be shared", Now()); err != nil {
		t.Fatal(err)
	}

	errs, err := st.ShareErrors(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 1 {
		t.Fatalf("expected the rejection to be visible in status, got %d entries", len(errs))
	}

	// Dequeued: it must no longer be eligible for contribution.
	stillPending, err := st.PendingContributions(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stillPending) != 0 {
		t.Errorf("terminally rejected entry is still queued for contribution (%d pending)", len(stillPending))
	}
}
