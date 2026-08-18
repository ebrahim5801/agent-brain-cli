package cursor

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
)

const maxPayloadBytes = 10 << 20

// payload holds the only hook fields the collector consumes. Everything
// else in Cursor's payload — prompt text, tool inputs/outputs, file
// contents — is structurally undecodable here and never reaches storage.
type payload struct {
	ConversationID string   `json:"conversation_id"`
	WorkspaceRoots []string `json:"workspace_roots"`
	TranscriptPath string   `json:"transcript_path"`
	Model          string   `json:"model"`
	ToolName       string   `json:"tool_name"`
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
	if p.ConversationID == "" {
		return p, errors.New("payload missing conversation_id")
	}
	return p, nil
}

func (Adapter) ParseHook(event string, r io.Reader) (assistant.HookInput, error) {
	p, err := parsePayload(r)
	if err != nil {
		return assistant.HookInput{}, err
	}
	in := assistant.HookInput{
		SessionKey:     p.ConversationID,
		TranscriptPath: p.TranscriptPath,
		Model:          p.Model,
		ToolName:       p.ToolName,
	}
	if len(p.WorkspaceRoots) > 0 {
		in.Cwd = p.WorkspaceRoots[0]
	}
	return in, nil
}

// PromptResponse always allows the submission: Cursor's beforeSubmitPrompt
// processes hook output, so an explicit continue beats relying on fail-open
// handling of empty stdout on a hook that fires for every prompt.
func (Adapter) PromptResponse() string {
	return `{"continue":true}`
}

// InjectionResponse wraps the memory pack in Cursor's sessionStart
// context-injection envelope.
func (Adapter) InjectionResponse(pack string) string {
	out, err := json.Marshal(map[string]string{"additional_context": pack})
	if err != nil {
		return ""
	}
	return string(out)
}

// ToolUseInjectionResponse injects a dedup-walk memory pack via Cursor's
// postToolUse additional_context — the fallback channel for keeping memory in
// context mid-task, since beforeSubmitPrompt cannot inject
// (assistant.ToolUseInjector). The envelope is the same additional_context
// shape sessionStart uses.
func (a Adapter) ToolUseInjectionResponse(pack string) string {
	return a.InjectionResponse(pack)
}

// StopResponse wraps the distillation prompt in Cursor's stop follow-up
// envelope.
func (Adapter) StopResponse(reason string) string {
	return followup(reason)
}

// MidTurnResponse wraps the checkpoint prompt in the same follow-up envelope,
// used on the tool-use hook to save interim memory mid-turn
// (assistant.MidTurnResponder).
func (Adapter) MidTurnResponse(reason string) string {
	return followup(reason)
}

func followup(reason string) string {
	out, err := json.Marshal(map[string]string{"followup_message": reason})
	if err != nil {
		return ""
	}
	return string(out)
}
