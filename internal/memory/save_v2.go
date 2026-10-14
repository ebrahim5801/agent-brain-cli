package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ebrahim5801/agent-brain-cli/internal/gitstate"
	"github.com/ebrahim5801/agent-brain-cli/internal/redact"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func digestText(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func saveV2(st *store.Store, in SaveInput, content, priority string) (SaveResult, error) {
	if len(in.Relations) > 32 || len(in.Supersedes) > 100 || len(in.Completes) > 100 {
		return SaveResult{}, ErrContentTooLong
	}
	rationale := strings.TrimSpace(redact.Apply(in.Rationale))
	subject := strings.TrimSpace(redact.Apply(in.Subject))
	scope := strings.TrimSpace(redact.Apply(in.Scope))
	if scope != "" && !strings.HasPrefix(scope, "branch:") && !strings.HasPrefix(scope, "checkout:") {
		return SaveResult{}, fmt.Errorf("scope must be project-wide, branch:name or checkout:absolute-path")
	}
	if len(subject) > 256 || len(scope) > 1024 {
		return SaveResult{}, ErrContentTooLong
	}
	in.Scope = scope
	for _, s := range []string{rationale, subject, scope} {
		if utf8.RuneCountInString(s) > MaxContentLen {
			return SaveResult{}, ErrContentTooLong
		}
	}
	if in.SessionHandle != "" {
		b, err := st.ResolveBinding(in.SessionHandle, in.ProjectID, in.Dir)
		if err != nil {
			return SaveResult{}, err
		}
		if in.SessionID != 0 && in.SessionID != b.SessionID {
			return SaveResult{}, store.ErrAttribution
		}
		in.SessionID = b.SessionID
	} else if in.SessionID != 0 {
		return SaveResult{}, store.ErrAttribution
	}
	requestSupersedes := append([]int64(nil), in.Supersedes...)
	requestCompletes := append([]int64(nil), in.Completes...)
	executor := in.SessionHandle
	if in.BatchID != "" {
		batch, err := st.CaptureStatus(in.BatchID, in.ProjectID)
		if err != nil {
			return SaveResult{}, err
		}
		var checkout string
		if err := st.QueryRow(`SELECT checkout FROM memory_session_bindings WHERE handle=? AND project_id=?`, batch.SourceHandle, in.ProjectID).Scan(&checkout); err != nil {
			return SaveResult{}, err
		}
		source, err := st.ResolveBinding(batch.SourceHandle, in.ProjectID, checkout)
		if err != nil {
			return SaveResult{}, err
		}
		in.SessionHandle = source.Handle
		in.SessionID = source.SessionID
		in.Dir = checkout
	}
	op := in.OperationID
	if op == "" {
		op = uuid.NewString()
	}
	completeness := "complete"
	if rationale == "" || subject == "" || len(in.Evidence) == 0 {
		completeness = "unknown: rationale, subject or evidence not supplied"
	}
	details := store.MemoryDetails{Rationale: rationale, Subject: subject, Scope: scope, SourceHandle: in.SessionHandle, ContentDigest: digestText(content), Completeness: completeness}
	// Capture-time Git and verification timestamps are excluded: retrying the
	// same logical operation after HEAD moves must return its original receipt.
	evidence, err := verifyEvidence(st, in, content)
	if err != nil {
		return SaveResult{}, err
	}
	seenSup := map[int64]bool{}
	seenDone := map[int64]bool{}
	for _, id := range in.Supersedes {
		seenSup[id] = true
	}
	for _, id := range in.Completes {
		seenDone[id] = true
	}
	for _, e := range evidence {
		action, idText, ok := strings.Cut(e.Action, ":")
		if !ok {
			continue
		}
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil {
			continue
		}
		if action == "replace" && !seenSup[id] {
			in.Supersedes = append(in.Supersedes, id)
			seenSup[id] = true
		}
		if action == "complete" && !seenDone[id] {
			in.Completes = append(in.Completes, id)
			seenDone[id] = true
		}
	}
	completeSubject := false
	for _, e := range evidence {
		completeSubject = completeSubject || e.Action == "complete_subject:"+subject
	}
	if subject != "" {
		snapshot, err := st.MemorySnapshot(in.ProjectID, map[string]bool{scope: true})
		if err != nil {
			return SaveResult{}, err
		}
		for _, m := range snapshot.Entries {
			if completeSubject && m.Status == store.MemoryActive && m.Kind == "task_state" && m.Details.Subject == subject && m.Details.Scope == scope && !seenDone[m.ID] {
				in.Completes = append(in.Completes, m.ID)
				seenDone[m.ID] = true
			}
			if m.Status == store.MemoryActive && m.Kind == in.Kind && m.Details.Subject == subject && m.Details.Scope == scope && m.Content != content && !sameDecisionChoice(m.Content, content) && !seenSup[m.ID] && in.Kind == "decision" {
				in.Supersedes = append(in.Supersedes, m.ID)
				seenSup[m.ID] = true
			}
		}
	}
	safeEvidence := append([]EvidenceInput(nil), in.Evidence...)
	for i := range safeEvidence {
		safeEvidence[i].Excerpt = redact.Apply(safeEvidence[i].Excerpt)
		safeEvidence[i].Reference = redact.Apply(safeEvidence[i].Reference)
	}
	raw, err := json.Marshal(struct {
		Content, Kind, Origin, Priority string
		Details                         store.MemoryDetails
		Relations                       []store.NewRelation
		Supersedes                      []int64
		SupersedesTeam                  []string
		Completes                       []int64
		Evidence                        []EvidenceInput
		Revision                        int64
		PersonalOnly                    bool
	}{content, in.Kind, in.Origin, priority, details, in.Relations, requestSupersedes, in.SupersedesTeam, requestCompletes, safeEvidence, in.ExpectedRevision, in.PersonalOnly})
	if err != nil {
		return SaveResult{}, err
	}
	teamRejected := map[string]string{}
	var teamAccepted, teamUIDs []string
	for _, raw := range in.SupersedesTeam {
		h := normalizeTeamHandle(raw)
		if h == "" {
			continue
		}
		if in.PersonalOnly {
			teamRejected[h] = "personal_only cannot supersede a team entry"
			continue
		}
		verifiedTeam := false
		for _, e := range evidence {
			verifiedTeam = verifiedTeam || (e.Type == "user_message" && e.Status == "verified_for_scope" && e.Action == "replace_team:"+h)
		}
		if !verifiedTeam || scope != "" || os.Getenv("AGENT_BRAIN_MEMORY_V2_AUTOMATION") == "off" {
			teamRejected[h] = "team replacement requires direct original-user evidence and project-wide scope"
			continue
		}
		uid, err := st.ResolveTeamHandle(in.ProjectID, h)
		if err != nil {
			teamRejected[h] = "no unambiguous active team entry"
			continue
		}
		teamUIDs = append(teamUIDs, uid)
		teamAccepted = append(teamAccepted, h)
	}
	gs := gitstate.Capture(in.Dir)
	saved, err := st.InsertMemoryV2(store.V2Write{Memory: store.NewMemory{ProjectID: in.ProjectID, SessionID: in.SessionID, Content: content, Kind: in.Kind, Origin: in.Origin, Priority: priority, Branch: gs.Branch, Commit: gs.Commit, HasRepo: gs.HasRepo, PersonalOnly: in.PersonalOnly, ShareLive: in.ShareLive}, Details: details, OperationID: op, BatchID: in.BatchID, Generation: in.Generation, PayloadDigest: digestText(string(raw)), TeamUIDs: teamUIDs, TeamAccepted: teamAccepted, TeamRejected: teamRejected, Evidence: evidence, Relations: in.Relations, Supersedes: in.Supersedes, Completes: in.Completes, ExpectedRevision: in.ExpectedRevision, DisableTransitions: os.Getenv("AGENT_BRAIN_MEMORY_V2_AUTOMATION") == "off", ExecutorHandle: executor}, time.Now().UTC())
	if err != nil {
		return SaveResult{}, err
	}
	attribution := "unbound"
	if in.SessionHandle != "" {
		attribution = "bound"
	}
	result := SaveResult{Attribution: attribution, ID: saved.ID, OperationID: saved.OperationID, Outcome: saved.Outcome, Superseded: saved.Accepted, Completed: saved.Completed, Pending: saved.Pending, Rejected: saved.Rejected, SupersededTeam: saved.SupersededTeam, RejectedTeam: saved.RejectedTeam}
	return result, nil
}

// Recognizable equivalent direct choices are not contradictions merely because
// the rationale wording differs. Unknown prose remains a proposed change;
// this lexical heuristic never authorizes retirement.
func sameDecisionChoice(a, b string) bool {
	option := func(s string) string {
		words := rawTokens(s)
		if len(words) < 2 {
			return ""
		}
		switch words[0] {
		case "use", "keep", "prefer", "require":
		default:
			return ""
		}
		i := 1
		if words[i] == "the" && len(words) > 2 {
			i++
		}
		choice := strings.Trim(words[i], "\"'")
		switch choice {
		case "local", "shared", "private", "durable", "automatic":
			if len(words) > i+1 {
				choice += " " + words[i+1]
			}
		}
		return choice
	}
	left, right := option(a), option(b)
	return left != "" && left == right
}
