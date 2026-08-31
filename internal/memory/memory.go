// Package memory owns the domain logic for personal per-project memory:
// validated, redacted saves with capture-time git state and supersede
// transitions, plus ranking, pack building, and retrieval. Every write path
// (MCP tool, CLI, hooks) goes through this package — nothing else touches the
// memories table directly.
package memory

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/redact"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/wire"
)

const MaxContentLen = 4000

var (
	Kinds      = wire.MemoryKinds
	Priorities = wire.MemoryPriorities
)

const (
	PriorityCritical   = wire.MemoryPriorityCritical
	PriorityNormal     = wire.MemoryPriorityNormal
	PriorityBackground = wire.MemoryPriorityBackground
)

const (
	OriginAuto     = "auto"
	OriginExplicit = "explicit"
)

var (
	ErrEmptyContent   = errors.New("content is empty after redaction")
	ErrContentTooLong = fmt.Errorf("content exceeds %d characters", MaxContentLen)
	ErrBadKind        = fmt.Errorf("kind must be one of: %s", strings.Join(Kinds, ", "))
	ErrBadOrigin      = errors.New(`origin must be "auto" or "explicit"`)
	ErrBadPriority    = fmt.Errorf("priority must be one of: %s", strings.Join(Priorities, ", "))
)

type SaveInput struct {
	ProjectID int64
	SessionID int64 // 0 when outside a tracked session
	Dir       string
	Content   string
	Kind      string
	Origin    string
	// Priority governs how hard the entry competes for the session-start pack
	// budget. Empty normalizes to "normal" so a caller that does not classify
	// is never forced to; an unrecognized value is ErrBadPriority.
	Priority   string
	Supersedes []int64
	// SupersedesTeam holds handles of cached team entries this save replaces or
	// contradicts (as rendered in the pack, e.g. "team#abc12345"). Each is mapped
	// to its team_uid and attached to the entry's contribution so the server
	// applies its same-author/cross-author rule end to end (FR-013).
	SupersedesTeam []string
	// PersonalOnly marks the entry as personal context, never contributed to a
	// team pool (FR-005). ShareLive means the project's sharing consent is
	// granted and unpaused, so a team_uid is minted at insert (T004 rules).
	PersonalOnly bool
	ShareLive    bool
}

type SaveResult struct {
	ID         int64
	Superseded []int64
	// Rejected maps supersede IDs that were refused to the reason.
	Rejected map[int64]string
	// SupersededTeam holds the team handles accepted for cross-author/same-author
	// retirement (queued for contribution); RejectedTeam maps a refused handle to
	// the reason (unknown, ambiguous, or not shareable).
	SupersededTeam []string
	RejectedTeam   map[string]string
}

// Save validates, redacts, records capture-time git state, inserts the entry,
// and applies supersede transitions. Invalid supersede IDs are reported
// per-ID without failing the save (contracts/mcp-memory.md).
func Save(st *store.Store, in SaveInput) (SaveResult, error) {
	if !validKind(in.Kind) {
		return SaveResult{}, ErrBadKind
	}
	if in.Origin != OriginAuto && in.Origin != OriginExplicit {
		return SaveResult{}, ErrBadOrigin
	}
	priority, ok := normalizePriority(in.Priority)
	if !ok {
		return SaveResult{}, ErrBadPriority
	}
	content := strings.TrimSpace(redact.Apply(stripToolArtifacts(in.Content)))
	if content == "" {
		return SaveResult{}, ErrEmptyContent
	}
	if len(content) > MaxContentLen {
		return SaveResult{}, ErrContentTooLong
	}

	gs := gitstate.Capture(in.Dir)
	at := store.Now()
	id, err := st.InsertMemory(store.NewMemory{
		ProjectID:    in.ProjectID,
		SessionID:    in.SessionID,
		Content:      content,
		Kind:         in.Kind,
		Origin:       in.Origin,
		Priority:     priority,
		Branch:       gs.Branch,
		Commit:       gs.Commit,
		HasRepo:      gs.HasRepo,
		PersonalOnly: in.PersonalOnly,
		ShareLive:    in.ShareLive,
	}, at)
	if err != nil {
		return SaveResult{}, err
	}

	res := SaveResult{ID: id, Rejected: map[int64]string{}, RejectedTeam: map[string]string{}}
	for _, oldID := range in.Supersedes {
		if oldID == id {
			res.Rejected[oldID] = "cannot supersede itself"
			continue
		}
		err := st.SupersedeMemory(oldID, id, in.ProjectID, at)
		switch {
		case err == nil:
			res.Superseded = append(res.Superseded, oldID)
		case errors.Is(err, store.ErrMemoryNotFound):
			res.Rejected[oldID] = "not an active entry of this project"
		default:
			return res, err
		}
	}
	for _, raw := range in.SupersedesTeam {
		handle := normalizeTeamHandle(raw)
		if handle == "" {
			continue
		}
		// A personal-only entry never reaches the pool, so it can never retire a
		// team entry — say so rather than silently storing a dead mapping.
		if in.PersonalOnly {
			res.RejectedTeam[handle] = "a personal_only entry is never shared, so it cannot supersede a team entry"
			continue
		}
		uid, err := st.ResolveTeamHandle(in.ProjectID, handle)
		switch {
		case err == nil:
			if err := st.AddTeamSupersede(id, uid); err != nil {
				return res, err
			}
			res.SupersededTeam = append(res.SupersededTeam, handle)
		case errors.Is(err, store.ErrMemoryNotFound):
			res.RejectedTeam[handle] = "no active team entry with this handle"
		case errors.Is(err, store.ErrAmbiguousTeamHandle):
			res.RejectedTeam[handle] = "handle matches more than one team entry; use more characters"
		default:
			return res, err
		}
	}
	return res, nil
}

// normalizeTeamHandle strips the rendered decoration a model may echo back
// ("[team#abc]", "team#abc", "abc") down to the bare uid prefix used for lookup.
func normalizeTeamHandle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	s = strings.TrimPrefix(s, "team#")
	return strings.TrimSpace(s)
}

// stripToolArtifacts removes function-call syntax the in-session model
// sometimes leaks into the content it passes to memory_save (observed:
// trailing "</parameter>" / "</invoke>" tags). Only tool-syntax tag names
// are stripped, and only at the ends of the content, so legitimate prose
// about XML/HTML is untouched.
var toolArtifact = regexp.MustCompile(`(?i)^\s*</?(?:[a-z][\w.-]*:)?(?:parameter|invoke|function_calls|tool_call)\b[^>]*>\s*|\s*</?(?:[a-z][\w.-]*:)?(?:parameter|invoke|function_calls|tool_call)\b[^>]*>\s*$`)

func stripToolArtifacts(s string) string {
	for {
		t := toolArtifact.ReplaceAllString(s, "")
		if t == s {
			return t
		}
		s = t
	}
}

// Edit re-redacts developer-updated content and stamps the edited marker;
// provenance (branch, commit, captured_at) is immutable.
func Edit(st *store.Store, id int64, content string) error {
	content = strings.TrimSpace(redact.Apply(stripToolArtifacts(content)))
	if content == "" {
		return ErrEmptyContent
	}
	if len(content) > MaxContentLen {
		return ErrContentTooLong
	}
	return st.UpdateMemoryContent(id, content, store.Now())
}

// SetPriority reclassifies an existing entry. An empty priority is rejected
// here rather than normalized: on the save path empty means "did not classify",
// but a reclassification command with no value is a mistake, not a default.
func SetPriority(st *store.Store, id int64, priority string) error {
	if priority == "" {
		return ErrBadPriority
	}
	p, ok := normalizePriority(priority)
	if !ok {
		return ErrBadPriority
	}
	return st.UpdateMemoryPriority(id, p, store.Now())
}

// ValidPriority reports whether p is in the vocabulary. Callers that filter
// rather than write use it to reject a typo up front: a filter on a value that
// cannot exist returns an empty result, which reads as "this project has no
// critical entries" rather than as a mistake.
func ValidPriority(p string) bool {
	for _, valid := range Priorities {
		if p == valid {
			return true
		}
	}
	return false
}

// normalizePriority maps an empty priority to the default and reports whether
// the value is in the vocabulary.
func normalizePriority(p string) (string, bool) {
	if p == "" {
		return PriorityNormal, true
	}
	for _, valid := range Priorities {
		if p == valid {
			return p, true
		}
	}
	return "", false
}

func validKind(kind string) bool {
	for _, k := range Kinds {
		if k == kind {
			return true
		}
	}
	return false
}
