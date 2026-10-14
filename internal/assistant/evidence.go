package assistant

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// UserSource is transient verification output. Callers may persist only a
// redacted, bounded relevant excerpt and its digest, never Text/transcripts.
type UserSource struct{ Text, Digest, Reference string }

func textParts(raw json.RawMessage) string {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Type == "text" || p.Type == "input_text" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// ReadUserReference inspects a bounded local stream only for an exact native
// message ID or 1-based line reference. Role is derived from host data, never
// accepted from the model's evidence input. Unsupported formats fail closed.
func ReadUserReference(assistantName, path, reference, expectedDigest string) (UserSource, bool) {
	if path == "" || reference == "" || (expectedDigest != "" && len(expectedDigest) != 64) {
		return UserSource{}, false
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 64<<20 {
		return UserSource{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return UserSource{}, false
	}
	defer f.Close()
	scan := bufio.NewScanner(io.LimitReader(f, 64<<20))
	scan.Buffer(make([]byte, 4096), 1<<20)
	line := 0
	var found *UserSource
	for scan.Scan() {
		line++
		var row struct {
			IsSidechain         bool            `json:"isSidechain"`
			IsMeta              bool            `json:"isMeta"`
			IsSynthetic         bool            `json:"isSynthetic"`
			IsCompactSummary    bool            `json:"isCompactSummary"`
			SourceAssistantUUID string          `json:"sourceAssistantUUID"`
			ParentToolUseSnake  string          `json:"parent_tool_use_id"`
			ParentToolUseID     string          `json:"parentToolUseID"`
			ToolUseResult       json.RawMessage `json:"toolUseResult"`
			Type                string          `json:"type"`
			UUID                string          `json:"uuid"`
			ID                  string          `json:"id"`
			Message             struct {
				ID      string          `json:"id"`
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Payload struct {
				Source  json.RawMessage `json:"source"`
				Type    string          `json:"type"`
				ID      string          `json:"id"`
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(scan.Bytes(), &row) != nil {
			continue
		}
		if row.IsSidechain || row.IsMeta || row.IsSynthetic || row.IsCompactSummary || row.SourceAssistantUUID != "" || row.ParentToolUseID != "" || row.ParentToolUseSnake != "" || (len(row.ToolUseResult) > 0 && string(row.ToolUseResult) != "null") {
			continue
		}
		if assistantName == "codex" && row.Type == "session_meta" && strings.Contains(strings.ToLower(string(row.Payload.Source)), "subagent") {
			return UserSource{}, false
		}
		var id, text string
		user := false
		switch assistantName {
		case "claude-code":
			id = row.UUID
			user = row.Type == "user" && row.Message.Role == "user"
		case "codex":
			id = row.Payload.ID
			user = row.Type == "response_item" && row.Payload.Type == "message" && row.Payload.Role == "user"
		default:
			return UserSource{}, false
		}
		if reference != "latest" && reference != id && reference != fmt.Sprintf("line:%d", line) {
			continue
		}
		if assistantName == "claude-code" {
			text = textParts(row.Message.Content)
		} else {
			text = textParts(row.Payload.Content)
		}
		if reference == "latest" && (!user || text == "") {
			continue
		}
		if !user || text == "" || len(text) > 32768 || (found != nil && reference != "latest") {
			return UserSource{}, false
		}
		h := sha256.Sum256([]byte(text))
		digest := hex.EncodeToString(h[:])
		if reference != "latest" && expectedDigest != "" && digest != expectedDigest {
			return UserSource{}, false
		}
		ref := id
		if ref == "" {
			ref = fmt.Sprintf("line:%d", line)
		}
		value := UserSource{Text: text, Digest: digest, Reference: ref}
		found = &value
	}
	if scan.Err() != nil || found == nil || (expectedDigest != "" && found.Digest != expectedDigest) {
		return UserSource{}, false
	}
	return *found, true
}
