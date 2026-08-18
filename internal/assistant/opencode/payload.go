package opencode

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

const maxPayloadBytes = 10 << 20

// payload holds the only fields the owned shim ever emits. It is our own
// schema (there is no external hook wire format to match — the shim
// constructs this JSON itself from OpenCode's typed SDK events), so nothing
// beyond session id, cwd, model, and usage is representable here: prompt
// text, tool bodies, and message content never enter this struct.
type payload struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Model     string `json:"model"`
	Usage     *struct {
		Input      int64 `json:"input"`
		Output     int64 `json:"output"`
		Reasoning  int64 `json:"reasoning"`
		CacheRead  int64 `json:"cache_read"`
		CacheWrite int64 `json:"cache_write"`
	} `json:"usage"`
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

// Adapter is the OpenCode implementation of assistant.Adapter.
type Adapter struct{}

func init() {
	assistant.Register(assistant.OrderOpenCode, Adapter{})
}

func (Adapter) Name() string        { return Assistant }
func (Adapter) DisplayName() string { return "OpenCode" }
func (Adapter) Detected() bool      { return Detected() }

func (Adapter) Install(binPath string) (backups []string, err error) {
	return Install(binPath)
}

func (Adapter) Uninstall() error {
	return Uninstall()
}

func (Adapter) State() (assistant.State, error) {
	return State()
}

func (Adapter) ParseHook(event string, r io.Reader) (assistant.HookInput, error) {
	p, err := parsePayload(r)
	if err != nil {
		return assistant.HookInput{}, err
	}
	out := assistant.HookInput{
		SessionKey: p.SessionID,
		Cwd:        p.Cwd,
	}
	if event == assistant.EventModelUsage {
		out.Model = p.Model
		if p.Usage != nil {
			out.Usage = &store.Usage{
				Input: p.Usage.Input,
				// store.Usage has no separate reasoning column; OpenCode
				// bills reasoning tokens as part of the completion, so they
				// fold into Output rather than being dropped.
				Output:     p.Usage.Output + p.Usage.Reasoning,
				CacheRead:  p.Usage.CacheRead,
				CacheWrite: p.Usage.CacheWrite,
			}
		}
	}
	return out, nil
}

// InjectionResponse is unimplemented: not exercised live in the spike (see
// plans/spikes/opencode.md §5). Returning "" honestly skips the stage rather
// than guessing at an envelope.
func (Adapter) InjectionResponse(pack string) string { return "" }

// StopResponse is unimplemented for the same reason as InjectionResponse.
func (Adapter) StopResponse(reason string) string { return "" }
