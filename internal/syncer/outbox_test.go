package syncer

import (
	"path/filepath"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// A mixed-assistant local store must sync each session with its true assistant
// label — the outbox previously hardcoded "claude-code" for every row (R2 fix,
// SC-004).
func TestEligibleSessionsCarryTrueAssistantPerRow(t *testing.T) {
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	at := store.Now()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "dir", Identity: "/proj", DisplayName: "proj"}, at)
	if err != nil {
		t.Fatal(err)
	}

	rows := []struct{ externalID, assistant string }{
		{"cla-1", "claude-code"},
		{"cursor:c1", "cursor"},
		{"gemini-cli:g1", "gemini-cli"},
		{"copilot-cli:p1", "copilot-cli"},
	}
	for _, r := range rows {
		sid, err := st.EnsureSession(r.externalID, pid, at, "", r.assistant)
		if err != nil {
			t.Fatalf("ensure %s: %v", r.externalID, err)
		}
		if err := st.AppendEvent(sid, "prompt", "", at); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{Links: map[string]config.Link{
		config.LinkKey("dir", "/proj"): {ProjectKey: "pk"},
	}}

	sessions, err := eligibleSessions(st, cfg, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != len(rows) {
		t.Fatalf("got %d eligible sessions, want %d", len(sessions), len(rows))
	}

	seen := map[string]bool{}
	for _, o := range sessions {
		seen[o.session.Assistant] = true
		if o.session.ProjectKey != "pk" {
			t.Errorf("project key = %q, want pk", o.session.ProjectKey)
		}
		if o.session.EventCounts["prompt"] != 1 {
			t.Errorf("assistant %s prompt count = %d, want 1", o.session.Assistant, o.session.EventCounts["prompt"])
		}
	}
	for _, r := range rows {
		if !seen[r.assistant] {
			t.Errorf("assistant %q missing from synced batch (mislabeled?)", r.assistant)
		}
	}
}
