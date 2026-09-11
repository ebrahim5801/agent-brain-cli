package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

const maxLineBytes = 16 << 20

// Line-type markers, tested against the raw bytes before any JSON decode. The
// two line types that carry usage are a small minority of a rollout file —
// response_item and event_msg dominate its bytes — and the SessionEnd hook has
// only 3 seconds to work in, so skipping the majority without decoding it is
// what keeps the parse inside the clamp. A false positive costs one wasted
// decode, never a wrong answer: the decoded type is still checked.
var (
	usageMarker      = []byte("token_usage_record")
	turnMarker       = []byte("turn_context")
	assistantMarker  = []byte("output_text")
	responseItemType = "response_item"
)

// tokenCounts is Codex's usage object, which appears three times on every
// token_usage_record: `usage` (the delta for one model response),
// `turn_token_usage` (running total for the turn) and `thread_token_usage`
// (running total for the whole thread). Verified against codex-cli 0.154.0.
type tokenCounts struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// usage folds reasoning tokens into Output. Codex bills and reports them
// separately, and store.Usage has no column for them, so the split is lost
// here — the same lossy fold opencode's parser makes, pinned by a test so it
// changes deliberately rather than by accident.
func (c tokenCounts) usage() store.Usage {
	return store.Usage{
		Input:      c.InputTokens,
		Output:     c.OutputTokens + c.ReasoningOutputTokens,
		CacheRead:  c.CachedInputTokens,
		CacheWrite: c.CacheWriteInputTokens,
	}
}

// rolloutLine is the whitelist for a rollout JSONL line. Everything else those
// lines carry is structurally unreachable: session_meta embeds the entire
// system prompt under base_instructions, response_item carries the full
// conversation and every tool call with its output, and turn_context carries
// the workspace paths and permission profile. None of those fields exist on
// this struct, so no caller can reach them.
type rolloutLine struct {
	Type    string `json:"type"`
	Payload struct {
		TurnID           string       `json:"turn_id"`
		Model            string       `json:"model"`
		TurnTokenUsage   *tokenCounts `json:"turn_token_usage"`
		ThreadTokenUsage *tokenCounts `json:"thread_token_usage"`
	} `json:"payload"`
}

// ParseRollout recovers a Codex session's absolute token usage, dominant model,
// and per-model breakdown from the rollout JSONL that every hook payload names
// as transcript_path. ok is false — with usage left zero — for a missing,
// unreadable, or count-free file, so a session keeps honest zeros rather than
// fabricated ones.
//
// Both totals are last-wins, which is what makes a re-parse idempotent and lets
// the reconcile sweep re-read a file safely:
//
//   - thread_token_usage is cumulative over the whole thread, so the last one
//     in the file is the session's absolute total.
//   - turn_token_usage is cumulative WITHIN a turn — a 19-record turn ends at
//     that turn's total, it does not contribute 19 addends — so the breakdown
//     takes the last record per turn_id and sums those across turns. Summing
//     every record instead overcounts several-fold.
//
// Measured against a real session: the per-turn last values summed to exactly
// the final thread_token_usage, so the breakdown reconciles with the scalar.
func ParseRollout(path string) (store.Usage, string, []store.ModelUsage, bool) {
	f, err := os.Open(path)
	if err != nil {
		return store.Usage{}, "", nil, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxLineBytes)

	var total store.Usage
	haveTotal := false
	modelOfTurn := map[string]string{} // turn_id -> model, from turn_context
	usageOfTurn := map[string]store.Usage{}
	fallbackModel := map[string]string{} // turn_id -> model in force when the record was seen

	currentModel := ""
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, usageMarker) && !bytes.Contains(line, turnMarker) {
			continue
		}
		var rl rolloutLine
		if err := json.Unmarshal(line, &rl); err != nil {
			continue
		}
		switch rl.Type {
		case "turn_context":
			if rl.Payload.Model == "" {
				continue
			}
			currentModel = rl.Payload.Model
			if rl.Payload.TurnID != "" {
				modelOfTurn[rl.Payload.TurnID] = rl.Payload.Model
			}
		case "token_usage_record":
			if rl.Payload.ThreadTokenUsage != nil {
				total = rl.Payload.ThreadTokenUsage.usage()
				haveTotal = true
			}
			// A record with no turn_id cannot be attributed to a turn, so it
			// is left out of the breakdown rather than merged into an
			// arbitrary bucket. It still counts toward the absolute total.
			if rl.Payload.TurnTokenUsage != nil && rl.Payload.TurnID != "" {
				usageOfTurn[rl.Payload.TurnID] = rl.Payload.TurnTokenUsage.usage()
				fallbackModel[rl.Payload.TurnID] = currentModel
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return store.Usage{}, "", nil, false
	}
	if !haveTotal {
		return store.Usage{}, "", nil, false
	}

	// turn_context is written before its turn's usage records, so the model in
	// force at record time is normally right; resolving by turn_id afterwards
	// keeps it right even if a future version reorders them.
	perModel := map[string]store.Usage{}
	for turnID, u := range usageOfTurn {
		model, ok := modelOfTurn[turnID]
		if !ok {
			model = fallbackModel[turnID]
		}
		m := perModel[model]
		m.Input += u.Input
		m.Output += u.Output
		m.CacheRead += u.CacheRead
		m.CacheWrite += u.CacheWrite
		perModel[model] = m
	}
	dominant, breakdown := assistant.ModelBreakdown(perModel)
	return total, dominant, breakdown, true
}

// citationLine is the whitelist for the assistant replies a citation scan
// reads. Only `text` is decoded, and only from a response_item message; the
// tool calls and tool output that share the response_item type carry their
// content under different keys that are absent here.
type citationLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
}

// ScanMemoryCitations scans a Codex rollout JSONL for memory ids the assistant
// restated in its replies. Only assistant messages are read — user and
// developer blocks arrive as input_text and are never scanned, so a memory id
// the user typed is not mistaken for the assistant recalling it. Matched text
// is discarded, never retained.
func ScanMemoryCitations(path string) ([]int64, []string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxLineBytes)

	var found assistant.CitationSet
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, assistantMarker) {
			continue
		}
		var cl citationLine
		if err := json.Unmarshal(line, &cl); err != nil {
			continue
		}
		if cl.Type != responseItemType || cl.Payload.Type != "message" || cl.Payload.Role != "assistant" {
			continue
		}
		for _, block := range cl.Payload.Content {
			if block.Type != "output_text" {
				continue
			}
			found.Scan(block.Text)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, false
	}
	personalIDs, teamHandles := found.Result()
	return personalIDs, teamHandles, true
}
