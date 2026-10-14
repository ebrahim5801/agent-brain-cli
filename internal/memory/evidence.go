package memory

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/redact"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

type EvidenceInput struct {
	Type      string `json:"type" jsonschema:"user_message or artifact. Model assertions are not verification."`
	Reference string `json:"reference" jsonschema:"Original user message ID or line:N, or repository-relative artifact path."`
	Digest    string `json:"digest" jsonschema:"SHA256 of original message text or artifact bytes; required to detect changed sources."`
	Excerpt   string `json:"excerpt,omitempty" jsonschema:"Relevant source excerpt; checked against the original and redacted before persistence."`
}

func verifyEvidence(st *store.Store, in SaveInput, content string) ([]store.MemoryEvidence, error) {
	if len(in.Evidence) > 16 {
		return nil, ErrContentTooLong
	}
	var out []store.MemoryEvidence
	for _, e := range in.Evidence {
		if len(e.Reference) > 512 || len(e.Excerpt) > 4000 || len(e.Digest) > 64 {
			return nil, ErrContentTooLong
		}
		checked := store.MemoryEvidence{Type: e.Type, Locator: redact.Apply(e.Reference), Digest: e.Digest, Excerpt: redact.Apply(e.Excerpt), Status: "unverified"}
		if decoded, err := hex.DecodeString(e.Digest); err != nil || len(decoded) != 32 {
			checked.Digest = ""
		}
		var raw string
		if checked.Digest == "" {
			checked.Excerpt = truncateRunes(checked.Excerpt, 500)
			out = append(out, checked)
			continue
		}
		switch e.Type {
		case "user_message":
			if in.SessionHandle != "" {
				b, err := st.ResolveBinding(in.SessionHandle, in.ProjectID, in.Dir)
				if err != nil {
					return nil, err
				}
				var path string
				if err := st.QueryRow(`SELECT COALESCE(transcript_path,'') FROM sessions WHERE id=? AND project_id=?`, b.SessionID, b.ProjectID).Scan(&path); err != nil {
					return nil, err
				}
				if user, ok := assistant.ReadUserReference(b.Assistant, path, e.Reference, e.Digest); ok {
					raw = user.Text
				}
			}
		case "artifact":
			if e.Reference != "" && !filepath.IsAbs(e.Reference) {
				root := store.CheckoutIdentity(in.Dir)
				path, err := filepath.EvalSymlinks(filepath.Join(root, e.Reference))
				if err == nil && strings.HasPrefix(path, root+string(filepath.Separator)) {
					if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Size() <= 32768 {
						if data, err := os.ReadFile(path); err == nil && digestText(string(data)) == e.Digest {
							raw = string(data)
						}
					}
				}
			}
		default:
			checked.Type = "assertion"
		}
		if raw != "" && e.Excerpt != "" && strings.Contains(raw, e.Excerpt) {
			checked.Status = "source_checked"
			checked.VerifiedAt = time.Now().UTC().Format(store.TimeLayout)
			// Automatic semantic acceptance is deliberately narrow: a direct,
			// standalone user declaration exactly names the claim or replacement.
			if e.Type == "user_message" && applicableEvidenceScope(in.Scope, in.Dir) && !strings.ContainsAny(raw, "\n\r") {
				trimmed := strings.TrimSpace(raw)
				accepted := in.Kind == "decision" && trimmed == "Decision: "+content
				if in.Kind == "decision" && trimmed == content {
					for _, prefix := range []string{"Use ", "Keep ", "Prefer ", "Require ", "Avoid "} {
						accepted = accepted || strings.HasPrefix(trimmed, prefix)
					}
				}
				for _, id := range in.Supersedes {
					accepted = accepted || (in.Kind == "decision" && trimmed == fmt.Sprintf("Replace #%d: %s", id, content))
				}
				accepted = accepted || (in.Kind == "task_state" && in.Subject != "" && trimmed == fmt.Sprintf("Completed task %s: %s", in.Subject, content))
				if accepted {
					checked.Status = "verified_for_scope"
					checked.Action = "decision"
					if in.Kind == "task_state" && trimmed == fmt.Sprintf("Completed task %s: %s", in.Subject, content) {
						checked.Action = "complete_subject:" + in.Subject
					}
					for _, id := range in.Supersedes {
						if trimmed == fmt.Sprintf("Replace #%d: %s", id, content) {
							checked.Action = fmt.Sprintf("replace:%d", id)
						}
					}
				}
				if matches := explicitTransition.FindStringSubmatch(trimmed); len(matches) == 4 && matches[3] == content {
					id, err := strconv.ParseInt(matches[2], 10, 64)
					if err == nil {
						if matches[1] == "Replace" && in.Kind == "decision" {
							checked.Status = "verified_for_scope"
							checked.Action = fmt.Sprintf("replace:%d", id)
						}
						if matches[1] == "Completed" && in.Kind == "task_state" {
							checked.Status = "verified_for_scope"
							checked.Action = fmt.Sprintf("complete:%d", id)
						}
					}
				}
				for _, rawHandle := range in.SupersedesTeam {
					handle := normalizeTeamHandle(rawHandle)
					if in.Kind == "decision" && handle != "" && trimmed == fmt.Sprintf("Replace team#%s: %s", handle, content) {
						checked.Status = "verified_for_scope"
						checked.Action = "replace_team:" + handle
					}
				}
				for _, id := range in.Completes {
					if in.Kind == "task_state" && trimmed == fmt.Sprintf("Completed #%d: %s", id, content) {
						checked.Status = "verified_for_scope"
						checked.Action = fmt.Sprintf("complete:%d", id)
					}
				}
			}
		}
		// Only the supplied relevant excerpt is retained, never the full message.
		checked.Excerpt = truncateRunes(checked.Excerpt, 500)
		out = append(out, checked)
	}
	return out, nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

var explicitTransition = regexp.MustCompile(`^(Replace|Completed) #([1-9][0-9]*): (.+)$`)

func applicableEvidenceScope(scope, dir string) bool {
	if scope == "" {
		return true
	}
	if strings.HasPrefix(scope, "checkout:") {
		return strings.TrimPrefix(scope, "checkout:") == store.CheckoutIdentity(dir)
	}
	if strings.HasPrefix(scope, "branch:") {
		gs := gitstate.Capture(dir)
		return gs.Branch != "" && scope == "branch:"+gs.Branch
	}
	return false
}
