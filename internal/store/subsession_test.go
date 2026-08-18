package store

import "testing"

func seedParentSession(t *testing.T, st *Store) (projectID, parentID int64) {
	t.Helper()
	at := Now()
	projectID, err := st.UpsertProject(ProjectIdentity{Kind: "directory", Identity: "/p", DisplayName: "p"}, at)
	if err != nil {
		t.Fatal(err)
	}
	parentID, err = st.EnsureSession("sess-1", projectID, at, "/tmp/t.jsonl", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	return projectID, parentID
}

func subFixture(projectID, parentID int64) SubSession {
	return SubSession{
		ExternalID: "sess-1/agent-abc", ProjectID: projectID, ParentID: parentID,
		Assistant: "claude-code", AgentType: "general-purpose", Model: "claude-fable-5",
		StartedAt: "2026-07-15T14:18:31.580Z", EndedAt: "2026-07-15T14:35:13.122Z",
		TranscriptPath: "/tmp/agent-abc.jsonl", Summary: "task\n\nreport", Prompt: "do the task",
		Usage: Usage{Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40},
	}
}

func TestUpsertSubSessionIdempotentDirtyGuard(t *testing.T) {
	st := openTestStore(t)
	projectID, parentID := seedParentSession(t, st)

	id, err := st.UpsertSubSession(subFixture(projectID, parentID))
	if err != nil {
		t.Fatal(err)
	}

	var parent int64
	var agentType, prompt, endReason string
	var dirty int
	row := st.DB.QueryRow(`SELECT parent_session_id, agent_type, agent_prompt, end_reason, sync_dirty FROM sessions WHERE id = ?`, id)
	if err := row.Scan(&parent, &agentType, &prompt, &endReason, &dirty); err != nil {
		t.Fatal(err)
	}
	if parent != parentID || agentType != "general-purpose" || prompt != "do the task" || endReason != "normal" || dirty != 1 {
		t.Fatalf("row = parent %d type %q prompt %q reason %q dirty %d", parent, agentType, prompt, endReason, dirty)
	}

	// Ack the sync, then re-scan unchanged: the row must stay clean.
	if _, err := st.Exec(`UPDATE sessions SET synced_at = ?, sync_dirty = 0 WHERE id = ?`, Now(), id); err != nil {
		t.Fatal(err)
	}
	id2, err := st.UpsertSubSession(subFixture(projectID, parentID))
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("re-scan created a new row: %d != %d", id2, id)
	}
	if err := st.QueryRow(`SELECT sync_dirty FROM sessions WHERE id = ?`, id).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Fatal("unchanged re-scan re-dirtied the row")
	}

	// A grown transcript (more usage) re-dirties.
	grown := subFixture(projectID, parentID)
	grown.Usage.Output = 99
	if _, err := st.UpsertSubSession(grown); err != nil {
		t.Fatal(err)
	}
	var out int64
	if err := st.QueryRow(`SELECT sync_dirty, output_tokens FROM sessions WHERE id = ?`, id).Scan(&dirty, &out); err != nil {
		t.Fatal(err)
	}
	if dirty != 1 || out != 99 {
		t.Fatalf("grown re-scan: dirty %d output %d", dirty, out)
	}
}

// The distillation summary targets the newest top-level session, never a
// sub-session captured after it.
func TestSetSessionSummarySkipsSubSessions(t *testing.T) {
	st := openTestStore(t)
	projectID, parentID := seedParentSession(t, st)

	sub := subFixture(projectID, parentID)
	sub.StartedAt = "2099-01-01T00:00:00.000Z"
	if _, err := st.UpsertSubSession(sub); err != nil {
		t.Fatal(err)
	}

	ok, err := st.SetSessionSummary(projectID, "distilled")
	if err != nil || !ok {
		t.Fatalf("SetSessionSummary: ok=%v err=%v", ok, err)
	}
	var got string
	if err := st.QueryRow(`SELECT COALESCE(summary, '') FROM sessions WHERE id = ?`, parentID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "distilled" {
		t.Fatalf("parent summary = %q", got)
	}
	if latest, err := st.LatestSessionID(projectID); err != nil || latest != parentID {
		t.Fatalf("LatestSessionID = %d err=%v, want %d", latest, err, parentID)
	}
}
