package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

const maxLineBytes = 16 << 20

type transcriptLine struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseTranscript sums token usage from a Claude Code session transcript and
// returns the aggregate usage, the dominant model, and a per-model breakdown
// (one entry per model the session used, heaviest first). A session that
// switched model mid-run yields several breakdown entries; the dominant model
// is the primary one carried on the session's scalar column. Usage blocks
// repeat across the multiple JSONL lines of one API call, so sums are
// deduplicated by message ID (verified against Claude Code 2.1.198). Each line
// is JSON-decoded into the minimal struct above and discarded; message content
// is never retained or returned.
func ParseTranscript(path string) (store.Usage, string, []store.ModelUsage, error) {
	f, err := os.Open(path)
	if err != nil {
		return store.Usage{}, "", nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxLineBytes)

	var usage store.Usage
	seen := map[string]bool{}
	perModel := map[string]store.Usage{}

	for scanner.Scan() {
		line := scanner.Bytes()
		var tl transcriptLine
		if err := json.Unmarshal(line, &tl); err != nil {
			continue
		}
		if tl.Type != "assistant" || tl.Message.Usage == nil {
			continue
		}
		key := tl.Message.ID
		if key == "" {
			key = tl.RequestID
		}
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		u := tl.Message.Usage
		usage.Input += u.InputTokens
		usage.Output += u.OutputTokens
		usage.CacheRead += u.CacheReadInputTokens
		usage.CacheWrite += u.CacheCreationInputTokens
		m := perModel[tl.Message.Model]
		m.Input += u.InputTokens
		m.Output += u.OutputTokens
		m.CacheRead += u.CacheReadInputTokens
		m.CacheWrite += u.CacheCreationInputTokens
		perModel[tl.Message.Model] = m
	}
	model, breakdown := modelBreakdown(perModel)
	if err := scanner.Err(); err != nil {
		return usage, model, breakdown, err
	}
	return usage, model, breakdown, nil
}

// modelBreakdown turns a per-model usage map into a deterministic slice ordered
// heaviest first (by total tokens, then model name) and picks the dominant
// model — the one with the most input+output tokens, matching the historical
// single-model selection. The empty model key (usage a transcript line omitted
// a model for) is dropped from both.
func modelBreakdown(perModel map[string]store.Usage) (string, []store.ModelUsage) {
	rows := make([]store.ModelUsage, 0, len(perModel))
	for m, u := range perModel {
		if m == "" {
			continue
		}
		rows = append(rows, store.ModelUsage{Model: m, Usage: u})
	}
	sort.Slice(rows, func(i, j int) bool {
		ti := rows[i].Usage.Input + rows[i].Usage.Output + rows[i].Usage.CacheRead + rows[i].Usage.CacheWrite
		tj := rows[j].Usage.Input + rows[j].Usage.Output + rows[j].Usage.CacheRead + rows[j].Usage.CacheWrite
		if ti != tj {
			return ti > tj
		}
		return rows[i].Model < rows[j].Model
	})
	var dominant string
	var max int64 = -1
	for _, r := range rows {
		if n := r.Usage.Input + r.Usage.Output; n > max {
			dominant, max = r.Model, n
		}
	}
	return dominant, rows
}

// dominantModel picks the model with the most input+output tokens, ignoring the
// empty key. Used by the sub-session parser, which needs only the single model
// (sub-sessions are not given a per-model breakdown).
func dominantModel(tokens map[string]int64) string {
	var best string
	var max int64 = -1
	for model, n := range tokens {
		if model == "" {
			continue
		}
		if n > max {
			best, max = model, n
		}
	}
	return best
}
