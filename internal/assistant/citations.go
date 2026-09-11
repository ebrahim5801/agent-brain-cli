package assistant

import (
	"regexp"
	"strconv"
)

// The shapes a memory id can take in an assistant's reply. They mirror how the
// pack renders entries — "[#12]" for a personal entry, "[team#abc12345]" for a
// team one — plus the looser forms an assistant writes when it paraphrases
// ("memory #12", "#12"). The matcher lives here rather than in one adapter
// because it tracks the pack format, which is agent-brain's, not any single
// assistant's: two adapters scanning the same format with drifting regexes
// would report different citations for identical replies.
var (
	personalCitationRe = regexp.MustCompile(`(?:memory\s+#|\[#|#)(\d+)`)
	teamCitationRe     = regexp.MustCompile(`team#([0-9a-f]{8,})`)
)

// CitationSet accumulates the memory ids and team handles an assistant
// restated across a session's replies, deduped and in first-seen order. The
// scanned text is matched and discarded — never retained — so a transcript's
// content cannot reach storage through a citation scan.
type CitationSet struct {
	personalIDs []int64
	teamHandles []string
	seenID      map[int64]bool
	seenHandle  map[string]bool
}

// Scan adds every memory citation in one block of assistant text. Callers pass
// assistant replies only; user and tool text is never a citation.
func (c *CitationSet) Scan(text string) {
	for _, m := range personalCitationRe.FindAllStringSubmatch(text, -1) {
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		if c.seenID == nil {
			c.seenID = map[int64]bool{}
		}
		if !c.seenID[id] {
			c.seenID[id] = true
			c.personalIDs = append(c.personalIDs, id)
		}
	}
	for _, m := range teamCitationRe.FindAllStringSubmatch(text, -1) {
		handle := m[1]
		if c.seenHandle == nil {
			c.seenHandle = map[string]bool{}
		}
		if !c.seenHandle[handle] {
			c.seenHandle[handle] = true
			c.teamHandles = append(c.teamHandles, handle)
		}
	}
}

// Result returns what was found. Callers intersect the ids against the
// session's actual retrievals before recording anything — the false-positive
// guard lives with the caller, not here.
func (c *CitationSet) Result() (personalIDs []int64, teamHandles []string) {
	return c.personalIDs, c.teamHandles
}
