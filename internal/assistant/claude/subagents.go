package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/wire"
)

// maxReportBytes caps the final-report, prompt, and description excerpts kept
// in the local store; the full text stays in the agent transcript on disk.
const maxReportBytes = 4000

// maxMetaBytes caps the meta.json read: the file normally holds a few short
// fields, so anything larger is hostile or corrupt and parses as neither.
const maxMetaBytes = 64 << 10

// Subagent is one completed subagent run recovered from the session's
// subagents directory. Description and FinalReport are content: they feed the
// local-only summary and never reach the wire (the sync schema has no field
// for them).
type Subagent struct {
	AgentID     string
	AgentType   string
	Description string
	Model       string
	StartedAt   string
	EndedAt     string
	Transcript  string
	Prompt      string
	FinalReport string
	Usage       store.Usage
}

type subagentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
}

// subagentsDir is the per-session directory Claude Code writes agent
// transcripts into: <project>/<session-id>/subagents next to
// <project>/<session-id>.jsonl.
func subagentsDir(mainTranscriptPath string) string {
	base := strings.TrimSuffix(mainTranscriptPath, ".jsonl")
	if base == mainTranscriptPath {
		return ""
	}
	return filepath.Join(base, "subagents")
}

// ScanSubagents lists the completed subagent runs recorded next to a session
// transcript (agent-<id>.jsonl + agent-<id>.meta.json, verified against Claude
// Code 2.x). A missing directory means no subagents ran; an unreadable agent
// file is skipped rather than failing the scan.
func ScanSubagents(mainTranscriptPath string) []Subagent {
	dir := subagentsDir(mainTranscriptPath)
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Subagent
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		path := filepath.Join(dir, name)
		sa, ok := parseAgentTranscript(path)
		if !ok {
			continue
		}
		sa.AgentID = strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
		sa.Transcript = path
		var meta subagentMeta
		if raw, err := readCapped(strings.TrimSuffix(path, ".jsonl")+".meta.json", maxMetaBytes); err == nil {
			if json.Unmarshal(raw, &meta) == nil {
				sa.AgentType = wire.SanitizeAgentType(meta.AgentType)
				sa.Description = truncateBytes(meta.Description, maxReportBytes)
			}
		}
		out = append(out, sa)
	}
	return out
}

type agentLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	RequestID string `json:"requestId"`
	Message   struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// parseAgentTranscript walks one agent JSONL: usage summed with the same
// message-id dedup as ParseTranscript, timestamps bounding the run, the
// dominant model, the first user message (the spawn prompt the parent
// assistant wrote), and the text of the last assistant message (the agent's
// report back to the parent).
func parseAgentTranscript(path string) (Subagent, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Subagent{}, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxLineBytes)

	var sa Subagent
	seen := map[string]bool{}
	modelTokens := map[string]int64{}
	any := false
	for scanner.Scan() {
		var al agentLine
		if err := json.Unmarshal(scanner.Bytes(), &al); err != nil {
			continue
		}
		if ts := normalizeTime(al.Timestamp); ts != "" {
			if sa.StartedAt == "" || ts < sa.StartedAt {
				sa.StartedAt = ts
			}
			if ts > sa.EndedAt {
				sa.EndedAt = ts
			}
			any = true
		}
		if al.Type == "user" && sa.Prompt == "" {
			sa.Prompt = contentText(al.Message.Content)
		}
		if al.Type != "assistant" {
			continue
		}
		if text := contentText(al.Message.Content); text != "" {
			sa.FinalReport = text
		}
		u := al.Message.Usage
		if u == nil {
			continue
		}
		key := al.Message.ID
		if key == "" {
			key = al.RequestID
		}
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		sa.Usage.Input += u.InputTokens
		sa.Usage.Output += u.OutputTokens
		sa.Usage.CacheRead += u.CacheReadInputTokens
		sa.Usage.CacheWrite += u.CacheCreationInputTokens
		modelTokens[al.Message.Model] += u.InputTokens + u.OutputTokens
	}
	sa.Model = dominantModel(modelTokens)
	sa.FinalReport = truncateBytes(sa.FinalReport, maxReportBytes)
	sa.Prompt = truncateBytes(sa.Prompt, maxReportBytes)
	return sa, any
}

// readCapped reads at most max bytes of a file; a larger file comes back
// truncated and fails the JSON parse instead of being allocated whole.
func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

// truncateBytes cuts s to at most n bytes without splitting a UTF-8 rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// contentText extracts the text of a message: user messages may carry a plain
// string, assistant messages an array of blocks (thinking and tool-use blocks
// are ignored).
func contentText(raw json.RawMessage) string {
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return plain
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// normalizeTime reformats a transcript timestamp into store.TimeLayout;
// unparsable values are dropped rather than stored in a foreign layout.
func normalizeTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return t.UTC().Format(store.TimeLayout)
}
