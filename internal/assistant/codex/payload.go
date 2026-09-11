package codex

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

const maxPayloadBytes = 10 << 20

// payload holds the only hook fields the collector consumes. Everything else a
// Codex hook payload carries is structurally unreachable from here: the fields
// are simply absent from the struct, so `prompt` (UserPromptSubmit),
// `tool_input` and `tool_response` (PostToolUse — the full command and its full
// output), and `last_assistant_message` (Stop) cannot reach storage even if a
// later caller is careless.
type payload struct {
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	ToolName       string `json:"tool_name"`
	Model          string `json:"model"`
}

func parsePayload(r io.Reader) (payload, error) {
	var p payload
	data, err := io.ReadAll(io.LimitReader(r, maxPayloadBytes))
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	if p.SessionID == "" {
		return p, errors.New("payload missing session_id")
	}
	return p, nil
}

// Adapter is the Codex CLI implementation of assistant.Adapter.
type Adapter struct{}

func init() {
	assistant.Register(assistant.OrderCodex, Adapter{})
}

func (Adapter) Name() string        { return Assistant }
func (Adapter) DisplayName() string { return "Codex CLI" }
func (Adapter) Detected() bool      { return Detected() }

func (Adapter) Install(binPath string) (backups []string, err error) {
	return Install(binPath)
}

func (Adapter) Uninstall() error {
	return Uninstall()
}

// trustNote is carried on every integrated state. Codex will not run a hook
// until the user approves it, and an unapproved hook is a silent no-op — so an
// install that looks perfect on disk can be recording nothing. Status must say
// so rather than let the tier imply otherwise.
const trustNote = "hooks need one-time approval in Codex (/hooks); until approved they do not run"

func (Adapter) State() (assistant.State, error) {
	s := assistant.State{Detected: Detected(), HookEventsWanted: ExpectedEventCount()}
	events, err := InstalledEvents()
	if err != nil {
		return s, err
	}
	s.HookEvents = len(events)
	registered, err := MCPRegistered()
	if err != nil {
		return s, err
	}
	s.MCPRegistered = registered
	s.Tier = assistant.DeriveTier(s.HookEvents, s.HookEventsWanted, s.MCPRegistered)
	if s.HookEvents > 0 {
		s.Notes = append(s.Notes, trustNote)
	}
	return s, nil
}

// PostInstallNotice tells the user about the approval step Codex requires
// before any installed hook runs (assistant.PostInstallNotice). It names the
// silence explicitly: an untrusted install reports no error anywhere, so a user
// who skips this step has no other signal that nothing is being recorded.
func (Adapter) PostInstallNotice() string {
	return "  One manual step left: Codex will not run a hook until you approve it.\n" +
		"  Open codex, run /hooks, and trust the agent-brain entries.\n" +
		"  Until then agent-brain records nothing from Codex sessions and reports no error."
}

// BackfillUsage recovers absolute token totals from the session's rollout JSONL
// at session-end (assistant.UsageBackfiller). No Codex hook payload carries
// token counts, so this is the only source; a missing path or a count-free file
// yields ok=false and the session keeps its zero counters.
func (Adapter) BackfillUsage(input assistant.HookInput) (store.Usage, string, []store.ModelUsage, bool) {
	if input.TranscriptPath == "" {
		return store.Usage{}, "", nil, false
	}
	return ParseRollout(input.TranscriptPath)
}

// ScanCitations recovers memory ids the assistant restated in its replies
// during the session (assistant.CitationScanner). ok reports only whether the
// scan ran; an empty result from a readable file is a legitimate "no
// citations".
func (Adapter) ScanCitations(input assistant.HookInput) ([]int64, []string, bool) {
	if input.TranscriptPath == "" {
		return nil, nil, false
	}
	return ScanMemoryCitations(input.TranscriptPath)
}

func (Adapter) ParseHook(event string, r io.Reader) (assistant.HookInput, error) {
	p, err := parsePayload(r)
	if err != nil {
		return assistant.HookInput{}, err
	}
	return assistant.HookInput{
		SessionKey:     p.SessionID,
		Cwd:            p.Cwd,
		TranscriptPath: p.TranscriptPath,
		ToolName:       p.ToolName,
		Model:          p.Model,
	}, nil
}

// InjectionResponse wraps the memory pack in Codex's SessionStart
// context-injection envelope. Codex reuses Claude Code's hookSpecificOutput
// shape verbatim; a probe confirmed the injected text reaches the model.
func (Adapter) InjectionResponse(pack string) string {
	return injectionEnvelope("SessionStart", pack, "")
}

// InjectionResponseWithNotice additionally sets the top-level systemMessage
// (assistant.InjectionNotifier). The notice is display-only and never enters
// the model's context.
func (Adapter) InjectionResponseWithNotice(pack, notice string) string {
	return injectionEnvelope("SessionStart", pack, notice)
}

// PromptInjectionResponse wraps a dedup-walk memory pack in Codex's
// UserPromptSubmit envelope, refreshing memory before the model acts on each
// prompt (assistant.PromptInjector).
func (Adapter) PromptInjectionResponse(pack string) string {
	return injectionEnvelope("UserPromptSubmit", pack, "")
}

func injectionEnvelope(hookEventName, pack, notice string) string {
	m := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     hookEventName,
			"additionalContext": pack,
		},
	}
	if notice != "" {
		m["systemMessage"] = notice
	}
	out, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(out)
}

// StopResponse wraps the distillation prompt in Codex's Stop decision envelope.
// On Stop, Codex treats decision:block as "continue, with reason as a new
// prompt" — the distillation redirect's exact semantics. Codex rejects a block
// carrying an empty reason, which the caller already avoids by skipping the
// envelope when there is nothing to ask for.
func (Adapter) StopResponse(reason string) string {
	out, err := json.Marshal(map[string]string{
		"decision": "block",
		"reason":   reason,
	})
	if err != nil {
		return ""
	}
	return string(out)
}
