// Package mcpserver exposes the local memory store to the assistant as an
// MCP server over stdio — no listening socket exists, so serving is local-only
// by construction (FR-012). The server always completes the handshake; when
// memory is gated (no consent, not entitled, project disabled, store broken)
// tools return structured errors per contracts/mcp-memory.md and never crash
// the session.
package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/diag"
	"github.com/ebrahim5801/agent-brain-cli/internal/entitlement"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

const serverName = "agent-brain-memory"

// gitBudget bounds total git subprocess time per tool call when rendering
// freshness (contracts/session-injection.md).
const gitBudget = 500 * time.Millisecond

type Server struct {
	st     *store.Store // nil when the store could not be opened
	dir    string
	mcp    *mcp.Server
	logger *diag.Logger
}

// New builds the MCP server for the project enclosing dir. st may be nil;
// tools then answer "temporarily unavailable".
func New(st *store.Store, dir, version string) *Server {
	s := &Server{st: st, dir: dir, logger: newLogger(st)}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "memory_save",
		Description: "Persist one durable memory for the current project (a decision, convention, " +
			"task state, or non-obvious fact). Use origin \"explicit\" when the developer asked for " +
			"this to be remembered. Pass supersedes with personal [#id] entry IDs this memory replaces. " +
			"To replace or contradict a team entry shown in the pack, pass its [team#...] handle to " +
			"supersedes_team; the server records a supersede for your own entry or a contradiction for a " +
			"teammate's, and both are surfaced to the team.",
	}, s.save)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "memory_search",
		Description: "Retrieve this project's memories beyond the injected pack. Keyword match over " +
			"active entries, most relevant first; empty query returns the most relevant entries.",
	}, s.search)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "memory_list",
		Description: "List all memory entries for this project (active first, newest first). Use it to " +
			"ground supersedes decisions before memory_save.",
	}, s.list)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "session_summary",
		Description: "Record a 2-3 sentence summary of this session (the goal, what changed, the outcome) " +
			"on the local session record, so `agent-brain stats --sessions` shows what each session was. " +
			"Stored on this machine only; never synced.",
	}, s.sessionSummary)

	return s
}

// Run serves MCP over stdio until the client disconnects.
func (s *Server) Run(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

// newLogger builds the diagnostics sink for this server, matching the hook
// path's fallbackLogger: writes go to the store's diagnostics table when a
// store is open, otherwise to the fallback log file.
func newLogger(st *store.Store) *diag.Logger {
	logPath, _ := store.DiagLogPath()
	l := &diag.Logger{FallbackPath: logPath}
	if st != nil {
		l.DB = st.DB
		l.Rebind = st.Rebind
	}
	return l
}

// recordRetrieval records that one memory entry was served this call. It is
// best-effort (FR-011): a failure is logged to diagnostics and never fails the
// tool call. sessionID == 0 (no session on record for this project yet) is
// skipped rather than attempted, since memory_usage_events.session_id has no
// valid target to reference.
func (s *Server) recordRetrieval(sessionID, projectID int64, scope string, memoryID int64, teamUID string) {
	if sessionID == 0 {
		return
	}
	if err := s.st.RecordRetrieval(sessionID, projectID, scope, memoryID, teamUID, store.Now()); err != nil {
		s.logger.Log("memory", "record retrieval: %v", err)
	}
}

type saveIn struct {
	Content        string   `json:"content" jsonschema:"The fact/decision/convention/task state to remember, self-contained and concise. Max 4000 characters."`
	Kind           string   `json:"kind" jsonschema:"One of: decision, convention, task_state, fact."`
	Origin         string   `json:"origin" jsonschema:"explicit when the developer asked for this to be remembered, auto otherwise."`
	Supersedes     []int64  `json:"supersedes,omitempty" jsonschema:"IDs of active personal entries (shown as [#id]) this replaces (contradicted or outdated)."`
	SupersedesTeam []string `json:"supersedes_team,omitempty" jsonschema:"Handles of team entries (shown as [team#...]) this replaces or contradicts. A same-author entry is superseded; a teammate's becomes a flagged contradiction."`
	PersonalOnly   bool     `json:"personal_only,omitempty" jsonschema:"Set true for personal context that should stay on this machine and never be shared to the team pool, even on a team project."`
}

func (s *Server) save(ctx context.Context, req *mcp.CallToolRequest, in saveIn) (*mcp.CallToolResult, any, error) {
	projectID, link, err := s.gate()
	if err != nil {
		return nil, nil, err
	}
	// Session attribution is provenance, best-effort like git state: a lookup
	// failure leaves session_id NULL rather than losing the memory.
	sessionID, _ := s.st.LatestSessionID(projectID)
	res, err := memory.Save(s.st, memory.SaveInput{
		ProjectID:      projectID,
		SessionID:      sessionID,
		Dir:            s.dir,
		Content:        in.Content,
		Kind:           in.Kind,
		Origin:         in.Origin,
		Supersedes:     in.Supersedes,
		SupersedesTeam: in.SupersedesTeam,
		PersonalOnly:   in.PersonalOnly,
		ShareLive:      link.ShareLive(),
	})
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "saved memory %d", res.ID)
	for _, id := range res.Superseded {
		fmt.Fprintf(&b, "\nsuperseded %d", id)
	}
	for id, reason := range res.Rejected {
		fmt.Fprintf(&b, "\nsupersedes %d rejected: %s", id, reason)
	}
	for _, h := range res.SupersededTeam {
		fmt.Fprintf(&b, "\nqueued to supersede/contradict team entry team#%s on next sync", h)
	}
	for h, reason := range res.RejectedTeam {
		fmt.Fprintf(&b, "\nsupersedes_team team#%s rejected: %s", h, reason)
	}
	return textResult(b.String()), nil, nil
}

type summaryIn struct {
	Summary string `json:"summary" jsonschema:"2-3 sentences: the goal, what changed, the outcome. Max 500 characters."`
}

// sessionSummary annotates the newest session of this project. Unlike the
// memory tools it is not entitlement-gated: the summary is a local telemetry
// annotation, available to every collector install.
func (s *Server) sessionSummary(ctx context.Context, req *mcp.CallToolRequest, in summaryIn) (*mcp.CallToolResult, any, error) {
	if s.st == nil {
		return nil, nil, gateErr("temporarily unavailable", "check `agent-brain status` and diagnostics")
	}
	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		return nil, nil, fmt.Errorf("summary must not be empty")
	}
	if r := []rune(summary); len(r) > 500 {
		summary = string(r[:500])
	}
	proj := attribution.Resolve(s.dir)
	projectID, err := s.st.UpsertProject(store.ProjectIdentity{
		Kind: proj.Kind, Identity: proj.Identity, DisplayName: proj.DisplayName,
	}, store.Now())
	if err != nil {
		return nil, nil, gateErr("temporarily unavailable", "check `agent-brain status` and diagnostics")
	}
	ok, err := s.st.SetSessionSummary(projectID, summary)
	if err != nil {
		return nil, nil, gateErr("temporarily unavailable", "check `agent-brain status` and diagnostics")
	}
	if !ok {
		return nil, nil, fmt.Errorf("no session recorded for this project yet")
	}
	return textResult("session summary saved"), nil, nil
}

type searchIn struct {
	Query string `json:"query,omitempty" jsonschema:"Keywords to match; empty returns most recent."`
	Kind  string `json:"kind,omitempty" jsonschema:"Narrow to one kind: decision, convention, task_state, or fact."`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum entries to return, 1-50; default 10."`
}

func (s *Server) search(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
	projectID, _, err := s.gate()
	if err != nil {
		return nil, nil, err
	}
	// Best-effort attribution, like save(): a search that predates any recorded
	// session (sessionID 0) simply skips retrieval capture below.
	sessionID, _ := s.st.LatestSessionID(projectID)
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	now := time.Now()
	ranked, err := memory.Search(s.st, projectID, s.dir, in.Query, in.Kind, limit, gitBudget, now)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range ranked {
		s.recordRetrieval(sessionID, projectID, "personal", r.Entry.ID, "")
	}
	// The limit bounds the whole result: personal matches rank first (they are
	// the reader's own context), team matches fill what remains.
	var teamLines []string
	if remaining := limit - len(ranked); remaining > 0 {
		var teamUIDs []string
		teamLines, teamUIDs, err = memory.TeamSearch(s.st, projectID, s.dir, in.Query, in.Kind, remaining, gitBudget, now)
		if err != nil {
			return nil, nil, err
		}
		for _, uid := range teamUIDs {
			s.recordRetrieval(sessionID, projectID, "team", 0, uid)
		}
	}
	if len(ranked) == 0 && len(teamLines) == 0 {
		return textResult("no matching memories"), nil, nil
	}
	return textResult(mergeRendered(renderEntries(ranked, false), teamLines)), nil, nil
}

type listIn struct {
	IncludeSuperseded bool `json:"include_superseded,omitempty" jsonschema:"Also list superseded entries, marked with what replaced them."`
}

func (s *Server) list(ctx context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, any, error) {
	projectID, _, err := s.gate()
	if err != nil {
		return nil, nil, err
	}
	ranked, err := memory.List(s.st, projectID, s.dir, in.IncludeSuperseded, gitBudget)
	if err != nil {
		return nil, nil, err
	}
	teamLines, err := memory.TeamList(s.st, projectID, s.dir, gitBudget, time.Now())
	if err != nil {
		return nil, nil, err
	}
	if len(ranked) == 0 && len(teamLines) == 0 {
		return textResult("no memories for this project"), nil, nil
	}
	return textResult(mergeRendered(renderEntries(ranked, true), teamLines)), nil, nil
}

// mergeRendered joins the personal block and team lines into one result.
func mergeRendered(personal string, teamLines []string) string {
	parts := make([]string, 0, 2)
	if personal != "" {
		parts = append(parts, personal)
	}
	if len(teamLines) > 0 {
		parts = append(parts, strings.Join(teamLines, "\n"))
	}
	return strings.Join(parts, "\n")
}

func renderEntries(ranked []memory.Ranked, markSuperseded bool) string {
	var b strings.Builder
	for i, r := range ranked {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(memory.RenderEntry(r))
		if markSuperseded && r.Entry.SupersededBy.Valid {
			fmt.Fprintf(&b, " [superseded by %d]", r.Entry.SupersededBy.Int64)
		}
	}
	return b.String()
}

// gate re-evaluates the memory-active predicate on every call (consent,
// entitlement, or membership may change mid-session) and resolves the current
// project and its link. Memory is active when personal memory is active OR the
// project is a team project the account may read (a membership benefit — no
// Pro or sharing consent required). The error text follows the gating
// contract: "memory is <reason>: <one-line remedy>".
func (s *Server) gate() (projectID int64, link config.Link, err error) {
	if s.st == nil {
		return 0, config.Link{}, gateErr("temporarily unavailable", "check `agent-brain status` and diagnostics")
	}
	cfg, err := config.Load()
	if err != nil {
		return 0, config.Link{}, gateErr("temporarily unavailable", "check `agent-brain status` and diagnostics")
	}
	proj := attribution.Resolve(s.dir)
	projectID, err = s.st.UpsertProject(store.ProjectIdentity{
		Kind: proj.Kind, Identity: proj.Identity, DisplayName: proj.DisplayName,
	}, store.Now())
	if err != nil {
		return 0, config.Link{}, gateErr("temporarily unavailable", "check `agent-brain status` and diagnostics")
	}
	disabled, err := s.st.ProjectMemoryDisabled(projectID)
	if err != nil {
		return 0, config.Link{}, gateErr("temporarily unavailable", "check `agent-brain status` and diagnostics")
	}
	link = cfg.Links[config.LinkKey(proj.Kind, proj.Identity)]
	now := time.Now()
	if entitlement.MemoryActive(cfg, disabled, now) || entitlement.TeamMemoryActive(cfg, link, disabled) {
		return projectID, link, nil
	}
	reason := entitlement.Reason(cfg, disabled, now)
	// On an org-linked project, personal-entitlement vocabulary ("a Pro
	// feature", "not enabled") points at the wrong remedy: the member is gated
	// because team access is off (lapsed org subscription, removed membership,
	// or a capability not yet refreshed), which no personal upgrade fixes.
	if link.OrgProject && reason != "disabled for this project" {
		return 0, config.Link{}, gateErr("unavailable for this team project (membership or organization subscription inactive)",
			"run `agent-brain sync`, then check `agent-brain status` or the organization dashboard")
	}
	return 0, config.Link{}, gateErr(reason, remedy(reason))
}

func gateErr(reason, remedy string) error {
	return fmt.Errorf("memory is %s: %s", reason, remedy)
}

func remedy(reason string) string {
	switch reason {
	case "not enabled":
		return "run `agent-brain memory enable`"
	case "paused (entitlement unverified > 7 days)":
		return "reconnect to the network and run `agent-brain status` to re-verify Pro"
	case "a Pro feature":
		return "upgrade on your dashboard, then run `agent-brain memory enable`"
	case "disabled for this project":
		return "run `agent-brain memory enable --project` in this project to re-enable"
	default:
		return "check `agent-brain status`"
	}
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
