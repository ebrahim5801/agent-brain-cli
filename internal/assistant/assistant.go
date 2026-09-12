// Package assistant defines the adapter contract every coding assistant
// integration implements and the registry the CLI iterates. The CLI (install,
// uninstall, status, hook) talks only to this interface and the registry;
// nothing assistant-specific appears outside an adapter package (constitution
// VI). Each adapter whitelists neutral fields at its ParseHook boundary, so
// prompt text, tool bodies, and model messages are structurally dropped before
// anything reaches storage (constitution II).
package assistant

import (
	"io"
	"sort"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// Neutral internal hook events. Adapters map each assistant's native events
// onto these; an adapter that cannot honestly produce an event simply does not
// install a hook for it — events are never synthesized.
const (
	EventSessionStart = "session-start"
	EventPrompt       = "prompt"
	EventToolUse      = "tool-use"
	EventModelUsage   = "model-usage" // incremental usage delta (Gemini AfterModel)
	EventSubagentStop = "subagent-stop"
	EventStop         = "stop"
	EventSessionEnd   = "session-end"
)

// HookInput is the complete neutral vocabulary a hook payload may contribute.
// These six fields are the whitelist: prompt text, tool inputs/outputs, file
// contents, and model message bodies are unrepresentable past ParseHook.
type HookInput struct {
	SessionKey     string       // native session/conversation id; required
	Cwd            string       // working dir or first workspace root, for attribution
	TranscriptPath string       // optional; stored on the session, feeds usage backfill
	ToolName       string       // tool-use events only, name only
	Model          string       // when the payload states it
	Usage          *store.Usage // model-usage events only; nil elsewhere
	Subagent       *SubagentRef // subagent-stop events only; nil elsewhere
}

// SubagentRef identifies the child run a subagent-stop payload names, for
// assistants that report it on the hook rather than leaving it to be discovered
// on disk (Codex CLI's agent_id / agent_transcript_path; Claude Code instead
// writes agent transcripts into a directory beside the session's own). All
// three members are identifiers and a path — no message text, no task text, and
// no vocabulary another adapter has to agree on.
type SubagentRef struct {
	AgentID        string
	AgentType      string
	TranscriptPath string
}

// Tier is the plain-language integration level shown in status.
type Tier string

const (
	TierFull          Tier = "full"           // expected hooks installed and MCP registered
	TierMCPOnly       Tier = "mcp-only"       // MCP registered, no usable hooks
	TierPartial       Tier = "partial"        // some but not all expected surfaces (broken)
	TierUnsupported   Tier = "unsupported"    // detected, no usable integration surface
	TierNotIntegrated Tier = "not-integrated" // detected, nothing installed yet
)

// State is on-disk integration truth at call time.
type State struct {
	Detected         bool
	HookEvents       int      // installed hook events carrying our command
	HookEventsWanted int      // expected hook events for a healthy install
	MCPRegistered    bool     // memory server entry present
	Tier             Tier     // derived from the counts above
	Notes            []string // honest field-level caveats for status display
}

// Integrated reports whether any agent-brain surface is present.
func (s State) Integrated() bool {
	return s.HookEvents > 0 || s.MCPRegistered
}

// DeriveTier computes the tier from installed-surface reality, never a static
// table (FR-004). An adapter with no wanted hooks (hookless) reaches full on
// MCP alone.
func DeriveTier(hookEvents, hookEventsWanted int, mcp bool) Tier {
	hooksComplete := hookEvents >= hookEventsWanted
	switch {
	case hooksComplete && mcp:
		return TierFull
	case hookEvents == 0 && mcp:
		return TierMCPOnly
	case hookEvents == 0 && !mcp:
		return TierNotIntegrated
	default:
		return TierPartial
	}
}

// Adapter is the contract every assistant integration implements.
type Adapter interface {
	// Name is the stable identity recorded on synced sessions. Lowercase,
	// hyphenated, no ':' (the session-key scope separator). Never changes.
	Name() string
	// DisplayName is the human name shown in install/status.
	DisplayName() string
	// Detected reports whether the assistant is present (config dir or binary
	// on PATH). Freshly evaluated per call; creates nothing on disk.
	Detected() bool
	// Install idempotently writes our hook + MCP entries, backing up each
	// changed file first; returns the backup paths created (empty on a no-op).
	Install(binPath string) (backups []string, err error)
	// Uninstall removes exactly our entries; a never-integrated assistant is a
	// success no-op.
	Uninstall() error
	// State reads current on-disk integration truth.
	State() (State, error)
	// ParseHook decodes stdin into the neutral HookInput for one event.
	ParseHook(event string, stdin io.Reader) (HookInput, error)
	// InjectionResponse wraps the memory pack in the assistant's session-start
	// context-injection envelope; "" when unsupported (stage skipped).
	InjectionResponse(pack string) string
	// StopResponse wraps the distillation prompt in the assistant's stop
	// redirect envelope; "" when unsupported (stage skipped).
	StopResponse(reason string) string
}

// UsageBackfiller is implemented by adapters that recover a session's absolute
// usage totals at session-end from a transcript or events file (Claude Code's
// transcript, Copilot CLI's events.jsonl). Adapters that report usage live
// (Gemini's model-usage) or not at all (Cursor) do not implement it; the hook
// wrapper type-asserts for it and skips backfill when absent.
type UsageBackfiller interface {
	// BackfillUsage returns the session's absolute usage, its dominant model,
	// and a per-model breakdown (one entry per model used, heaviest first;
	// single-element for assistants that report one model). ok is false on
	// honest absence, leaving the session's counters untouched.
	BackfillUsage(input HookInput) (usage store.Usage, model string, models []store.ModelUsage, ok bool)
}

// InjectionNotifier is implemented by adapters whose session-start protocol
// carries a user-visible message alongside the injected context (Claude
// Code's top-level systemMessage). The notice is display-only — it never
// enters the model's context. Adapters without such a channel don't implement
// it and the hook wrapper falls back to InjectionResponse.
type InjectionNotifier interface {
	// InjectionResponseWithNotice is InjectionResponse plus a user-facing
	// notice; an empty notice is byte-identical to InjectionResponse.
	InjectionResponseWithNotice(pack, notice string) string
}

// Subagent is one completed subagent run under a session, recovered by an
// adapter that supports it. Summary (task description + final report) and
// Prompt (the task text the parent assistant wrote when spawning the agent —
// model-authored, not the user's) are content destined for local-only columns;
// the remaining fields are the same neutral vocabulary sessions already use.
type Subagent struct {
	AgentID        string
	AgentType      string
	Model          string
	StartedAt      string
	EndedAt        string
	TranscriptPath string
	Summary        string
	Prompt         string
	Usage          store.Usage
}

// SubagentScanner is implemented by adapters whose assistant runs subagents
// and records their transcripts (Claude Code's Task tool). The scan is
// idempotent — it reports the current complete set for the session, and the
// hook wrapper upserts. Adapters without subagents don't implement it.
type SubagentScanner interface {
	ScanSubagents(input HookInput) []Subagent
}

// CitationScanner is implemented by adapters that can recover, from a
// session's transcript, which memory ids/handles the assistant restated in
// its replies (Claude Code's JSONL transcript). ok reports whether the scan
// ran at all (a transcript path was available and readable); it is not a
// judgment on whether anything was found — an empty result with ok=true is a
// legitimate "no citations". The hook wrapper intersects the returned ids
// against the session's actual retrievals before recording anything (the
// false-positive guard lives with the caller, not the adapter). Adapters
// without a scannable transcript don't implement it — retrieval is still
// recorded, just not citation.
type CitationScanner interface {
	ScanCitations(input HookInput) (personalIDs []int64, teamHandles []string, ok bool)
}

// PromptResponder is implemented by adapters whose prompt hook processes
// stdout (Cursor's beforeSubmitPrompt); the response must always let the
// prompt proceed — the collector never blocks a session. Adapters without it
// emit nothing on prompt events.
type PromptResponder interface {
	PromptResponse() string
}

// PromptInjector is implemented by adapters whose pre-prompt hook can inject
// context into the model's input for the coming turn (Claude Code's
// UserPromptSubmit, Gemini CLI's BeforeAgent — both via
// hookSpecificOutput.additionalContext). The hook wrapper calls this on every
// prompt event with a dedup-walk memory pack, so memory is refreshed before the
// model acts; "" skips (empty pack). Adapters whose pre-prompt hook cannot
// inject (Cursor's beforeSubmitPrompt is gate-only; Copilot's
// userPromptSubmitted output is not processed) do not implement it — they fall
// back to ToolUseInjector.
type PromptInjector interface {
	PromptInjectionResponse(pack string) string
}

// ToolUseInjector is implemented by adapters that cannot inject before the
// prompt but can inject context on their post-tool-use hook (Cursor's
// postToolUse additional_context, Copilot's postToolUse additionalContext). The
// hook wrapper calls this once per turn — on the first tool-use after a prompt —
// as a best-effort fallback that keeps memory in context mid-task rather than
// only at session start. "" skips (empty pack).
type ToolUseInjector interface {
	ToolUseInjectionResponse(pack string) string
}

// PostInstallNotice is implemented by adapters whose host requires a manual
// step before the installed hooks take effect (Codex CLI will not run a hook
// until the user has approved its exact definition, and an unapproved hook is a
// silent no-op). The install command prints the notice after a successful
// install; adapters whose install is complete on its own don't implement it.
type PostInstallNotice interface {
	PostInstallNotice() string
}

// MidTurnResponder is implemented by adapters whose tool-use hook can push an
// actionable prompt back to the model while the agent is still working — used to
// trigger an interim memory checkpoint. The envelope matches the adapter's stop
// redirect (a blocking decision, or Cursor's follow-up message). Adapters whose
// assistant cannot honestly force a model turn mid-turn do not implement it, and
// the checkpoint stage no-ops for them (Stop distillation still covers the
// session).
type MidTurnResponder interface {
	MidTurnResponse(reason string) string
}

// Canonical registry order. Each adapter self-registers from its package init
// with its order key, so Registry is deterministic regardless of the order Go
// runs the inits (the composition root blank-imports the adapter packages to
// trigger registration). Keeping the order here — rather than a static adapter
// list in this package — avoids an import cycle (adapters import this package
// for the interface types).
const (
	OrderClaude   = 0
	OrderCursor   = 1
	OrderCopilot  = 2
	OrderGemini   = 3
	OrderOpenCode = 4
	OrderCodex    = 5
)

type registered struct {
	order int
	a     Adapter
}

var registry []registered

// Register adds an adapter at its canonical order slot. Called from adapter
// package inits; not for general use.
func Register(order int, a Adapter) {
	registry = append(registry, registered{order, a})
	sort.Slice(registry, func(i, j int) bool { return registry[i].order < registry[j].order })
}

// Registry returns the adapters in canonical order
// (claude-code, cursor, copilot-cli, gemini-cli, opencode, codex).
func Registry() []Adapter {
	out := make([]Adapter, len(registry))
	for i, r := range registry {
		out[i] = r.a
	}
	return out
}

// ByName returns the adapter with the given Name, if registered.
func ByName(name string) (Adapter, bool) {
	for _, r := range registry {
		if r.a.Name() == name {
			return r.a, true
		}
	}
	return nil, false
}
