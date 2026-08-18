package copilot

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

const maxPayloadBytes = 10 << 20

// payload holds the only hook fields the collector consumes. Everything else
// in the payload — prompt text, tool inputs/outputs, file contents — is
// dropped here at the adapter boundary and never reaches storage.
type payload struct {
	SessionID      string `json:"sessionId"`
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcriptPath"`
	ToolName       string `json:"toolName"`
}

// ParseHook decodes stdin into the neutral HookInput for one event. Usage and
// Model are never populated here: usage comes from the session-end backfill
// via ParseEvents, not the hook payload.
func (Adapter) ParseHook(event string, r io.Reader) (assistant.HookInput, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxPayloadBytes))
	if err != nil {
		return assistant.HookInput{}, err
	}
	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return assistant.HookInput{}, err
	}
	if p.SessionID == "" {
		return assistant.HookInput{}, errors.New("payload missing sessionId")
	}
	return assistant.HookInput{
		SessionKey:     p.SessionID,
		Cwd:            p.Cwd,
		TranscriptPath: p.TranscriptPath,
		ToolName:       p.ToolName,
	}, nil
}

// InjectionResponse wraps the memory pack in Copilot CLI's sessionStart
// context-injection envelope.
func (Adapter) InjectionResponse(pack string) string {
	out, err := json.Marshal(map[string]string{"additionalContext": pack})
	if err != nil {
		return ""
	}
	return string(out)
}

// ToolUseInjectionResponse injects a dedup-walk memory pack via Copilot CLI's
// postToolUse additionalContext — the fallback channel for keeping memory in
// context mid-task, since userPromptSubmitted output is not processed
// (assistant.ToolUseInjector). Same additionalContext shape as sessionStart.
func (a Adapter) ToolUseInjectionResponse(pack string) string {
	return a.InjectionResponse(pack)
}

// StopResponse wraps the distillation prompt in Copilot CLI's agentStop
// decision envelope (documented Claude Stop semantics: forces another agent
// turn using reason as the prompt).
func (Adapter) StopResponse(reason string) string {
	return blockDecision(reason)
}

// MidTurnResponse wraps the checkpoint prompt in the same blocking decision
// envelope, used on the tool-use hook to save interim memory mid-turn
// (assistant.MidTurnResponder).
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
