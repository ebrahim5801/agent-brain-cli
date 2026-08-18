// Package memsync is the client engine for the team-memory pipeline: it pushes
// consented, redacted memory contributions to the cloud pool and pulls the pool
// into the local read-through cache. It is deliberately separate from
// internal/syncer (telemetry): the two pipelines never share a request and
// never share consent (Constitution II).
package memsync

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/httpauth"
	"github.com/ebrahim5801/agent-brain-cli/internal/redact"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/wire"
)

// ErrReauth mirrors the syncer: pause and keep the queue.
var ErrReauth = httpauth.ErrReauth

var ErrNotSignedIn = errors.New("not signed in — run `agent-brain login`")

// ErrEndpointInsecure marks a per-link memory endpoint that is not https. The
// client refuses to send memory content over cleartext and surfaces the reason
// via `agent-brain status` (016, memory-endpoint-capability.md §3.2).
var ErrEndpointInsecure = errors.New("memory endpoint is not https")

// MemoryBaseURL resolves a link's memory base URL: its per-link MemoryEndpoint
// (which must be https), else the vendor cloud (cfg.Server()). Telemetry never
// calls this — only the memory pipeline routes per link (FR-004/FR-005).
func MemoryBaseURL(cfg *config.Config, link config.Link) (string, error) {
	if link.MemoryEndpoint == "" {
		return cfg.Server(), nil
	}
	u, err := url.Parse(link.MemoryEndpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", ErrEndpointInsecure
	}
	return strings.TrimSuffix(link.MemoryEndpoint, "/"), nil
}

type Result struct {
	Contributed int
	Rejected    int
	// Skipped counts entries withheld client-side this cycle (share_error set,
	// e.g. redaction grew the content past the server cap); they stay queued.
	Skipped int
	// Held counts entries the server refused for pool capacity this cycle
	// (pool_limit). Unlike Rejected they are recoverable: they stay queued and
	// re-send every cycle until the team frees pool space or the plan's cap
	// rises (FR-005).
	Held   int
	Pulled int
}

// contributeBatchSize caps one contribute POST well under the server's 2 MB
// body limit (500 entries x 4000 chars would exceed it).
const contributeBatchSize = 200

type Engine struct {
	HTTP *http.Client
}

func New() *Engine {
	return &Engine{HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// linkedProject pairs a local project row with its config link.
type linkedProject struct {
	projectID int64
	link      config.Link
	linkKey   string
}

// Run performs one memory sync cycle: contribute then pull, for every linked
// org project. Best-effort — an unreachable server leaves the queue intact for
// the next cycle (FR-017); serving is never affected.
func (e *Engine) Run(st *store.Store) (Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return Result{}, err
	}
	if !cfg.SignedIn() {
		return Result{}, ErrNotSignedIn
	}
	projects, err := linkedProjects(st, cfg)
	if err != nil {
		return Result{}, err
	}
	var res Result
	var errs []error
	for _, p := range projects {
		// Contribute and pull are independent (different endpoints, different
		// consent): a failing contribute must not stop the member from
		// receiving the team's updates. Transport/5xx leave the queue intact.
		// A reauth aborts the whole run; any other failure is collected and
		// returned after every project is attempted, so persistent memory-sync
		// failures surface in `sync` output and daemon diagnostics rather than
		// vanishing (FR-018) while entries stay safely queued.
		if err := e.contributeProject(st, cfg, p, &res); err != nil {
			if errors.Is(err, ErrReauth) {
				return res, err
			}
			errs = append(errs, err)
		}
		if err := e.pullProject(st, cfg, p, &res); err != nil {
			if errors.Is(err, ErrReauth) {
				return res, err
			}
			errs = append(errs, err)
		}
	}
	return res, errors.Join(errs...)
}

// PullOne refreshes a single project's cache best-effort — used by the
// SessionStart hook inside its 1 s stage. Signed-out or unlinked projects are a
// silent no-op; the caller bounds the wait and serves whatever the cache holds.
func (e *Engine) PullOne(st *store.Store, projectID int64) error {
	cfg, err := config.Load()
	if err != nil || !cfg.SignedIn() {
		return err
	}
	var kind, identity string
	if err := st.QueryRow(`SELECT identity_kind, identity FROM projects WHERE id = ?`, projectID).Scan(&kind, &identity); err != nil {
		return err
	}
	key := config.LinkKey(kind, identity)
	link, ok := cfg.Links[key]
	if !ok {
		return nil
	}
	var res Result
	return e.pullProject(st, cfg, linkedProject{projectID: projectID, link: link, linkKey: key}, &res)
}

// linkedProjects returns local projects that map to a config link.
func linkedProjects(st *store.Store, cfg *config.Config) ([]linkedProject, error) {
	rows, err := st.Query(`SELECT id, identity_kind, identity FROM projects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []linkedProject
	for rows.Next() {
		var id int64
		var kind, identity string
		if err := rows.Scan(&id, &kind, &identity); err != nil {
			return nil, err
		}
		key := config.LinkKey(kind, identity)
		if link, ok := cfg.Links[key]; ok {
			out = append(out, linkedProject{projectID: id, link: link, linkKey: key})
		}
	}
	return out, rows.Err()
}

// contributeProject drains one project's share queue. Contribution runs only
// for a granted link (even if paused: pre-pause queued entries still deliver);
// pausing merely stops new entries getting a team_uid at capture time (T004).
func (e *Engine) contributeProject(st *store.Store, cfg *config.Config, p linkedProject, res *Result) error {
	if !p.link.ShareGranted() {
		return nil
	}
	pending, err := st.PendingContributions(p.projectID)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	base, err := MemoryBaseURL(cfg, p.link)
	if errors.Is(err, ErrEndpointInsecure) {
		// Refuse to send content over cleartext: hold the queue and surface the
		// reason so `agent-brain status` can point the operator at the endpoint.
		for _, m := range pending {
			if err := st.SetShareError(m.UID, "memory endpoint is not https; refusing to send memory over cleartext"); err != nil {
				return err
			}
		}
		res.Skipped += len(pending)
		return nil
	} else if err != nil {
		return err
	}
	// Chunk the queue: one unbounded POST could exceed the server's 2 MB body
	// limit and wedge forever (each entry carries up to 4000 chars).
	for start := 0; start < len(pending); start += contributeBatchSize {
		end := start + contributeBatchSize
		if end > len(pending) {
			end = len(pending)
		}
		req := wire.MemoryContributeRequest{ProjectKey: p.link.ProjectKey}
		for _, m := range pending[start:end] {
			// Redaction markers can be longer than the spans they replace, so
			// content saved under the local cap may exceed the server's after
			// redaction. Sending it would earn a terminal rejection; withhold
			// it instead and record why, so `agent-brain status` can point the
			// author at the entry (it re-evaluates every cycle).
			content := redact.Apply(m.Content)
			if n := len([]rune(content)); n > wire.MemoryMaxContentRunes {
				reason := fmt.Sprintf("content is %d runes after redaction (server limit %d); shorten the entry or mark it personal_only", n, wire.MemoryMaxContentRunes)
				if err := st.SetShareError(m.UID, reason); err != nil {
					return err
				}
				res.Skipped++
				continue
			}
			req.Entries = append(req.Entries, wire.MemoryContributeEntry{
				UID:        m.UID,
				Content:    content,
				Kind:       m.Kind,
				Origin:     m.Origin,
				CapturedAt: isoToRFC3339(m.CapturedAt),
				Branch:     m.Branch.String,
				CommitHash: m.CommitHash.String,
				Supersedes: m.Supersedes,
			})
		}
		if len(req.Entries) == 0 {
			continue
		}
		var resp wire.MemoryContributeResponse
		code, errCode, err := e.post(cfg, base, "/v1/memory/contribute", req, &resp)
		if err != nil {
			return err
		}
		if code == http.StatusForbidden {
			// A contribute 403 must NOT drop the cache or clear the capability
			// (the member may still be a reader); the pull endpoint is the
			// authority on read access and catches a genuine loss in the same
			// cycle. `no_memory_consent` means the grant was revoked elsewhere
			// (server truth, R5): reconcile the local grant so the queue stops
			// re-transmitting content the user no longer consents to share.
			if errCode == "no_memory_consent" {
				return clearShareGrant(p.linkKey)
			}
			// Self-hosted recoverable refusals (016): a frozen cutover source or a
			// read-only instance past its license grace. Keep every entry queued
			// (never MarkShared) and surface the reason; the queue drains after
			// unfreeze / re-license, delivering to whatever endpoint resolves then
			// (FR-015/FR-016/FR-032).
			if errCode == wire.MemErrFrozen || errCode == wire.MemErrLicenseReadOnly {
				return holdBatch(st, req.Entries, errCode, res)
			}
			return nil
		}
		if code != http.StatusOK {
			return fmt.Errorf("contribute returned %d", code)
		}
		now := store.Now()
		for _, r := range resp.Results {
			switch r.Status {
			case wire.MemStatusStored, wire.MemStatusDuplicate:
				if err := st.MarkShared(r.UID, now); err != nil {
					return err
				}
				res.Contributed++
			case wire.MemStatusRejected:
				if r.Reason == wire.MemReasonPoolLimit {
					// Recoverable: the pool is full right now, not forever. Keep
					// the entry queued (no MarkShared) so it re-sends every cycle
					// and lands on its own once space frees (FR-005); record the
					// hold so `agent-brain status` explains it. MarkShared clears
					// the error on eventual delivery.
					if err := st.SetShareError(r.UID, "team pool is at its plan limit — free space in the shared pool or upgrade the plan"); err != nil {
						return err
					}
					res.Held++
					continue
				}
				// Terminal: resending the identical payload will be rejected again.
				// Drop it from the queue (no unbounded re-egress) but keep a visible
				// permanent-rejection reason, so `agent-brain status` shows the loss
				// distinctly from a recoverable hold — not just the one-shot count on
				// a manual sync (FR-014, US4).
				reason := "rejected by the server as invalid and will not be shared"
				if r.Reason != "" {
					reason = "rejected by the server (" + r.Reason + ") and will not be shared"
				}
				if err := st.MarkShareRejected(r.UID, reason, now); err != nil {
					return err
				}
				res.Rejected++
			}
		}
	}
	return refreshCapability(p.link, p.linkKey)
}

// pullProject refreshes one project's local cache from the pool, looping on the
// cursor until caught up. Reading is a membership benefit: it runs for any
// linked org project, independent of sharing consent (R5). It keys off
// OrgProject, not the TeamMemory hint — a lapse-cleared hint must not stop the
// client from asking again, or access could never resume after reactivation
// or re-add (FR-018); the server refuses cheaply while access is off.
func (e *Engine) pullProject(st *store.Store, cfg *config.Config, p linkedProject, res *Result) error {
	if !p.link.TeamMemory && !p.link.OrgProject {
		return nil
	}
	base, err := MemoryBaseURL(cfg, p.link)
	if errors.Is(err, ErrEndpointInsecure) {
		// Non-https endpoint: refuse to pull over cleartext. Serving continues off
		// whatever the cache already holds; status surfaces the refusal.
		return nil
	} else if err != nil {
		return err
	}
	// Endpoint-change resync (016, T022): if the cached pool belongs to a
	// different endpoint than the one now resolved for this link (cutover in
	// either direction, or an endpoint typo-fix), drop the cache and rebuild from
	// the new endpoint's full pool. Reuses the 010 resync machinery.
	storedEndpoint, err := st.PullEndpoint(p.projectID)
	if err != nil {
		return err
	}
	if storedEndpoint != p.link.MemoryEndpoint {
		if err := st.DropTeamCache(p.projectID); err != nil {
			return err
		}
		if err := st.StampPullEndpoint(p.projectID, p.link.MemoryEndpoint); err != nil {
			return err
		}
	}
	var learnedEndpoint string
	for {
		cursor, err := st.PullCursor(p.projectID)
		if err != nil {
			return err
		}
		var resp wire.MemoryPullResponse
		code, _, err := e.get(cfg, base, "/v1/memory/pull", map[string]string{
			"project_key": p.link.ProjectKey, "since": cursor,
		}, &resp)
		if err != nil {
			return err
		}
		if code == http.StatusForbidden {
			// Pull requires membership + active subscription + current key, so a
			// 403 here is a definitive loss of read access (removal, lapse, or
			// rotation). Drop the cached pool so serving stops immediately
			// (SC-006) — clearing the capability flag alone would not, because
			// BuildPack reads the cache directly and the OR gate stays open via
			// personal memory.
			return revokeTeamAccess(st, p.link, p.linkKey, p.projectID)
		}
		if code != http.StatusOK {
			return fmt.Errorf("pull returned %d", code)
		}
		// Every pull echoes the org's current endpoint; the last one wins. This is
		// the propagation vehicle: an endpoint change (or a move back to vendor)
		// reaches the client here without relinking (FR-002).
		learnedEndpoint = resp.MemoryEndpoint
		if resp.Resync {
			// The saved cursor predates the tombstone grace period: deletions
			// may have been physically purged before this client saw them, so
			// the cache cannot be trusted. Rebuild it from the full pool this
			// response starts (FR-011: a deleted entry is never served again).
			if err := st.DropTeamCache(p.projectID); err != nil {
				return err
			}
		}
		rows := make([]store.TeamMemoryRow, 0, len(resp.Entries))
		for _, e := range resp.Entries {
			rows = append(rows, store.TeamMemoryRow{
				UID: e.UID, ProjectID: p.projectID, Author: e.Author, AuthorFormer: e.AuthorFormer,
				Content: e.Content, Kind: e.Kind, Origin: e.Origin, Status: e.Status,
				Contradicts: e.ContradictsUID, Flagged: e.Flagged, Mine: e.Mine,
				Branch: e.Branch, CommitHash: e.CommitHash,
				CapturedAt: rfc3339ToISO(e.CapturedAt), UpdatedAt: rfc3339ToISO(e.UpdatedAt),
			})
		}
		if err := st.ApplyPull(p.projectID, rows); err != nil {
			return err
		}
		res.Pulled += len(rows)
		if err := st.SetPullCursor(p.projectID, resp.NextCursor); err != nil {
			return err
		}
		if !resp.More {
			break
		}
	}
	if learnedEndpoint != p.link.MemoryEndpoint {
		if err := updateLinkEndpoint(p.linkKey, learnedEndpoint); err != nil {
			return err
		}
	}
	return refreshCapability(p.link, p.linkKey)
}

// updateLinkEndpoint persists a memory endpoint learned from a pull response so
// the next cycle routes to it (and triggers the endpoint-change resync). Empty
// moves the link back to the vendor cloud (reverse migration).
func updateLinkEndpoint(linkKey, endpoint string) error {
	_, err := config.Update(func(c *config.Config) error {
		if l, ok := c.Links[linkKey]; ok {
			l.MemoryEndpoint = endpoint
			c.Links[linkKey] = l
		}
		return nil
	})
	return err
}

// post sends a JSON body through the shared transport (httpauth.Do): 401 maps
// to ErrReauth, Retry-After is honored on 429 with a short in-run wait, and
// transport errors classify as *httpauth.TransportError. On a 403 the contract
// error code is returned so callers can tell a consent refusal from a
// membership/subscription loss.
func (e *Engine) post(cfg *config.Config, baseURL, path string, body, out any) (int, string, error) {
	return httpauth.Do(e.HTTP, cfg.MemoryAuthToken(), http.MethodPost, baseURL+path, body, out)
}

// refreshCapability sets the cached team_memory capability after a successful
// authorized call; clearCapability drops it after a pull 403 so serving stops.
// Both take the cycle's link snapshot to skip the config write when the flag
// already holds the target value (the config file is lock-and-rewrite).
func refreshCapability(link config.Link, linkKey string) error {
	if link.TeamMemory && link.OrgProject {
		return nil
	}
	_, err := config.Update(func(c *config.Config) error {
		if l, ok := c.Links[linkKey]; ok {
			l.TeamMemory = true
			l.OrgProject = true
			c.Links[linkKey] = l
		}
		return nil
	})
	return err
}

// revokeTeamAccess is the response to a definitive loss of read access: drop
// the local pool cache so serving stops immediately, then clear the capability
// hint. Dropping the cache is the load-bearing step — the serving path reads
// the cache directly and does not consult the capability flag.
func revokeTeamAccess(st *store.Store, link config.Link, linkKey string, projectID int64) error {
	if err := st.DropTeamCache(projectID); err != nil {
		return err
	}
	return clearCapability(link, linkKey)
}

func clearCapability(link config.Link, linkKey string) error {
	if !link.TeamMemory {
		return nil
	}
	_, err := config.Update(func(c *config.Config) error {
		if l, ok := c.Links[linkKey]; ok {
			l.TeamMemory = false
			c.Links[linkKey] = l
		}
		return nil
	})
	return err
}

// holdBatch keeps a refused batch queued and records why, so `agent-brain status`
// can explain the hold. Never MarkShared — the entries re-send next cycle and
// drain once the source unfreezes or the license is renewed (016, T026).
func holdBatch(st *store.Store, entries []wire.MemoryContributeEntry, code string, res *Result) error {
	reason := "team memory is read-only (license grace elapsed) — queued, will retry"
	if code == wire.MemErrFrozen {
		reason = "team memory is frozen for a migration cutover — queued, will retry"
	}
	for _, en := range entries {
		if err := st.SetShareError(en.UID, reason); err != nil {
			return err
		}
	}
	res.Held += len(entries)
	return nil
}

// clearShareGrant reconciles a grant the server reports as revoked (e.g. the
// user revoked from another machine): without it the queue would re-transmit
// the same content every cycle against a standing refusal.
func clearShareGrant(linkKey string) error {
	_, err := config.Update(func(c *config.Config) error {
		if l, ok := c.Links[linkKey]; ok {
			l.MemoryShare = nil
			c.Links[linkKey] = l
		}
		return nil
	})
	return err
}

// get issues an authorized GET with query params through the same shared
// transport as post.
func (e *Engine) get(cfg *config.Config, baseURL, path string, params map[string]string, out any) (int, string, error) {
	u, err := url.Parse(baseURL + path)
	if err != nil {
		return 0, "", err
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return httpauth.Do(e.HTTP, cfg.MemoryAuthToken(), http.MethodGet, u.String(), nil, out)
}

// isoToRFC3339 converts the store's fixed UTC layout to the wire's RFC3339.
func isoToRFC3339(s string) string {
	t, err := time.Parse(store.TimeLayout, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(time.RFC3339)
}

// rfc3339ToISO converts a wire timestamp to the store's fixed UTC layout so
// team and personal entries sort consistently in the cache.
func rfc3339ToISO(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(store.TimeLayout)
}
