package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/entitlement"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// hookWith drives runHook once with the given stdin payload and returns its
// stdout envelope (or "").
func hookWith(t *testing.T, event, assistantName, payload string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "payload-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old; f.Close() }()
	return runHook(event, assistantName)
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// enableMemory records Pro entitlement + memory consent so the memory gate is
// active for injection/distillation.
func enableMemoryForTest(t *testing.T) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := config.Update(func(c *config.Config) error {
		c.Entitlement = &config.Entitlement{Tier: entitlement.TierPro, VerifiedAt: now}
		c.Consent.Memory = &config.Consent{GrantedAt: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type sessionRow struct {
	assistant string
	model     string
	in, out   int64
	cacheRead int64
}

func readSession(t *testing.T, st *store.Store, externalID string) sessionRow {
	t.Helper()
	var r sessionRow
	var model *string
	err := st.QueryRow(`SELECT assistant, model, input_tokens, output_tokens, cache_read_tokens
        FROM sessions WHERE external_id = ?`, externalID).Scan(&r.assistant, &model, &r.in, &r.out, &r.cacheRead)
	if err != nil {
		t.Fatalf("read session %s: %v", externalID, err)
	}
	if model != nil {
		r.model = *model
	}
	return r
}

func TestCaptureCursorHonestAbsenceAndGeminiUsage(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()

	hookWith(t, "session-start", "cursor", `{"conversation_id":"c1","workspace_roots":["`+proj+`"],"model":"gpt-x"}`)
	hookWith(t, "session-end", "cursor", `{"conversation_id":"c1","workspace_roots":["`+proj+`"]}`)

	hookWith(t, "session-start", "gemini-cli", `{"session_id":"g1","cwd":"`+proj+`"}`)
	hookWith(t, "model-usage", "gemini-cli", `{"session_id":"g1","cwd":"`+proj+`","llm_request":{"model":"gemini-2.5-pro"},"llm_response":{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":40,"cachedContentTokenCount":25}}}`)
	hookWith(t, "session-end", "gemini-cli", `{"session_id":"g1","cwd":"`+proj+`"}`)

	st := openTestStore(t)

	cur := readSession(t, st, "cursor:c1")
	if cur.assistant != "cursor" || cur.model != "gpt-x" || cur.in != 0 || cur.out != 0 || cur.cacheRead != 0 {
		t.Errorf("cursor session = %+v; want honest absence (model gpt-x, counters 0)", cur)
	}

	gem := readSession(t, st, "gemini-cli:g1")
	if gem.assistant != "gemini-cli" || gem.model != "gemini-2.5-pro" || gem.in != 100 || gem.out != 40 || gem.cacheRead != 25 {
		t.Errorf("gemini session = %+v; want 100/40/25 gemini-2.5-pro", gem)
	}

	// model-usage appends no activity event row.
	var events int
	_ = st.QueryRow(`SELECT COUNT(*) FROM events e JOIN sessions s ON s.id = e.session_id
        WHERE s.external_id = 'gemini-cli:g1'`).Scan(&events)
	// session_start + session_end only.
	if events != 2 {
		t.Errorf("gemini event count = %d; want 2 (model-usage adds no event)", events)
	}
}

func TestCaptureSameNativeKeyStaysDistinctAcrossAssistants(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	hookWith(t, "session-start", "cursor", `{"conversation_id":"dup","workspace_roots":["`+proj+`"]}`)
	hookWith(t, "session-start", "gemini-cli", `{"session_id":"dup","cwd":"`+proj+`"}`)

	st := openTestStore(t)
	var n int
	_ = st.QueryRow(`SELECT COUNT(*) FROM sessions WHERE external_id IN ('cursor:dup','gemini-cli:dup')`).Scan(&n)
	if n != 2 {
		t.Errorf("expected 2 distinct sessions for the same native key, got %d", n)
	}
}

// extractPack pulls the injected memory pack out of any adapter's envelope.
func extractPack(t *testing.T, envelope string) string {
	t.Helper()
	if envelope == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(envelope), &m); err != nil {
		t.Fatalf("envelope not JSON: %v (%s)", err, envelope)
	}
	if v, ok := m["additional_context"].(string); ok { // cursor
		return v
	}
	if v, ok := m["additionalContext"].(string); ok { // copilot
		return v
	}
	if hso, ok := m["hookSpecificOutput"].(map[string]any); ok { // claude / gemini
		if v, ok := hso["additionalContext"].(string); ok {
			return v
		}
	}
	t.Fatalf("no pack field in envelope: %s", envelope)
	return ""
}

func TestInjectionPackParityAcrossAssistants(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	enableMemoryForTest(t)

	// Save a memory so the pack is non-empty.
	st := openTestStore(t)
	pid, err := st.UpsertProject(projectIdentityFor(t, proj), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertMemory(store.NewMemory{
		ProjectID: pid, Content: "always use tabs", Kind: "convention", Origin: "explicit",
	}, store.Now()); err != nil {
		t.Fatal(err)
	}

	starts := map[string]string{
		"cursor":      `{"conversation_id":"s","workspace_roots":["` + proj + `"]}`,
		"copilot-cli": `{"sessionId":"s","cwd":"` + proj + `"}`,
		"gemini-cli":  `{"session_id":"s","cwd":"` + proj + `"}`,
		"claude-code": `{"session_id":"s","cwd":"` + proj + `"}`,
		"codex":       `{"session_id":"s","cwd":"` + proj + `"}`,
	}
	var packs []string
	for name, payload := range starts {
		env := hookWith(t, "session-start", name, payload)
		pack := extractPack(t, env)
		if pack == "" {
			t.Fatalf("%s: empty injection pack", name)
		}
		packs = append(packs, pack)
	}
	for i := 1; i < len(packs); i++ {
		if packs[i] != packs[0] {
			t.Errorf("pack mismatch across assistants:\n%q\nvs\n%q", packs[0], packs[i])
		}
	}
}

// The Claude envelope carries a user-visible loaded-memories notice
// (systemMessage); adapters without a display channel stay notice-free.
func TestInjectionNoticeClaudeOnly(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	enableMemoryForTest(t)

	st := openTestStore(t)
	pid, err := st.UpsertProject(projectIdentityFor(t, proj), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"always use tabs", "prefer pgx"} {
		if _, err := st.InsertMemory(store.NewMemory{
			ProjectID: pid, Content: content, Kind: "convention", Origin: "explicit",
		}, store.Now()); err != nil {
			t.Fatal(err)
		}
	}

	env := hookWith(t, "session-start", "claude-code", `{"session_id":"n1","cwd":"`+proj+`"}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(env), &m); err != nil {
		t.Fatalf("claude envelope not JSON: %v (%s)", err, env)
	}
	notice, _ := m["systemMessage"].(string)
	if !strings.Contains(notice, "loaded 2 memories") {
		t.Errorf("systemMessage = %q, want a loaded-2-memories notice", notice)
	}

	env = hookWith(t, "session-start", "gemini-cli", `{"session_id":"n1","cwd":"`+proj+`"}`)
	if strings.Contains(env, "systemMessage") {
		t.Errorf("gemini envelope should carry no notice: %s", env)
	}
}

// A prompt event injects only memory the session has not already been shown
// (dedup-walk), so the model gets a fresh refresh before each task without
// repeating the session-start pack.
func TestPromptInjectionWalksPastSeenEntries(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	enableMemoryForTest(t)

	st := openTestStore(t)
	pid, err := st.UpsertProject(projectIdentityFor(t, proj), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertMemory(store.NewMemory{
		ProjectID: pid, Content: "seen at session start", Kind: "convention", Origin: "explicit",
	}, store.Now()); err != nil {
		t.Fatal(err)
	}

	// Session start serves and records the first memory.
	if pack := extractPack(t, hookWith(t, "session-start", "claude-code", `{"session_id":"s","cwd":"`+proj+`"}`)); !strings.Contains(pack, "seen at session start") {
		t.Fatalf("session-start pack missing first memory: %q", pack)
	}

	// A memory saved mid-session is what the next prompt should surface.
	if _, err := st.InsertMemory(store.NewMemory{
		ProjectID: pid, Content: "saved mid session", Kind: "convention", Origin: "explicit",
	}, store.Now()); err != nil {
		t.Fatal(err)
	}

	env := hookWith(t, "prompt", "claude-code", `{"session_id":"s","cwd":"`+proj+`"}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(env), &m); err != nil {
		t.Fatalf("prompt envelope not JSON: %v (%s)", err, env)
	}
	if hso, _ := m["hookSpecificOutput"].(map[string]any); hso == nil || hso["hookEventName"] != "UserPromptSubmit" {
		t.Errorf("prompt injection should use UserPromptSubmit envelope: %s", env)
	}
	pack := extractPack(t, env)
	if !strings.Contains(pack, "saved mid session") {
		t.Errorf("prompt pack missing the unseen memory: %q", pack)
	}
	if strings.Contains(pack, "seen at session start") {
		t.Errorf("prompt pack repeated an already-seen memory: %q", pack)
	}

	// With nothing new since, a second prompt injects nothing.
	if env := hookWith(t, "prompt", "claude-code", `{"session_id":"s","cwd":"`+proj+`"}`); env != "" {
		t.Errorf("second prompt should inject nothing, got: %s", env)
	}
}

// Cursor and Copilot cannot inject before the prompt; their fallback fires once
// per turn on the first tool-use after a prompt, via the postToolUse channel.
func TestToolUseInjectionFallbackOncePerTurn(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	enableMemoryForTest(t)

	st := openTestStore(t)
	pid, err := st.UpsertProject(projectIdentityFor(t, proj), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertMemory(store.NewMemory{
		ProjectID: pid, Content: "seen at session start", Kind: "convention", Origin: "explicit",
	}, store.Now()); err != nil {
		t.Fatal(err)
	}
	hookWith(t, "session-start", "cursor", `{"conversation_id":"c","workspace_roots":["`+proj+`"]}`)
	if _, err := st.InsertMemory(store.NewMemory{
		ProjectID: pid, Content: "saved mid session", Kind: "convention", Origin: "explicit",
	}, store.Now()); err != nil {
		t.Fatal(err)
	}

	// The prompt hook only permits the submission (no injection channel).
	if env := hookWith(t, "prompt", "cursor", `{"conversation_id":"c","workspace_roots":["`+proj+`"]}`); env != `{"continue":true}` {
		t.Errorf("cursor prompt should pass through, got: %s", env)
	}

	// First tool-use of the turn injects the unseen memory.
	pack := extractPack(t, hookWith(t, "tool-use", "cursor", `{"conversation_id":"c","workspace_roots":["`+proj+`"],"tool_name":"read_file"}`))
	if !strings.Contains(pack, "saved mid session") {
		t.Errorf("tool-use fallback missing unseen memory: %q", pack)
	}

	// A second tool-use in the same turn does not re-inject.
	if env := hookWith(t, "tool-use", "cursor", `{"conversation_id":"c","workspace_roots":["`+proj+`"],"tool_name":"read_file"}`); env != "" {
		t.Errorf("second tool-use same turn should not inject, got: %s", env)
	}
}

// An empty pack must not consume the turn's single injection: a memory saved
// later in the same turn still reaches the model on a subsequent tool-use.
func TestToolUseInjectionNotBurnedByEmptyPack(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	enableMemoryForTest(t)

	st := openTestStore(t)
	pid, err := st.UpsertProject(projectIdentityFor(t, proj), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertMemory(store.NewMemory{
		ProjectID: pid, Content: "seen at session start", Kind: "convention", Origin: "explicit",
	}, store.Now()); err != nil {
		t.Fatal(err)
	}
	hookWith(t, "session-start", "cursor", `{"conversation_id":"c","workspace_roots":["`+proj+`"]}`)
	hookWith(t, "prompt", "cursor", `{"conversation_id":"c","workspace_roots":["`+proj+`"]}`)

	// Nothing unseen yet: the first tool-use injects nothing and must leave the
	// turn's injection still armed.
	if env := hookWith(t, "tool-use", "cursor", `{"conversation_id":"c","workspace_roots":["`+proj+`"],"tool_name":"read_file"}`); env != "" {
		t.Fatalf("tool-use with nothing unseen should inject nothing, got: %s", env)
	}

	if _, err := st.InsertMemory(store.NewMemory{
		ProjectID: pid, Content: "saved mid turn", Kind: "convention", Origin: "explicit",
	}, store.Now()); err != nil {
		t.Fatal(err)
	}
	pack := extractPack(t, hookWith(t, "tool-use", "cursor", `{"conversation_id":"c","workspace_roots":["`+proj+`"],"tool_name":"read_file"}`))
	if !strings.Contains(pack, "saved mid turn") {
		t.Errorf("injection was burned by the earlier empty pack; got: %q", pack)
	}
}

// The dedup-walk stops once a session has been served its ceiling, so a long
// session cannot march down the ranking and inject the whole project.
func TestPromptInjectionStopsAtSessionCap(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	enableMemoryForTest(t)

	old := promptWalkMaxSessionEntries
	promptWalkMaxSessionEntries = 2
	t.Cleanup(func() { promptWalkMaxSessionEntries = old })

	st := openTestStore(t)
	pid, err := st.UpsertProject(projectIdentityFor(t, proj), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := st.InsertMemory(store.NewMemory{
			ProjectID: pid, Content: "memory number " + strconv.Itoa(i), Kind: "fact", Origin: "auto",
		}, store.Now()); err != nil {
			t.Fatal(err)
		}
	}

	// Session start serves well past the cap on its own.
	if pack := extractPack(t, hookWith(t, "session-start", "claude-code", `{"session_id":"s","cwd":"`+proj+`"}`)); pack == "" {
		t.Fatal("session-start served nothing")
	}
	if env := hookWith(t, "prompt", "claude-code", `{"session_id":"s","cwd":"`+proj+`"}`); env != "" {
		t.Errorf("prompt past the session cap should inject nothing, got: %s", env)
	}
}

func TestInjectionEmptyWhenMemoryGated(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	// Memory not enabled ⇒ gate closed.
	env := hookWith(t, "session-start", "gemini-cli", `{"session_id":"s","cwd":"`+proj+`"}`)
	if env != "" {
		t.Errorf("expected empty stdout when memory is gated, got %q", env)
	}
}

// extractReason pulls the distillation prompt out of any adapter's stop
// envelope (cursor followup_message, others decision-block reason).
func extractReason(t *testing.T, envelope string) string {
	t.Helper()
	if envelope == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(envelope), &m); err != nil {
		t.Fatalf("stop envelope not JSON: %v (%s)", err, envelope)
	}
	if v, ok := m["followup_message"].(string); ok {
		return v
	}
	if v, ok := m["reason"].(string); ok {
		return v
	}
	t.Fatalf("no reason field in stop envelope: %s", envelope)
	return ""
}

func TestDistillationFiresOncePerAssistant(t *testing.T) {
	for _, tc := range []struct{ name, start, prompt, tool, stop string }{
		{"cursor",
			`{"conversation_id":"d","workspace_roots":["%s"]}`,
			`{"conversation_id":"d","workspace_roots":["%s"]}`,
			`{"conversation_id":"d","workspace_roots":["%s"],"tool_name":"edit"}`,
			`{"conversation_id":"d","workspace_roots":["%s"]}`},
		{"copilot-cli",
			`{"sessionId":"d","cwd":"%s"}`,
			`{"sessionId":"d","cwd":"%s"}`,
			`{"sessionId":"d","cwd":"%s","toolName":"edit"}`,
			`{"sessionId":"d","cwd":"%s"}`},
		{"gemini-cli",
			`{"session_id":"d","cwd":"%s"}`,
			`{"session_id":"d","cwd":"%s"}`,
			`{"session_id":"d","cwd":"%s","tool_name":"edit"}`,
			`{"session_id":"d","cwd":"%s"}`},
		{"codex",
			`{"session_id":"d","cwd":"%s"}`,
			`{"session_id":"d","cwd":"%s"}`,
			`{"session_id":"d","cwd":"%s","tool_name":"Bash"}`,
			`{"session_id":"d","cwd":"%s"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			proj := t.TempDir()
			enableMemoryForTest(t)

			fill := func(s string) string { return strings.ReplaceAll(s, "%s", proj) }
			hookWith(t, "session-start", tc.name, fill(tc.start))
			hookWith(t, "prompt", tc.name, fill(tc.prompt))
			hookWith(t, "tool-use", tc.name, fill(tc.tool))

			env := hookWith(t, "stop", tc.name, fill(tc.stop))
			if got := extractReason(t, env); got != distillationReason {
				t.Errorf("%s stop reason = %q, want the distillation prompt", tc.name, got)
			}

			// The marker is claimed before emission: a second stop is silent.
			if again := hookWith(t, "stop", tc.name, fill(tc.stop)); again != "" {
				t.Errorf("%s second stop emitted %q, want empty (at most one distillation)", tc.name, again)
			}
		})
	}
}

func TestDistillationRearmsAfterNewPrompt(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	enableMemoryForTest(t)

	hookWith(t, "session-start", "gemini-cli", `{"session_id":"r","cwd":"`+proj+`"}`)
	hookWith(t, "prompt", "gemini-cli", `{"session_id":"r","cwd":"`+proj+`"}`)
	hookWith(t, "tool-use", "gemini-cli", `{"session_id":"r","cwd":"`+proj+`","tool_name":"edit"}`)

	env := hookWith(t, "stop", "gemini-cli", `{"session_id":"r","cwd":"`+proj+`"}`)
	if got := extractReason(t, env); got != distillationReason {
		t.Fatalf("first stop reason = %q, want the distillation prompt", got)
	}
	if again := hookWith(t, "stop", "gemini-cli", `{"session_id":"r","cwd":"`+proj+`"}`); again != "" {
		t.Errorf("stop without new prompt emitted %q, want empty", again)
	}

	// Timestamps have millisecond precision; step past the marker's instant.
	time.Sleep(2 * time.Millisecond)
	hookWith(t, "prompt", "gemini-cli", `{"session_id":"r","cwd":"`+proj+`"}`)

	env = hookWith(t, "stop", "gemini-cli", `{"session_id":"r","cwd":"`+proj+`"}`)
	if got := extractReason(t, env); got != distillationReason {
		t.Errorf("stop after new prompt = %q, want the distillation prompt again", got)
	}
	if again := hookWith(t, "stop", "gemini-cli", `{"session_id":"r","cwd":"`+proj+`"}`); again != "" {
		t.Errorf("post-redistillation stop emitted %q, want empty", again)
	}
}

func TestDistillationSilentWithoutActivity(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	enableMemoryForTest(t)
	// session-start + stop, but no prompt and no tool-use.
	hookWith(t, "session-start", "gemini-cli", `{"session_id":"n","cwd":"`+proj+`"}`)
	if env := hookWith(t, "stop", "gemini-cli", `{"session_id":"n","cwd":"`+proj+`"}`); env != "" {
		t.Errorf("no-activity stop emitted %q, want empty", env)
	}
}

func TestDistillationSilentWhenMemoryGated(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	// Memory off: even with activity, no distillation.
	hookWith(t, "session-start", "gemini-cli", `{"session_id":"m","cwd":"`+proj+`"}`)
	hookWith(t, "prompt", "gemini-cli", `{"session_id":"m","cwd":"`+proj+`"}`)
	hookWith(t, "tool-use", "gemini-cli", `{"session_id":"m","cwd":"`+proj+`","tool_name":"edit"}`)
	if env := hookWith(t, "stop", "gemini-cli", `{"session_id":"m","cwd":"`+proj+`"}`); env != "" {
		t.Errorf("gated stop emitted %q, want empty", env)
	}
}

func TestCheckpointFiresOnToolThreshold(t *testing.T) {
	for _, tc := range []struct{ name, start, tool string }{
		{"cursor",
			`{"conversation_id":"c","workspace_roots":["%s"]}`,
			`{"conversation_id":"c","workspace_roots":["%s"],"tool_name":"edit"}`},
		{"copilot-cli",
			`{"sessionId":"c","cwd":"%s"}`,
			`{"sessionId":"c","cwd":"%s","toolName":"edit"}`},
		{"gemini-cli",
			`{"session_id":"c","cwd":"%s"}`,
			`{"session_id":"c","cwd":"%s","tool_name":"edit"}`},
		{"claude-code",
			`{"session_id":"c","cwd":"%s"}`,
			`{"session_id":"c","cwd":"%s","tool_name":"edit"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			proj := t.TempDir()
			enableMemoryForTest(t)
			restore := checkpointToolThreshold
			checkpointToolThreshold = 3
			t.Cleanup(func() { checkpointToolThreshold = restore })

			fill := func(s string) string { return strings.ReplaceAll(s, "%s", proj) }
			hookWith(t, "session-start", tc.name, fill(tc.start))

			// First two tool-uses are below threshold: silent.
			for i := 0; i < 2; i++ {
				if env := hookWith(t, "tool-use", tc.name, fill(tc.tool)); env != "" {
					t.Fatalf("%s tool-use %d emitted %q, want empty (below threshold)", tc.name, i, env)
				}
			}
			// Third crosses the threshold: the checkpoint prompt is injected.
			env := hookWith(t, "tool-use", tc.name, fill(tc.tool))
			if got := extractReason(t, env); got != checkpointReason {
				t.Errorf("%s threshold tool-use reason = %q, want the checkpoint prompt", tc.name, got)
			}
			// The next tool-use re-arms from zero: silent until threshold again.
			if again := hookWith(t, "tool-use", tc.name, fill(tc.tool)); again != "" {
				t.Errorf("%s tool-use right after checkpoint emitted %q, want empty", tc.name, again)
			}
		})
	}
}

func TestCheckpointSilentWhenMemoryGated(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	restore := checkpointToolThreshold
	checkpointToolThreshold = 1
	t.Cleanup(func() { checkpointToolThreshold = restore })
	// Memory off: even at threshold, no checkpoint.
	hookWith(t, "session-start", "gemini-cli", `{"session_id":"cg","cwd":"`+proj+`"}`)
	if env := hookWith(t, "tool-use", "gemini-cli", `{"session_id":"cg","cwd":"`+proj+`","tool_name":"edit"}`); env != "" {
		t.Errorf("gated tool-use emitted %q, want empty", env)
	}
}

func projectIdentityFor(t *testing.T, dir string) store.ProjectIdentity {
	t.Helper()
	// Resolve exactly as the hook does so the saved memory shares the project id
	// the injection path will look up.
	p := attribution.Resolve(dir)
	return store.ProjectIdentity{Kind: p.Kind, Identity: p.Identity, DisplayName: p.DisplayName}
}

func TestReconcileNeverParsesForeignTranscripts(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()

	// A Gemini session with live-accumulated usage and a transcript path whose
	// file exists but is not a Claude transcript. Under a cross-assistant
	// reconcile bug, the Claude parser would return ok with zero usage on this
	// file and wipe the accumulated counters.
	transcript := filepath.Join(t.TempDir(), "gemini-transcript.json")
	if err := os.WriteFile(transcript, []byte(`{"someGeminiShape":true}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hookWith(t, "session-start", "gemini-cli", `{"session_id":"stale1","cwd":"`+proj+`","transcript_path":"`+transcript+`"}`)
	hookWith(t, "model-usage", "gemini-cli", `{"session_id":"stale1","cwd":"`+proj+`","llm_request":{"model":"gemini-2.5-pro"},"llm_response":{"usageMetadata":{"promptTokenCount":70,"candidatesTokenCount":30,"cachedContentTokenCount":5}}}`)

	st := openTestStore(t)
	stale := time.Now().UTC().Add(-5 * time.Hour).Format(store.TimeLayout)
	if _, err := st.Exec(`UPDATE sessions SET started_at = ? WHERE external_id = 'gemini-cli:stale1'`, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(`UPDATE events SET occurred_at = ? WHERE session_id =
        (SELECT id FROM sessions WHERE external_id = 'gemini-cli:stale1')`, stale); err != nil {
		t.Fatal(err)
	}

	reconcile(st, fallbackLogger())

	gem := readSession(t, st, "gemini-cli:stale1")
	if gem.in != 70 || gem.out != 30 || gem.cacheRead != 5 || gem.model != "gemini-2.5-pro" {
		t.Errorf("gemini session after reconcile = %+v; accumulated usage must survive", gem)
	}
	var endReason string
	if err := st.QueryRow(`SELECT COALESCE(end_reason,'') FROM sessions WHERE external_id = 'gemini-cli:stale1'`).Scan(&endReason); err != nil {
		t.Fatal(err)
	}
	if endReason != "interrupted" {
		t.Errorf("stale session end_reason = %q, want interrupted", endReason)
	}
}
