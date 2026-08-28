package syncer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/httpauth"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/wire"
)

// ErrReauth surfaces the contract's reauth_required: the caller pauses sync
// and keeps collecting; queued data is untouched (FR-004).
var ErrReauth = httpauth.ErrReauth

// ErrNoConsent is the client-side gate: without recorded consent the syncer
// refuses to touch the network at all (FR-011, SC-004).
var ErrNoConsent = errors.New("telemetry consent not granted — run `agent-brain consent`")

var ErrNotSignedIn = errors.New("not signed in — run `agent-brain login`")

type Result struct {
	Synced   int
	Rejected int
	Projects map[string]bool
}

type Engine struct {
	HTTP *http.Client
	// Progress, when set, receives per-batch updates (the CLI's verbose mode).
	Progress func(sent, rejected int)
}

func New() *Engine {
	return &Engine{HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// Run drains the outbox: batch, deliver, mark, repeat until empty. Consent is
// re-checked from disk before every request so withdrawal stops an in-flight
// catch-up between batches (spec edge case). A config-dir lock keeps a manual
// `sync` and the daemon from uploading the same rows concurrently (the server
// would dedupe anyway; the lock just avoids wasted transfers).
func (e *Engine) Run(st *store.Store) (Result, error) {
	unlock, err := lockSync()
	if err != nil {
		return Result{Projects: map[string]bool{}}, err
	}
	defer unlock()
	res := Result{Projects: map[string]bool{}}
	// Accumulated across every batch so a wholly-rejected final batch can name
	// all offending projects, not just its own — sync drains every linked
	// project at once, so rejections often span more than the current directory.
	rejectCodes := map[string]int{}
	rejectedByProject := map[string]int{}
	for {
		cfg, err := config.Load()
		if err != nil {
			return res, err
		}
		if !cfg.SignedIn() {
			return res, ErrNotSignedIn
		}
		if !cfg.TelemetryConsented() {
			return res, ErrNoConsent
		}

		ns, err := machineNamespace(cfg)
		if err == nil {
			if err := st.EnsureSyncUIDs(ns); err != nil {
				return res, err
			}
		}

		batch, sessions, err := nextBatch(st, cfg)
		if err != nil {
			return res, err
		}
		if len(batch.Sessions) == 0 {
			return res, nil
		}

		resp, err := e.deliver(cfg, batch)
		if err != nil {
			return res, err
		}

		var ackedRows []outboxSession
		acked := map[string]bool{}
		rejected := map[string]bool{}
		// Map each rejected sync_uid back to a recognizable project label. The
		// wire result carries only the sync_uid, so pair it with the outbox rows
		// (project key) and resolve the key to its link identity (e.g. the linked
		// directory or git remote) via config.
		keyBySyncUID := map[string]string{}
		for _, os := range sessions {
			keyBySyncUID[os.session.SyncUID] = os.session.ProjectKey
		}
		identityByProjectKey := map[string]string{}
		for identity, link := range cfg.Links {
			identityByProjectKey[link.ProjectKey] = identity
		}
		for _, r := range resp.Results {
			switch r.Status {
			case wire.StatusAccepted, wire.StatusDuplicate:
				acked[r.SyncUID] = true
			case wire.StatusRejected:
				res.Rejected++
				rejected[r.SyncUID] = true
				rejectCodes[r.Error]++
				label := identityByProjectKey[keyBySyncUID[r.SyncUID]]
				if label == "" {
					label = "unknown project"
				}
				rejectedByProject[label]++
			}
		}
		// Carry the eligibleSessions values whole: markSynced needs their
		// usage/model signatures, and a reconstructed zero signature would
		// never match a session that has usage rows, leaving it eligible and
		// spinning the drain loop forever on server-side duplicates.
		var rejectedRows []outboxSession
		for _, os := range sessions {
			switch {
			case acked[os.session.SyncUID]:
				ackedRows = append(ackedRows, os)
				res.Synced++
				res.Projects[os.session.ProjectKey] = true
			case rejected[os.session.SyncUID]:
				rejectedRows = append(rejectedRows, os)
			}
		}
		if err := markSynced(st, ackedRows); err != nil {
			return res, err
		}
		// Defer the refused rows before deciding whether to continue: without
		// this they stay at the head of the ORDER BY id window and every later
		// batch re-sends them.
		if err := markRejected(st, rejectedRows); err != nil {
			return res, err
		}
		if e.Progress != nil {
			e.Progress(res.Synced, res.Rejected)
		}
		if len(ackedRows) == 0 && res.Rejected > 0 {
			// Nothing acked and something rejected: stop rather than spin on
			// the same rejected rows; they stay queued for diagnosis.
			return res, rejectionError(res.Rejected, rejectCodes, rejectedByProject)
		}
	}
}

// rejectionGuidance pairs each known reject code with the action that clears
// it, in the order the codes are reported. The server's vocabulary is open
// (wire.Reject*), so an unrecognized code is surfaced verbatim rather than
// guessed at — a newer server can explain itself through an older client.
var rejectionGuidance = []struct {
	code   string
	advice string
}{
	{wire.RejectUnknownKey, "no project on the platform matches the stored key — it was deleted there, or the key belongs to another deployment; re-run `agent-brain link` in that directory"},
	{wire.RejectKeyRotated, "the project key was rotated — re-run `agent-brain link` in that directory to pick up the current key"},
	{wire.RejectNotAuthorized, "this account is not authorized to write to it — for a personal project, sign in as the account that owns it; for an organization project, ask an admin to (re-)add you and confirm the team subscription is active"},
	{wire.RejectMemoryRefNotAuthorized, "the records cite team memory entries this account may not read; re-linking will not help — ask an org admin about your team-memory access"},
	{wire.RejectAttributionConflict, "the sync_uid already belongs to a different project or account, which usually means a copied agent-brain.db"},
	{wire.RejectInvalidRecord, "the platform considered the records malformed; they will not start succeeding on their own, so this is worth reporting as a bug"},
	{wire.RejectInternal, "the platform hit an internal error; the records stay queued and retry on their own"},
}

// rejectionError explains a batch that was wholly rejected. Each distinct
// reject code contributes its own guidance, because one drain spans every
// linked project and a single pass can hit several unrelated causes at once
// (machine-api-delta.md §2, FR-012). The data stays queued and catches up once
// the cause is cleared.
func rejectionError(total int, codes map[string]int, byProject map[string]int) error {
	where := projectBreakdown(byProject)
	var reasons []string
	seen := map[string]bool{}
	for _, g := range rejectionGuidance {
		if codes[g.code] > 0 {
			reasons = append(reasons, g.advice)
			seen[g.code] = true
		}
	}
	unknown := make([]string, 0, len(codes))
	for code, n := range codes {
		if !seen[code] && n > 0 {
			unknown = append(unknown, code)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		reasons = append(reasons, "the platform reported "+strings.Join(unknown, ", "))
	}
	if len(reasons) == 0 {
		return fmt.Errorf("%d record(s) rejected by the platform for %s", total, where)
	}
	return fmt.Errorf("%d record(s) rejected for %s: %s. Your data stays queued locally and will sync once the cause is cleared",
		total, where, strings.Join(reasons, "; also, "))
}

// projectBreakdown names which linked projects had records rejected. Because
// sync drains every linked project in one pass, a rejection often belongs to a
// different project than the one the user just linked or worked in; naming them
// turns a generic failure into something the operator can act on. Labels are
// the link identity (linked directory or git remote) and are sorted for a
// stable message.
func projectBreakdown(byProject map[string]int) string {
	if len(byProject) == 0 {
		return "the linked project"
	}
	labels := make([]string, 0, len(byProject))
	for label := range byProject {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	if len(labels) == 1 {
		return fmt.Sprintf("project %s (%d record(s))", labels[0], byProject[labels[0]])
	}
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, fmt.Sprintf("%s (%d)", label, byProject[label]))
	}
	return "these projects: " + strings.Join(parts, ", ")
}

func nextBatch(st *store.Store, cfg *config.Config) (wire.Batch, []outboxSession, error) {
	sessions, err := eligibleSessions(st, cfg, maxBatchSessions, true)
	if err != nil {
		return wire.Batch{}, nil, err
	}
	batch := wire.Batch{
		SchemaVersion: wire.SchemaVersion,
		MachineID:     cfg.MachineID,
		ClientSentAt:  store.Now(),
		Sessions:      make([]wire.Session, 0, len(sessions)),
		Events:        []struct{}{},
	}
	for _, s := range sessions {
		batch.Sessions = append(batch.Sessions, s.session)
	}
	return batch, sessions, nil
}

// deliver posts one batch through the shared transport (httpauth.Do): 429
// honors Retry-After, 5xx backs off briefly, 401 aborts with ErrReauth.
// Transport errors (offline, unreachable) return immediately — the long
// 1s→15min backoff between whole runs belongs to the daemon loop (R8), and a
// manual `sync` must fail fast with data safely queued rather than hang.
func (e *Engine) deliver(cfg *config.Config, batch wire.Batch) (*wire.IngestResponse, error) {
	var out wire.IngestResponse
	code, _, err := httpauth.Do(e.HTTP, cfg.Token, http.MethodPost, cfg.Server()+"/v1/ingest", batch, &out)
	if err != nil {
		return nil, err
	}
	switch code {
	case http.StatusOK:
		return &out, nil
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("platform throttled the batch")
	default:
		return nil, fmt.Errorf("platform returned %d", code)
	}
}

// Heartbeat reports liveness and asks whether the dashboard requested an
// immediate sync (research R9).
func (e *Engine) Heartbeat(st *store.Store, cfg *config.Config, version string) (syncRequested bool, err error) {
	body, err := json.Marshal(wire.HeartbeatRequest{
		MachineID:        cfg.MachineID,
		CollectorVersion: version,
		UnsyncedCount:    UnsyncedCount(st, cfg),
	})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.Server()+"/v1/heartbeat", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	httpResp, err := e.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode == http.StatusUnauthorized {
		return false, ErrReauth
	}
	if httpResp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("heartbeat returned %d", httpResp.StatusCode)
	}
	var out wire.HeartbeatResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.SyncRequested, nil
}
