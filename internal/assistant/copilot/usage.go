package copilot

import (
	"bufio"
	"encoding/json"
	"os"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

const maxEventLineBytes = 16 << 20

// eventLine is the assumed shape of one line in Copilot CLI's
// ~/.copilot/session-state/<sessionId>/events.jsonl, per community
// documentation (no first-party schema commitment): a stream of newline-
// delimited event objects, most of which carry only a "type". The event that
// closes a session, "session.shutdown", is documented to additionally carry
// cumulative token totals under "usage" and the model name under "model".
// Any other shape (missing usage, different key names, a future rename) is
// treated as absence rather than guessed at.
type eventLine struct {
	Type  string `json:"type"`
	Model string `json:"model"`
	Usage *struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"usage"`
}

// ParseEvents best-effort extracts cumulative token usage and the model name
// from a Copilot CLI events.jsonl session log. It scans tolerantly: malformed
// lines are skipped, and only the last "session.shutdown" event carrying a
// usage object is trusted. ok is false — with usage left zero-valued — when
// no such event is found, so callers never write fabricated data (clarify
// Q1).
func ParseEvents(path string) (usage store.Usage, model string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return store.Usage{}, "", false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxEventLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		var el eventLine
		if err := json.Unmarshal(line, &el); err != nil {
			continue
		}
		if el.Type != "session.shutdown" || el.Usage == nil {
			continue
		}
		usage = store.Usage{
			Input:     el.Usage.InputTokens,
			Output:    el.Usage.OutputTokens,
			CacheRead: el.Usage.CachedTokens,
		}
		model = el.Model
		ok = true
	}
	if !ok {
		return store.Usage{}, "", false
	}
	return usage, model, true
}
