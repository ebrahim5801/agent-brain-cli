package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func openMemStore(t *testing.T) (*Store, int64, int64) {
	t.Helper()
	st, err := OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	at := Now()
	projectID, err := st.UpsertProject(ProjectIdentity{Kind: "remote", Identity: "gitlab.com/acme/mem", DisplayName: "mem"}, at)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := st.EnsureSession("sess-mem-ops", projectID, at, "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	return st, projectID, sessionID
}

func insert(t *testing.T, st *Store, projectID, sessionID int64, content string) int64 {
	t.Helper()
	id, err := st.InsertMemory(NewMemory{
		ProjectID: projectID, SessionID: sessionID, Content: content,
		Kind: "decision", Origin: "explicit", Branch: "main", Commit: "abc123", HasRepo: true,
	}, Now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPendingContributionsUnionsTeamSupersedes(t *testing.T) {
	st, projectID, sessionID := openMemStore(t)
	// A shareable entry (team_uid minted) that supersedes a cached team entry.
	id, err := st.InsertMemory(NewMemory{
		ProjectID: projectID, SessionID: sessionID, Content: "replaces a teammate's entry",
		Kind: "decision", Origin: "auto", ShareLive: true,
	}, Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddTeamSupersede(id, "teammate-team-uid"); err != nil {
		t.Fatal(err)
	}
	// Idempotent per pair.
	if err := st.AddTeamSupersede(id, "teammate-team-uid"); err != nil {
		t.Fatal(err)
	}

	pending, err := st.PendingContributions(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if len(pending[0].Supersedes) != 1 || pending[0].Supersedes[0] != "teammate-team-uid" {
		t.Errorf("Supersedes = %v, want [teammate-team-uid]", pending[0].Supersedes)
	}
}

func TestInsertAndGetMemory(t *testing.T) {
	st, projectID, sessionID := openMemStore(t)
	id := insert(t, st, projectID, sessionID, "use pgx directly")

	m, err := st.GetMemory(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Content != "use pgx directly" || m.Status != MemoryActive || m.Kind != "decision" {
		t.Errorf("memory = %+v", m)
	}
	if !m.Branch.Valid || m.Branch.String != "main" || m.CommitHash.String != "abc123" {
		t.Errorf("capture state = %v %v", m.Branch, m.CommitHash)
	}
	if !m.SessionID.Valid || m.SessionID.Int64 != sessionID {
		t.Errorf("session = %v", m.SessionID)
	}
}

func TestInsertMemoryNoRepoNoSession(t *testing.T) {
	st, projectID, _ := openMemStore(t)
	id, err := st.InsertMemory(NewMemory{
		ProjectID: projectID, Content: "fact", Kind: "fact", Origin: "auto",
	}, Now())
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.GetMemory(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Branch.Valid || m.CommitHash.Valid || m.SessionID.Valid {
		t.Errorf("want NULL branch/commit/session, got %+v", m)
	}
}

func TestSupersedeAndRestore(t *testing.T) {
	st, projectID, sessionID := openMemStore(t)
	oldID := insert(t, st, projectID, sessionID, "REST API")
	newID := insert(t, st, projectID, sessionID, "event-driven now")

	if err := st.SupersedeMemory(oldID, newID, projectID, Now()); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetMemory(oldID)
	if m.Status != MemorySuperseded || m.SupersededBy.Int64 != newID {
		t.Errorf("superseded = %+v", m)
	}

	active, err := st.ListMemories(projectID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != newID {
		t.Errorf("active list = %+v", active)
	}
	all, _ := st.ListMemories(projectID, true)
	if len(all) != 2 || all[0].Status != MemoryActive {
		t.Errorf("full list = %+v", all)
	}

	// Superseding an already-superseded entry is rejected.
	if err := st.SupersedeMemory(oldID, newID, projectID, Now()); !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("double supersede err = %v", err)
	}
	// Cross-project supersede is rejected.
	otherProject, _ := st.UpsertProject(ProjectIdentity{Kind: "remote", Identity: "gitlab.com/acme/other", DisplayName: "other"}, Now())
	otherID := insert(t, st, otherProject, 0, "other project entry")
	if err := st.SupersedeMemory(otherID, newID, projectID, Now()); !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("cross-project supersede err = %v", err)
	}

	if err := st.RestoreMemory(oldID, Now()); err != nil {
		t.Fatal(err)
	}
	m, _ = st.GetMemory(oldID)
	if m.Status != MemoryActive || m.SupersededBy.Valid {
		t.Errorf("restored = %+v", m)
	}
}

func TestUpdateDeleteWipe(t *testing.T) {
	st, projectID, sessionID := openMemStore(t)
	id := insert(t, st, projectID, sessionID, "before")
	id2 := insert(t, st, projectID, sessionID, "second")

	if err := st.UpdateMemoryContent(id, "after", Now()); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetMemory(id)
	if m.Content != "after" || !m.Edited {
		t.Errorf("edited = %+v", m)
	}
	if m.CommitHash.String != "abc123" {
		t.Error("edit altered provenance")
	}

	// Delete is soft: the row is retained (restorable) but no longer served.
	if err := st.DeleteMemory(id, Now()); err != nil {
		t.Fatal(err)
	}
	deleted, err := st.GetMemory(id)
	if err != nil {
		t.Fatalf("soft-deleted entry should still be retrievable: %v", err)
	}
	if deleted.Status != MemoryDeleted {
		t.Errorf("status = %q, want %q", deleted.Status, MemoryDeleted)
	}
	if containsID(listActive(t, st, projectID), id) {
		t.Error("deleted entry still served in active list")
	}
	// Re-deleting an already-deleted entry affects nothing.
	if err := st.DeleteMemory(id, Now()); !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("double delete err = %v", err)
	}
	// Restore brings it back to active.
	if err := st.RestoreMemory(id, Now()); err != nil {
		t.Fatalf("restore soft-deleted entry: %v", err)
	}
	if m, _ := st.GetMemory(id); m.Status != MemoryActive {
		t.Errorf("after restore status = %q, want active", m.Status)
	}

	// Wipe soft-deletes every still-active entry; they remain retrievable.
	n, err := st.WipeProjectMemories(projectID, Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("wiped %d, want 2 (id restored + id2)", n)
	}
	if got := listActive(t, st, projectID); len(got) != 0 {
		t.Errorf("wipe left %d active entries", len(got))
	}
	if _, err := st.GetMemory(id2); err != nil {
		t.Error("wipe physically removed an entry; it should be soft-deleted")
	}
}

func listActive(t *testing.T, st *Store, projectID int64) []Memory {
	t.Helper()
	ms, err := st.ListMemories(projectID, false)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func containsID(ms []Memory, id int64) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}

func TestProjectMemoryDisabledFlag(t *testing.T) {
	st, projectID, _ := openMemStore(t)
	disabled, err := st.ProjectMemoryDisabled(projectID)
	if err != nil || disabled {
		t.Fatalf("default disabled = %v, %v", disabled, err)
	}
	if err := st.SetProjectMemoryDisabled(projectID, true); err != nil {
		t.Fatal(err)
	}
	if disabled, _ = st.ProjectMemoryDisabled(projectID); !disabled {
		t.Error("disable did not stick")
	}
	// Unknown project reads as not disabled rather than erroring.
	if disabled, err = st.ProjectMemoryDisabled(99999); err != nil || disabled {
		t.Errorf("unknown project = %v, %v", disabled, err)
	}
}

func TestShareQueueMintingAndMarkShared(t *testing.T) {
	st, projectID, sessionID := openMemStore(t)

	// Sharing not live → no team_uid, never queued.
	_, err := st.InsertMemory(NewMemory{ProjectID: projectID, SessionID: sessionID, Content: "not shared", Kind: "fact", Origin: "auto"}, Now())
	if err != nil {
		t.Fatal(err)
	}
	// personal_only overrides a live share → no team_uid.
	_, err = st.InsertMemory(NewMemory{ProjectID: projectID, SessionID: sessionID, Content: "private", Kind: "fact", Origin: "auto", ShareLive: true, PersonalOnly: true}, Now())
	if err != nil {
		t.Fatal(err)
	}
	// Live share → minted and queued.
	sharedID, err := st.InsertMemory(NewMemory{ProjectID: projectID, SessionID: sessionID, Content: "shared decision", Kind: "decision", Origin: "auto", Branch: "main", Commit: "abc", HasRepo: true, ShareLive: true}, Now())
	if err != nil {
		t.Fatal(err)
	}

	pending, err := st.PendingContributions(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Content != "shared decision" {
		t.Fatalf("pending = %+v", pending)
	}
	if !pending[0].Branch.Valid || pending[0].Branch.String != "main" {
		t.Errorf("pending provenance = %+v", pending[0])
	}
	uid := pending[0].UID
	if uid == "" {
		t.Fatal("no team_uid minted")
	}

	if err := st.MarkShared(uid, Now()); err != nil {
		t.Fatal(err)
	}
	pending, _ = st.PendingContributions(projectID)
	if len(pending) != 0 {
		t.Errorf("pending after ack = %d, want 0", len(pending))
	}
	_ = sharedID
}

func TestPendingContributionCarriesSupersedesUID(t *testing.T) {
	st, projectID, sessionID := openMemStore(t)

	oldID, err := st.InsertMemory(NewMemory{ProjectID: projectID, SessionID: sessionID, Content: "old", Kind: "decision", Origin: "auto", ShareLive: true}, Now())
	if err != nil {
		t.Fatal(err)
	}
	// Old was shared already.
	oldPending, _ := st.PendingContributions(projectID)
	oldUID := oldPending[0].UID
	if err := st.MarkShared(oldUID, Now()); err != nil {
		t.Fatal(err)
	}

	newID, err := st.InsertMemory(NewMemory{ProjectID: projectID, SessionID: sessionID, Content: "new", Kind: "decision", Origin: "auto", ShareLive: true}, Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SupersedeMemory(oldID, newID, projectID, Now()); err != nil {
		t.Fatal(err)
	}

	pending, _ := st.PendingContributions(projectID)
	if len(pending) != 1 || pending[0].Content != "new" {
		t.Fatalf("pending = %+v", pending)
	}
	if len(pending[0].Supersedes) != 1 || pending[0].Supersedes[0] != oldUID {
		t.Errorf("supersedes = %+v, want [%q]", pending[0].Supersedes, oldUID)
	}
}

func TestPendingContributionCarriesMultipleSupersedes(t *testing.T) {
	st, projectID, sessionID := openMemStore(t)

	old1, err := st.InsertMemory(NewMemory{ProjectID: projectID, SessionID: sessionID, Content: "old1", Kind: "decision", Origin: "auto", ShareLive: true}, Now())
	if err != nil {
		t.Fatal(err)
	}
	old2, err := st.InsertMemory(NewMemory{ProjectID: projectID, SessionID: sessionID, Content: "old2", Kind: "decision", Origin: "auto", ShareLive: true}, Now())
	if err != nil {
		t.Fatal(err)
	}
	pend, _ := st.PendingContributions(projectID)
	uids := map[string]string{}
	for _, p := range pend {
		uids[p.Content] = p.UID
		_ = st.MarkShared(p.UID, Now())
	}

	newID, err := st.InsertMemory(NewMemory{ProjectID: projectID, SessionID: sessionID, Content: "new", Kind: "decision", Origin: "auto", ShareLive: true}, Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SupersedeMemory(old1, newID, projectID, Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SupersedeMemory(old2, newID, projectID, Now()); err != nil {
		t.Fatal(err)
	}

	pending, _ := st.PendingContributions(projectID)
	if len(pending) != 1 || len(pending[0].Supersedes) != 2 {
		t.Fatalf("want one pending with two supersedes, got %+v", pending)
	}
	got := map[string]bool{}
	for _, u := range pending[0].Supersedes {
		got[u] = true
	}
	if !got[uids["old1"]] || !got[uids["old2"]] {
		t.Errorf("supersedes = %v, want both %q and %q", pending[0].Supersedes, uids["old1"], uids["old2"])
	}
}

func TestMarkSessionDistilledOnce(t *testing.T) {
	st, _, sessionID := openMemStore(t)
	first, err := st.MarkSessionDistilled(sessionID, Now())
	if err != nil || !first {
		t.Fatalf("first mark = %v, %v", first, err)
	}
	second, err := st.MarkSessionDistilled(sessionID, Now())
	if err != nil || second {
		t.Errorf("second mark = %v, %v (must lose the race)", second, err)
	}
}

func TestMarkSessionDistilledRearmsAfterPrompt(t *testing.T) {
	st, _, sessionID := openMemStore(t)
	first, err := st.MarkSessionDistilled(sessionID, "2026-01-01T00:00:00.000Z")
	if err != nil || !first {
		t.Fatalf("first mark = %v, %v", first, err)
	}

	if err := st.AppendEvent(sessionID, "tool_use", "Edit", "2026-01-01T00:00:01.000Z"); err != nil {
		t.Fatal(err)
	}
	again, err := st.MarkSessionDistilled(sessionID, "2026-01-01T00:00:02.000Z")
	if err != nil || again {
		t.Errorf("mark after tool_use only = %v, %v (must not re-arm)", again, err)
	}

	if err := st.AppendEvent(sessionID, "prompt", "", "2026-01-01T00:00:03.000Z"); err != nil {
		t.Fatal(err)
	}
	rearmed, err := st.MarkSessionDistilled(sessionID, "2026-01-01T00:00:04.000Z")
	if err != nil || !rearmed {
		t.Fatalf("mark after new prompt = %v, %v (must re-arm)", rearmed, err)
	}

	final, err := st.MarkSessionDistilled(sessionID, "2026-01-01T00:00:05.000Z")
	if err != nil || final {
		t.Errorf("mark with no newer prompt = %v, %v (must lose)", final, err)
	}
}

func TestMarkSessionPromptInjectedOncePerTurn(t *testing.T) {
	st, _, sessionID := openMemStore(t)

	// First tool-use of the session claims (marker starts NULL).
	first, err := st.MarkSessionPromptInjected(sessionID, "2026-01-01T00:00:00.000Z")
	if err != nil || !first {
		t.Fatalf("first mark = %v, %v", first, err)
	}
	// A later tool-use in the same turn must not re-inject.
	if err := st.AppendEvent(sessionID, "tool_use", "Edit", "2026-01-01T00:00:01.000Z"); err != nil {
		t.Fatal(err)
	}
	again, err := st.MarkSessionPromptInjected(sessionID, "2026-01-01T00:00:02.000Z")
	if err != nil || again {
		t.Errorf("mark after tool_use only = %v, %v (must not re-arm)", again, err)
	}
	// A new prompt opens the next turn: the next tool-use re-injects.
	if err := st.AppendEvent(sessionID, "prompt", "", "2026-01-01T00:00:03.000Z"); err != nil {
		t.Fatal(err)
	}
	rearmed, err := st.MarkSessionPromptInjected(sessionID, "2026-01-01T00:00:04.000Z")
	if err != nil || !rearmed {
		t.Fatalf("mark after new prompt = %v, %v (must re-arm)", rearmed, err)
	}
	final, err := st.MarkSessionPromptInjected(sessionID, "2026-01-01T00:00:05.000Z")
	if err != nil || final {
		t.Errorf("mark with no newer prompt = %v, %v (must lose)", final, err)
	}
}

func TestMarkSessionCheckpointedThreshold(t *testing.T) {
	st, _, sessionID := openMemStore(t)

	// Below threshold: no claim.
	_ = st.AppendEvent(sessionID, "tool_use", "Edit", "2026-01-01T00:00:00.000Z")
	_ = st.AppendEvent(sessionID, "tool_use", "Bash", "2026-01-01T00:00:01.000Z")
	won, err := st.MarkSessionCheckpointed(sessionID, "2026-01-01T00:00:02.000Z", 3)
	if err != nil || won {
		t.Fatalf("below-threshold mark = %v, %v (must not claim)", won, err)
	}

	// At threshold: exactly one claim.
	_ = st.AppendEvent(sessionID, "tool_use", "Read", "2026-01-01T00:00:03.000Z")
	won, err = st.MarkSessionCheckpointed(sessionID, "2026-01-01T00:00:04.000Z", 3)
	if err != nil || !won {
		t.Fatalf("at-threshold mark = %v, %v (must claim)", won, err)
	}

	// Immediately after: nothing new since the checkpoint, so no claim.
	won, err = st.MarkSessionCheckpointed(sessionID, "2026-01-01T00:00:05.000Z", 3)
	if err != nil || won {
		t.Errorf("second mark with no new tool_use = %v, %v (must lose)", won, err)
	}
}

func TestMarkSessionCheckpointedRearmsAfterToolUse(t *testing.T) {
	st, _, sessionID := openMemStore(t)
	_ = st.AppendEvent(sessionID, "tool_use", "Edit", "2026-01-01T00:00:00.000Z")
	won, err := st.MarkSessionCheckpointed(sessionID, "2026-01-01T00:00:01.000Z", 1)
	if err != nil || !won {
		t.Fatalf("first mark = %v, %v", won, err)
	}

	// tool_use dated before the checkpoint does not re-arm.
	won, err = st.MarkSessionCheckpointed(sessionID, "2026-01-01T00:00:02.000Z", 1)
	if err != nil || won {
		t.Fatalf("mark with only old tool_use = %v, %v (must not re-arm)", won, err)
	}

	// A tool_use after the last checkpoint re-arms at threshold 1.
	_ = st.AppendEvent(sessionID, "tool_use", "Bash", "2026-01-01T00:00:03.000Z")
	won, err = st.MarkSessionCheckpointed(sessionID, "2026-01-01T00:00:04.000Z", 1)
	if err != nil || !won {
		t.Fatalf("mark after new tool_use = %v, %v (must re-arm)", won, err)
	}
}

func TestMarkSessionCheckpointedNoActivity(t *testing.T) {
	st, _, sessionID := openMemStore(t)
	// With no tool_use at all, even threshold 1 must not fire.
	won, err := st.MarkSessionCheckpointed(sessionID, Now(), 1)
	if err != nil || won {
		t.Errorf("no-activity checkpoint = %v, %v (must not claim)", won, err)
	}
}

func TestLatestSessionID(t *testing.T) {
	st, projectID, sessionID := openMemStore(t)

	got, err := st.LatestSessionID(projectID)
	if err != nil || got != sessionID {
		t.Fatalf("latest = %d, %v, want %d", got, err, sessionID)
	}

	newer, err := st.EnsureSession("sess-mem-ops-2", projectID, "2999-01-01T00:00:00Z", "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	got, err = st.LatestSessionID(projectID)
	if err != nil || got != newer {
		t.Errorf("latest after newer session = %d, %v, want %d", got, err, newer)
	}

	emptyProject, err := st.UpsertProject(ProjectIdentity{Kind: "remote", Identity: "gitlab.com/acme/empty", DisplayName: "empty"}, Now())
	if err != nil {
		t.Fatal(err)
	}
	got, err = st.LatestSessionID(emptyProject)
	if err != nil || got != 0 {
		t.Errorf("latest with no sessions = %d, %v, want 0", got, err)
	}
}

func TestSessionActivity(t *testing.T) {
	st, _, sessionID := openMemStore(t)
	prompts, tools, err := st.SessionActivity(sessionID)
	if err != nil || prompts != 0 || tools != 0 {
		t.Fatalf("empty activity = %d/%d, %v", prompts, tools, err)
	}
	at := Now()
	_ = st.AppendEvent(sessionID, "prompt", "", at)
	_ = st.AppendEvent(sessionID, "tool_use", "Edit", at)
	_ = st.AppendEvent(sessionID, "tool_use", "Bash", at)
	prompts, tools, err = st.SessionActivity(sessionID)
	if err != nil || prompts != 1 || tools != 2 {
		t.Errorf("activity = %d/%d, %v", prompts, tools, err)
	}
}

func TestMemoryPriorityCounts(t *testing.T) {
	st, projectID, _ := openMemStore(t)
	at := Now()
	insert := func(content, priority string) int64 {
		id, err := st.InsertMemory(NewMemory{ProjectID: projectID, Content: content,
			Kind: "fact", Origin: "auto", Priority: priority}, at)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	insert("a", MemoryPriorityCritical)
	insert("b", MemoryPriorityBackground)
	insert("c", "")
	superseded := insert("d", MemoryPriorityCritical)
	live := insert("e", MemoryPriorityNormal)
	if err := st.SupersedeMemory(superseded, live, projectID, at); err != nil {
		t.Fatal(err)
	}

	counts, err := st.MemoryPriorityCounts(projectID)
	if err != nil {
		t.Fatal(err)
	}
	// An unset priority is stored as the default, and a superseded entry is not
	// competing for the budget so it must not inflate the share.
	want := map[string]int{MemoryPriorityCritical: 1, MemoryPriorityNormal: 2, MemoryPriorityBackground: 1}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("%s = %d, want %d (got %v)", k, counts[k], v, counts)
		}
	}
}
