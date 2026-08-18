package claude

import (
	"encoding/json"
	"io"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// Adapter is the Claude Code implementation of assistant.Adapter. It is a thin
// delegation onto the package's existing functions: the interface was extracted
// from this package's real API, so behavior is byte-identical to before the
// multi-assistant seam (FR-002). The bare identifiers below (Install, Uninstall,
// Detected, …) resolve to the package-level functions, not the methods.
type Adapter struct{}

func init() {
	assistant.Register(assistant.OrderClaude, Adapter{})
}

func (Adapter) Name() string        { return Assistant }
func (Adapter) DisplayName() string { return "Claude Code" }
func (Adapter) Detected() bool      { return Detected() }

func (Adapter) Install(binPath string) (backups []string, err error) {
	backup, err := Install(binPath)
	if err != nil {
		return backups, err
	}
	if backup != "" {
		backups = append(backups, backup)
	}
	if err := RegisterMCP(binPath); err != nil {
		return backups, err
	}
	return backups, nil
}

func (Adapter) Uninstall() error {
	if err := Uninstall(); err != nil {
		return err
	}
	return UnregisterMCP()
}

func (Adapter) State() (assistant.State, error) {
	s := assistant.State{Detected: Detected(), HookEventsWanted: ExpectedEventCount()}
	events, err := InstalledEvents()
	if err != nil {
		return s, err
	}
	s.HookEvents = len(events)
	registered, _, err := MCPRegistered()
	if err != nil {
		return s, err
	}
	s.MCPRegistered = registered
	s.Tier = assistant.DeriveTier(s.HookEvents, s.HookEventsWanted, s.MCPRegistered)
	return s, nil
}

func (Adapter) ParseHook(event string, r io.Reader) (assistant.HookInput, error) {
	p, err := ParsePayload(r)
	if err != nil {
		return assistant.HookInput{}, err
	}
	return assistant.HookInput{
		SessionKey:     p.SessionID,
		Cwd:            p.Cwd,
		TranscriptPath: p.TranscriptPath,
		ToolName:       p.ToolName,
	}, nil
}

// BackfillUsage recovers absolute token totals from the session transcript at
// session-end. A missing path or parse failure yields ok=false (honest
// absence — the session keeps its zero counters).
func (Adapter) BackfillUsage(input assistant.HookInput) (store.Usage, string, []store.ModelUsage, bool) {
	if input.TranscriptPath == "" {
		return store.Usage{}, "", nil, false
	}
	usage, model, models, err := ParseTranscript(input.TranscriptPath)
	if err != nil {
		return store.Usage{}, "", nil, false
	}
	return usage, model, models, true
}

// ScanSubagents recovers the session's completed subagent runs from the
// subagents directory next to the transcript (assistant.SubagentScanner). The
// summary is the Task description plus the agent's final report — content that
// stays in the local store only, like the parent's distilled summary.
func (Adapter) ScanSubagents(input assistant.HookInput) []assistant.Subagent {
	if input.TranscriptPath == "" {
		return nil
	}
	var out []assistant.Subagent
	for _, sa := range ScanSubagents(input.TranscriptPath) {
		summary := sa.Description
		if sa.FinalReport != "" {
			if summary != "" {
				summary += "\n\n"
			}
			summary += sa.FinalReport
		}
		out = append(out, assistant.Subagent{
			AgentID:        sa.AgentID,
			AgentType:      sa.AgentType,
			Model:          sa.Model,
			StartedAt:      sa.StartedAt,
			EndedAt:        sa.EndedAt,
			TranscriptPath: sa.Transcript,
			Summary:        summary,
			Prompt:         sa.Prompt,
			Usage:          sa.Usage,
		})
	}
	return out
}

// ScanCitations recovers memory ids/handles the assistant restated in its
// replies during the session (assistant.CitationScanner). A missing path or
// scan failure yields ok=false — honest absence, mirroring BackfillUsage.
func (Adapter) ScanCitations(input assistant.HookInput) ([]int64, []string, bool) {
	if input.TranscriptPath == "" {
		return nil, nil, false
	}
	personalIDs, teamHandles, err := ScanMemoryCitations(input.TranscriptPath)
	if err != nil {
		return nil, nil, false
	}
	return personalIDs, teamHandles, true
}

// InjectionResponse wraps the memory pack in Claude Code's SessionStart
// context-injection envelope. The shape is byte-identical to the literal that
// previously lived inline in cli/hook_memory.go.
func (Adapter) InjectionResponse(pack string) string {
	return injectionEnvelope("SessionStart", pack, "")
}

// InjectionResponseWithNotice additionally sets Claude Code's top-level
// systemMessage, which the CLI shows to the user; the model only receives
// additionalContext (assistant.InjectionNotifier).
func (Adapter) InjectionResponseWithNotice(pack, notice string) string {
	return injectionEnvelope("SessionStart", pack, notice)
}

// PromptInjectionResponse wraps a dedup-walk memory pack in Claude Code's
// UserPromptSubmit context-injection envelope, refreshing memory before the
// model acts on each prompt (assistant.PromptInjector).
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

// StopResponse wraps the distillation prompt in Claude Code's Stop decision
// envelope, byte-identical to the previous inline literal.
func (Adapter) StopResponse(reason string) string {
	return blockDecision(reason)
}

// MidTurnResponse wraps the checkpoint prompt in Claude Code's blocking
// decision envelope, used on PostToolUse and PreCompact to make the model save
// interim memory before continuing (assistant.MidTurnResponder).
func (Adapter) MidTurnResponse(reason string) string {
	return blockDecision(reason)
}

func blockDecision(reason string) string {
	out, err := json.Marshal(map[string]string{
		"decision": "block",
		"reason":   reason,
	})
	if err != nil {
		return ""
	}
	return string(out)
}
