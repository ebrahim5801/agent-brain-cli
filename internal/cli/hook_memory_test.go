package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func TestInjectionNotice(t *testing.T) {
	cases := []struct {
		name string
		pack memory.Pack
		want string
	}{
		{"empty", memory.Pack{}, ""},
		{"singular", memory.Pack{Personal: 1, Active: 1},
			"agent-brain: loaded 1 memory into context — inspect with: agent-brain memory list"},
		{"personal only", memory.Pack{Personal: 4, Active: 4},
			"agent-brain: loaded 4 memories into context — inspect with: agent-brain memory list"},
		{"with team", memory.Pack{Personal: 4, Team: 2, Active: 6},
			"agent-brain: loaded 6 memories (4 personal, 2 team) into context — inspect with: agent-brain memory list"},
		{"budget cut", memory.Pack{Personal: 10, Active: 47},
			"agent-brain: loaded 10 of 47 memories into context — inspect with: agent-brain memory list"},
	}
	for _, tc := range cases {
		if got := injectionNotice(tc.pack); got != tc.want {
			t.Errorf("%s: injectionNotice = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestRecordCitationsIntersectionGuard proves the citation pass's primary
// false-positive defense: a #id/team#handle referenced in the transcript is
// only marked cited when that exact id/uid was retrieved by this session.
// #999 is cited in the transcript but never retrieved, so it must leave no
// row at all (MarkCited never creates one) while #282 and the retrieved team
// uid, both cited and retrieved, end up marked.
func TestRecordCitationsIntersectionGuard(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	enableMemoryForTest(t)

	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcript, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	hookWith(t, "session-start", "claude-code", `{"session_id":"cit1","cwd":"`+proj+`","transcript_path":"`+transcript+`"}`)

	st := openTestStore(t)
	var sessionID, projectID int64
	if err := st.QueryRow(`SELECT id, project_id FROM sessions WHERE external_id = 'cit1'`).Scan(&sessionID, &projectID); err != nil {
		t.Fatal(err)
	}

	if err := st.RecordRetrieval(sessionID, projectID, "personal", 282, "", store.Now()); err != nil {
		t.Fatalf("seed retrieval 282: %v", err)
	}
	const teamUID = "abc12345-full-team-uid"
	if err := st.ApplyPull(projectID, []store.TeamMemoryRow{{
		UID: teamUID, ProjectID: projectID, Author: "teammate", Content: "use gofmt",
		Kind: "convention", Origin: "auto", Status: "active",
		CapturedAt: store.Now(), UpdatedAt: store.Now(),
	}}); err != nil {
		t.Fatalf("seed team cache: %v", err)
	}
	if err := st.RecordRetrieval(sessionID, projectID, "team", 0, teamUID, store.Now()); err != nil {
		t.Fatalf("seed retrieval team: %v", err)
	}

	body := `{"type":"assistant","message":{"content":[{"type":"text",` +
		`"text":"Used memory #282 and memory #999, and [team#abc12345]."}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	hookWith(t, "session-end", "claude-code", `{"session_id":"cit1","cwd":"`+proj+`"}`)

	var cited282 int
	if err := st.QueryRow(`SELECT cited FROM memory_usage_events
        WHERE session_id = ? AND scope = 'personal' AND memory_id = 282`, sessionID).Scan(&cited282); err != nil {
		t.Fatalf("read #282 row: %v", err)
	}
	if cited282 != 1 {
		t.Errorf("cited(#282) = %d, want 1 (retrieved and cited)", cited282)
	}

	var count999 int
	if err := st.QueryRow(`SELECT COUNT(*) FROM memory_usage_events
        WHERE session_id = ? AND scope = 'personal' AND memory_id = 999`, sessionID).Scan(&count999); err != nil {
		t.Fatal(err)
	}
	if count999 != 0 {
		t.Errorf("a never-retrieved id #999 must not be recorded, got count=%d", count999)
	}

	var citedTeam int
	if err := st.QueryRow(`SELECT cited FROM memory_usage_events
        WHERE session_id = ? AND scope = 'team' AND team_uid = ?`, sessionID, teamUID).Scan(&citedTeam); err != nil {
		t.Fatalf("read team row: %v", err)
	}
	if citedTeam != 1 {
		t.Errorf("cited(team) = %d, want 1 (retrieved and cited)", citedTeam)
	}
}

// TestRecordCitationsGracefulWithoutScanner proves an adapter that doesn't
// implement assistant.CitationScanner leaves retrieval rows alone: no panic,
// no citation, retrieval untouched (graceful degradation).
func TestRecordCitationsGracefulWithoutScanner(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	enableMemoryForTest(t)

	hookWith(t, "session-start", "gemini-cli", `{"session_id":"cit2","cwd":"`+proj+`"}`)

	st := openTestStore(t)
	var sessionID, projectID int64
	if err := st.QueryRow(`SELECT id, project_id FROM sessions WHERE external_id = 'gemini-cli:cit2'`).
		Scan(&sessionID, &projectID); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRetrieval(sessionID, projectID, "personal", 5, "", store.Now()); err != nil {
		t.Fatalf("seed retrieval: %v", err)
	}

	hookWith(t, "session-end", "gemini-cli", `{"session_id":"cit2","cwd":"`+proj+`"}`)

	var cited int
	if err := st.QueryRow(`SELECT cited FROM memory_usage_events WHERE session_id = ? AND memory_id = 5`, sessionID).Scan(&cited); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if cited != 0 {
		t.Errorf("cited = %d, want 0 (gemini-cli has no CitationScanner)", cited)
	}
}

// The injected prompts are the only thing that reaches the auto-save path, so a
// priority the model never hears about would leave every distilled entry at the
// default. Both prompts carry it: a checkpoint save is a real save.
func TestInjectedPromptsCarryPriority(t *testing.T) {
	for name, prompt := range map[string]string{
		"distillation": distillationReason,
		"checkpoint":   checkpointReason,
	} {
		for _, must := range []string{"priority", "critical", "background"} {
			if !strings.Contains(prompt, must) {
				t.Errorf("%s prompt missing %q", name, must)
			}
		}
	}
}

// contracts/session-injection.md quotes the Stop prompt verbatim. It had already
// drifted from the code once; nothing but this test would catch it again.
func TestSessionInjectionContractMatchesPrompt(t *testing.T) {
	// specs/ is private and absent from the extracted public CLI tree, where
	// this guard has nothing to guard. Skip only on that whole-tree signal — a
	// missing file while specs/ exists is the drift this test is here to catch.
	if _, err := os.Stat(filepath.Join("..", "..", "specs")); os.IsNotExist(err) {
		t.Skip("specs/ not present (public CLI tree); contract lives in the private repo")
	}
	path := filepath.Join("..", "..", "specs", "003-personal-memory-mcp", "contracts", "session-injection.md")
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The doc quotes the prompt inside a JSON payload, so compare the JSON form.
	quoted, err := json.Marshal(distillationReason)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), string(quoted)) {
		t.Errorf("%s no longer quotes distillationReason verbatim; update it with the current prompt", path)
	}
}
