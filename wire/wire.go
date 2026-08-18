// Package wire defines the only structs the collector ever serializes to the
// network and the server accepts from it. The schema is exhaustive by design:
// no field exists for local paths, git remotes, project names, hostnames, or
// message text, so redaction holds by construction (spec FR-014, research R10).
// contracts/event-schema.md is the human-readable form of this package; the
// guard test keeps the two honest.
package wire

const SchemaVersion = 1

type Batch struct {
	SchemaVersion int       `json:"schema_version"`
	MachineID     string    `json:"machine_id"`
	ClientSentAt  string    `json:"client_sent_at"`
	Sessions      []Session `json:"sessions"`
	// Events is reserved for a future phase; always empty in v1.
	// Servers accept an empty array and reject a non-empty one.
	Events []struct{} `json:"events"`
}

type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type Session struct {
	SyncUID    string `json:"sync_uid"`
	ProjectKey string `json:"project_key"`
	Assistant  string `json:"assistant"`
	Model      string `json:"model,omitempty"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at,omitempty"`
	EndReason  string `json:"end_reason,omitempty"`
	// ParentSyncUID marks a sub-session (a subagent run) with its parent
	// session's sync_uid; AgentType is the assistant's subagent type slug.
	// Both are additive and absent on ordinary sessions (event-schema.md).
	// The sub-session's summary is content and never has a wire field.
	ParentSyncUID string           `json:"parent_sync_uid,omitempty"`
	AgentType     string           `json:"agent_type,omitempty"`
	Usage         Usage            `json:"usage"`
	EventCounts   map[string]int64 `json:"event_counts,omitempty"`
	// MemoryUsage carries which memory entries this session retrieved and
	// cited — identifiers and counts only, never memory content (Constitution
	// II). Personal memory content stays local; team memory content already
	// exists in the cloud via the separate memory pipeline.
	MemoryUsage []MemoryUsageRef `json:"memory_usage,omitempty"`
	// ModelUsage breaks the session's token totals down by model, one entry per
	// model the session used. A session that switched model mid-run carries
	// several entries; Model above still names the dominant one. Model names and
	// token counts only — the same non-sensitive class as Model and Usage.
	ModelUsage []ModelUsageRef `json:"model_usage,omitempty"`
}

// ModelUsageRef is one model's token totals within a session. The token fields
// mirror Usage; Model reuses the session-level model vocabulary. No content.
type ModelUsageRef struct {
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

// MemoryUsageRef is one memory entry's usage within a session: how many times
// it was retrieved and whether the agent cited it. Scope distinguishes
// personal memory (MemoryID, local to the client) from team memory (TeamUID);
// exactly one of the two is set. No memory content ever appears here.
type MemoryUsageRef struct {
	Scope     string `json:"scope"`               // "personal" | "team"
	MemoryID  int64  `json:"memory_id,omitempty"` // personal id; 0 for team
	TeamUID   string `json:"team_uid,omitempty"`  // team uid; "" for personal
	Retrieved int64  `json:"retrieved"`
	Cited     bool   `json:"cited"`
}

const (
	StatusAccepted  = "accepted"
	StatusDuplicate = "duplicate"
	StatusRejected  = "rejected"
)

type Result struct {
	SyncUID string `json:"sync_uid"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

type IngestResponse struct {
	Results []Result `json:"results"`
}

type HeartbeatRequest struct {
	MachineID        string `json:"machine_id"`
	CollectorVersion string `json:"collector_version"`
	UnsyncedCount    int64  `json:"unsynced_count"`
}

type HeartbeatResponse struct {
	SyncRequested bool `json:"sync_requested"`
}

type DeviceStartRequest struct {
	MachineHint string `json:"machine_hint"`
	// MachineID is the client's stable random machine identifier (it survives
	// sign-out). It lets the platform recognize a returning machine: the
	// approval page recalls the machine's previous name, and the new token
	// supersedes the machine's old one instead of accumulating. Empty from
	// older clients, which keep the historical one-token-per-login shape.
	MachineID string `json:"machine_id,omitempty"`
}

type DeviceStartResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type DeviceTokenRequest struct {
	DeviceCode string `json:"device_code"`
}

type DeviceTokenResponse struct {
	Token string `json:"token"`
	// MemoryToken is a memory-scoped token the client uses for the memory
	// pipeline so the account-wide token is never sent to a self-hosted memory
	// endpoint. Empty from older servers; the client falls back to Token.
	MemoryToken  string `json:"memory_token,omitempty"`
	AccountEmail string `json:"account_email"`
}

type LinkValidateRequest struct {
	ProjectKey string `json:"project_key"`
}

type LinkValidateResponse struct {
	ProjectName string `json:"project_name"`
	// OrgName and OrgVisibility are present only when the project is
	// organization-owned (additive, machine-api-delta.md §1). The consent gate
	// uses them to narrow its wording; older collectors ignore them.
	OrgName       string `json:"org_name,omitempty"`
	OrgVisibility string `json:"org_visibility,omitempty"`
	// MemorySharing is present only for org-owned projects; true iff the org's
	// subscription currently entitles team memory. MemoryRetentionDays (0 =
	// omitted = keep forever) gives the consent gate truthful retention wording.
	// Additive: older collectors ignore both (memory-serving.md §LinkValidate).
	MemorySharing       bool `json:"memory_sharing,omitempty"`
	MemoryRetentionDays int  `json:"memory_retention_days,omitempty"`
	// MemoryEndpoint is the HTTPS base URL a self-hosted enterprise org's memory
	// requests must route to. Present only when the org is enterprise-entitled AND
	// the endpoint is activated (never while pending); empty ⇒ route memory to the
	// vendor cloud (cfg.Server()). Routing metadata, not content: the pipelines
	// stay disjoint (016, memory-endpoint-capability.md §1).
	MemoryEndpoint string `json:"memory_endpoint,omitempty"`
}

// LinkAutoCreateRequest asks the platform to find-or-create a personal
// project by name so `link --auto` needs no key pasted from the dashboard.
// It carries only the project's display name — never paths or code.
type LinkAutoCreateRequest struct {
	ProjectName string `json:"project_name"`
}

type LinkAutoCreateResponse struct {
	ProjectKey  string `json:"project_key"`
	ProjectName string `json:"project_name"`
	// Created is true when the project did not exist and was created; false
	// means an existing personal project with this name was reused (with a
	// freshly issued key, since raw keys are unrecoverable from their hashes).
	Created bool `json:"created,omitempty"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// EntitlementResponse carries the server-decided tier for the collector's
// local gating cache (contracts/entitlement-api.md). It carries no memory
// content in either direction. Personal memory never crosses the wire; team
// memory travels on its own consent-gated pipeline (see memwire.go and
// contracts/memory-sync-api.md), never on this endpoint or the ingest batch.
type EntitlementResponse struct {
	Tier      string `json:"tier"`
	CheckedAt string `json:"checked_at"`
}
