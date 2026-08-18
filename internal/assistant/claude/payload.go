package claude

import (
	"encoding/json"
	"errors"
	"io"
)

const maxPayloadBytes = 10 << 20

// Payload holds the only hook fields the collector consumes. Everything else
// in the payload — prompt text, tool inputs/outputs, file contents — is
// dropped here at the adapter boundary and never reaches storage.
type Payload struct {
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	HookEventName  string `json:"hook_event_name"`
	ToolName       string `json:"tool_name"`
}

func ParsePayload(r io.Reader) (Payload, error) {
	var p Payload
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
