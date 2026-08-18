package wire

// Memory pipeline wire types. These are deliberately unreachable from Batch:
// telemetry and memory content are separate pipelines with separate consent and
// must never share a request (Constitution II). contracts/memory-sync-api.md is
// the human-readable form; memwire_guard_test.go keeps the two honest and
// exhaustively approves every field below.

const (
	MemStatusStored    = "stored"
	MemStatusDuplicate = "duplicate"
	MemStatusRejected  = "rejected"
)

// MemReasonPoolLimit is the per-entry rejection reason for a team pool at its
// plan-tier cap. Unlike the prose validation reasons it is contract vocabulary:
// clients branch on it by equality (recoverable — keep the entry queued and
// re-send next cycle), so it must never be reworded
// (contracts/memory-pool-limit.md).
const MemReasonPoolLimit = "pool_limit"

// Recoverable contribute-refusal error codes for self-hosted memory (016). Like
// MemReasonPoolLimit the client branches on them by equality and keeps the entry
// queued (never dropped): memory_frozen during a cutover freeze, license_readonly
// when the instance is past its license grace window
// (contracts/memory-endpoint-capability.md §3.5).
const (
	MemErrFrozen          = "memory_frozen"
	MemErrLicenseReadOnly = "license_readonly"
)

// MemoryMaxContentRunes is the server-side cap on contributed content. It sits
// above the client save cap (memory.MaxContentLen, 4000) so redaction-marker
// growth at egress alone can never push a saved entry over the limit; the
// client skips anything larger instead of sending a doomed payload
// (contracts/memory-sync-api.md).
const MemoryMaxContentRunes = 4200

type MemoryContributeRequest struct {
	ProjectKey string                  `json:"project_key"`
	Entries    []MemoryContributeEntry `json:"entries"`
}

type MemoryContributeEntry struct {
	UID        string `json:"uid"`
	Content    string `json:"content"`
	Kind       string `json:"kind"`
	Origin     string `json:"origin"`
	CapturedAt string `json:"captured_at"`
	Branch     string `json:"branch,omitempty"`
	CommitHash string `json:"commit_hash,omitempty"`
	// Supersedes carries the team_uids of every already-shared entry this one
	// replaces (an entry may retire several predecessors at once). The server
	// applies same-author-only semantics per uid.
	Supersedes []string `json:"supersedes,omitempty"`
}

type MemoryContributeResponse struct {
	Results []MemoryContributeResult `json:"results"`
}

type MemoryContributeResult struct {
	UID    string `json:"uid"`
	Status string `json:"status"` // stored | duplicate | rejected
	Reason string `json:"reason,omitempty"`
}

type MemoryConsentRequest struct {
	ProjectKey string `json:"project_key"`
	Action     string `json:"action"` // grant | revoke
	Disclosure string `json:"disclosure,omitempty"`
}

type MemoryConsentResponse struct {
	Status string `json:"status"`
}

type MemoryPullResponse struct {
	Entries    []MemoryPullEntry `json:"entries"`
	NextCursor string            `json:"next_cursor"`
	More       bool              `json:"more"`
	// Resync is set when the request cursor predates the tombstone grace
	// period: deletions may have been physically purged before this client saw
	// them, so it must drop its cache and rebuild from this response, which
	// starts from the full pool (FR-011). Also set on server-side cursor
	// regression (cursor newer than the newest entry), the restore-from-backup
	// case (016, FR-034).
	Resync bool `json:"resync,omitempty"`
	// MemoryEndpoint propagates the org's current memory endpoint on every pull
	// so an endpoint change (or a move back to vendor) reaches clients without
	// relinking (016, memory-endpoint-capability.md §2). Same rules as the link
	// response; empty ⇒ vendor cloud.
	MemoryEndpoint string `json:"memory_endpoint,omitempty"`
}

type MemoryPullEntry struct {
	UID            string `json:"uid"`
	Author         string `json:"author"`
	AuthorFormer   bool   `json:"author_former"`
	Content        string `json:"content"` // "" when status = "deleted"
	Kind           string `json:"kind"`
	Origin         string `json:"origin"`
	Status         string `json:"status"` // active | superseded | deleted
	ContradictsUID string `json:"contradicts_uid,omitempty"`
	Flagged        bool   `json:"flagged"`
	Mine           bool   `json:"mine"`
	Edited         bool   `json:"edited"`
	Branch         string `json:"branch,omitempty"`
	CommitHash     string `json:"commit_hash,omitempty"`
	CapturedAt     string `json:"captured_at"`
	UpdatedAt      string `json:"updated_at"`
}

// MemoryKinds is the canonical memory-kind vocabulary, in canonical order.
// It lives here because both sides of the wire order entries by it.
var MemoryKinds = []string{"decision", "convention", "task_state", "fact"}
