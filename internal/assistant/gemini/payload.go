package gemini

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

const maxPayloadBytes = 10 << 20

// payload holds the only hook fields the collector consumes. Everything
// else — prompt text, tool inputs/outputs, file contents, and the
// llm_response candidates/content/message bodies — is structurally
// unreachable here: usageMetadata is the only field this struct reaches
// inside llm_response.
type payload struct {
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	ToolName       string `json:"tool_name"`

	LLMRequest *struct {
		Model string `json:"model"`
	} `json:"llm_request"`

	LLMResponse *struct {
		UsageMetadata *struct {
			PromptTokenCount        int64 `json:"promptTokenCount"`
			CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
			CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
	} `json:"llm_response"`
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

// Adapter is the Gemini CLI implementation of assistant.Adapter.
type Adapter struct{}

func init() {
	assistant.Register(assistant.OrderGemini, Adapter{})
}

func (Adapter) Name() string        { return Assistant }
func (Adapter) DisplayName() string { return "Gemini CLI" }
func (Adapter) Detected() bool      { return Detected() }

func (Adapter) Install(binPath string) (backups []string, err error) {
	return Install(binPath)
}

func (Adapter) Uninstall() error {
	return Uninstall()
}

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
	return s, nil
}

func (Adapter) ParseHook(event string, r io.Reader) (assistant.HookInput, error) {
	p, err := parsePayload(r)
	if err != nil {
		return assistant.HookInput{}, err
	}
	out := assistant.HookInput{
		SessionKey:     p.SessionID,
		Cwd:            p.Cwd,
		TranscriptPath: p.TranscriptPath,
		ToolName:       p.ToolName,
	}
	if event == assistant.EventModelUsage {
		if p.LLMRequest != nil {
			out.Model = p.LLMRequest.Model
		}
		if p.LLMResponse != nil && p.LLMResponse.UsageMetadata != nil {
			um := p.LLMResponse.UsageMetadata
			if um.PromptTokenCount != 0 || um.CandidatesTokenCount != 0 || um.CachedContentTokenCount != 0 {
				out.Usage = &store.Usage{
					Input:      um.PromptTokenCount,
					Output:     um.CandidatesTokenCount,
					CacheRead:  um.CachedContentTokenCount,
					CacheWrite: 0,
				}
			}
		}
	}
	return out, nil
}

// InjectionResponse wraps the memory pack in Gemini CLI's SessionStart
// context-injection envelope, mirroring Claude Code's hookSpecificOutput
// shape.
func (Adapter) InjectionResponse(pack string) string {
	return injectionEnvelope("SessionStart", pack)
}

// PromptInjectionResponse wraps a dedup-walk memory pack in Gemini CLI's
// BeforeAgent context-injection envelope, refreshing memory before the model
// acts on each prompt (assistant.PromptInjector). The additionalContext field
// is identical to SessionStart; only the hookEventName differs.
func (Adapter) PromptInjectionResponse(pack string) string {
	return injectionEnvelope("BeforeAgent", pack)
}

func injectionEnvelope(hookEventName, pack string) string {
	out, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     hookEventName,
			"additionalContext": pack,
		},
	})
	if err != nil {
		return ""
	}
	return string(out)
}

// StopResponse wraps the distillation prompt in Gemini CLI's stop decision
// envelope.
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
