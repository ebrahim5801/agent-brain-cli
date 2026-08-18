package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
)

type citationLine struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

var (
	personalCitationRe = regexp.MustCompile(`(?:memory\s+#|\[#|#)(\d+)`)
	teamCitationRe     = regexp.MustCompile(`team#([0-9a-f]{8,})`)
)

// ScanMemoryCitations scans a Claude Code session transcript for memory ids
// the assistant restated in its replies. Only assistant text blocks are
// scanned (never user or tool content); each line is JSON-decoded into the
// minimal struct above and discarded — matched text is never retained or
// returned, matching the redaction posture of ParseTranscript. The returned
// sets are deduped; callers intersect them against the session's actual
// retrievals before recording a citation (the false-positive guard lives with
// the caller, not here).
func ScanMemoryCitations(path string) (personalIDs []int64, teamHandles []string, err error) {
	f, openErr := os.Open(path)
	if openErr != nil {
		return nil, nil, openErr
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxLineBytes)

	seenIDs := map[int64]bool{}
	seenHandles := map[string]bool{}

	for scanner.Scan() {
		line := scanner.Bytes()
		var cl citationLine
		if jsonErr := json.Unmarshal(line, &cl); jsonErr != nil {
			continue
		}
		if cl.Type != "assistant" {
			continue
		}
		for _, block := range cl.Message.Content {
			if block.Type != "text" {
				continue
			}
			for _, m := range personalCitationRe.FindAllStringSubmatch(block.Text, -1) {
				id, convErr := strconv.ParseInt(m[1], 10, 64)
				if convErr != nil {
					continue
				}
				if !seenIDs[id] {
					seenIDs[id] = true
					personalIDs = append(personalIDs, id)
				}
			}
			for _, m := range teamCitationRe.FindAllStringSubmatch(block.Text, -1) {
				handle := m[1]
				if !seenHandles[handle] {
					seenHandles[handle] = true
					teamHandles = append(teamHandles, handle)
				}
			}
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return personalIDs, teamHandles, scanErr
	}
	return personalIDs, teamHandles, nil
}
