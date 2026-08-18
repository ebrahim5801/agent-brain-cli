package store

import (
	"path/filepath"
	"testing"
)

func openTeamStore(t *testing.T) (*Store, int64) {
	t.Helper()
	st, err := OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pid, err := st.UpsertProject(ProjectIdentity{Kind: "remote", Identity: "gitlab.com/acme/team", DisplayName: "team"}, Now())
	if err != nil {
		t.Fatal(err)
	}
	return st, pid
}

func TestResolveTeamHandle(t *testing.T) {
	st, pid := openTeamStore(t)
	if err := st.ApplyPull(pid, []TeamMemoryRow{
		{UID: "abc12345deadbeef", Author: "a@example.com", Content: "x", Kind: "fact", Origin: "auto", Status: "active", CapturedAt: "2026-07-05T10:00:00.000Z", UpdatedAt: "2026-07-05T10:00:00.000Z"},
		{UID: "abc12399feedface", Author: "b@example.com", Content: "y", Kind: "fact", Origin: "auto", Status: "active", CapturedAt: "2026-07-05T10:01:00.000Z", UpdatedAt: "2026-07-05T10:01:00.000Z"},
		{UID: "0f0f0f0fcafecafe", Author: "c@example.com", Content: "z", Kind: "fact", Origin: "auto", Status: "active", CapturedAt: "2026-07-05T10:02:00.000Z", UpdatedAt: "2026-07-05T10:02:00.000Z"},
	}); err != nil {
		t.Fatal(err)
	}

	uid, err := st.ResolveTeamHandle(pid, "0f0f0f0f")
	if err != nil || uid != "0f0f0f0fcafecafe" {
		t.Errorf("unique prefix: uid=%q err=%v", uid, err)
	}
	if _, err := st.ResolveTeamHandle(pid, "nomatch"); err != ErrMemoryNotFound {
		t.Errorf("missing prefix: err=%v, want ErrMemoryNotFound", err)
	}
	if _, err := st.ResolveTeamHandle(pid, "abc123"); err != ErrAmbiguousTeamHandle {
		t.Errorf("ambiguous prefix: err=%v, want ErrAmbiguousTeamHandle", err)
	}
}

func TestApplyPullUpsertAndTombstone(t *testing.T) {
	st, pid := openTeamStore(t)

	rows := []TeamMemoryRow{
		{UID: "u1", Author: "a@example.com", Content: "first", Kind: "decision", Origin: "auto", Status: "active", Branch: "main", CommitHash: "abc", CapturedAt: "2026-07-05T10:00:00.000Z", UpdatedAt: "2026-07-05T10:00:00.000Z"},
		{UID: "u2", Author: "b@example.com", Content: "second", Kind: "fact", Origin: "auto", Status: "active", CapturedAt: "2026-07-05T10:01:00.000Z", UpdatedAt: "2026-07-05T10:01:00.000Z"},
	}
	if err := st.ApplyPull(pid, rows); err != nil {
		t.Fatal(err)
	}
	active, err := st.ListTeamMemories(pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("active = %d, want 2", len(active))
	}
	// Newest-first: u2 captured later.
	if active[0].UID != "u2" || active[1].UID != "u1" {
		t.Errorf("order = %s,%s", active[0].UID, active[1].UID)
	}
	if active[1].Branch != "main" || active[1].CommitHash != "abc" {
		t.Errorf("provenance lost: %+v", active[1])
	}

	// Update u1 to superseded, tombstone u2.
	update := []TeamMemoryRow{
		{UID: "u1", Author: "a@example.com", Content: "first", Kind: "decision", Origin: "auto", Status: "superseded", CapturedAt: "2026-07-05T10:00:00.000Z", UpdatedAt: "2026-07-05T10:05:00.000Z"},
		{UID: "u2", Content: "", Status: "deleted", CapturedAt: "2026-07-05T10:01:00.000Z", UpdatedAt: "2026-07-05T10:06:00.000Z"},
	}
	if err := st.ApplyPull(pid, update); err != nil {
		t.Fatal(err)
	}
	active, _ = st.ListTeamMemories(pid)
	if len(active) != 0 {
		t.Errorf("active after supersede+delete = %d, want 0", len(active))
	}
	var count int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM team_memories WHERE project_id = ?`, pid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("rows = %d, want 1 (u1 superseded kept, u2 tombstone dropped)", count)
	}
}

func TestPullCursorRoundTrip(t *testing.T) {
	st, pid := openTeamStore(t)
	cursor, err := st.PullCursor(pid)
	if err != nil || cursor != "" {
		t.Fatalf("default cursor = %q, %v", cursor, err)
	}
	if err := st.SetPullCursor(pid, "2026-07-05T10:00:00Z|42"); err != nil {
		t.Fatal(err)
	}
	if cursor, _ = st.PullCursor(pid); cursor != "2026-07-05T10:00:00Z|42" {
		t.Errorf("cursor = %q", cursor)
	}
	if err := st.SetPullCursor(pid, "next"); err != nil {
		t.Fatal(err)
	}
	if cursor, _ = st.PullCursor(pid); cursor != "next" {
		t.Errorf("cursor after update = %q", cursor)
	}
}

func TestDropTeamCache(t *testing.T) {
	st, pid := openTeamStore(t)
	_ = st.ApplyPull(pid, []TeamMemoryRow{{UID: "u1", Author: "a", Content: "x", Kind: "fact", Origin: "auto", Status: "active", CapturedAt: "2026-07-05T10:00:00.000Z", UpdatedAt: "2026-07-05T10:00:00.000Z"}})
	_ = st.SetPullCursor(pid, "c1")
	if err := st.DropTeamCache(pid); err != nil {
		t.Fatal(err)
	}
	active, _ := st.ListTeamMemories(pid)
	if len(active) != 0 {
		t.Errorf("cache not dropped: %d rows", len(active))
	}
	if cursor, _ := st.PullCursor(pid); cursor != "" {
		t.Errorf("cursor not reset: %q", cursor)
	}
}
