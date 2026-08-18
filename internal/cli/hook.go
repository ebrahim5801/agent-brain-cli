package cli

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/assistant/claude"
	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/diag"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func newHookCmd() *cobra.Command {
	var assistantName string
	cmd := &cobra.Command{
		Use:    "hook <event>",
		Short:  "Internal capture endpoint invoked by the assistant's hooks",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		// The hook contract: always exit 0, errors only to diagnostics — a
		// collector failure must never disturb the assistant session
		// (FR-009). stdout carries only well-formed hook JSON (memory
		// injection at session-start, distillation decision at stop) and
		// stays empty otherwise.
		RunE: func(cmd *cobra.Command, args []string) error {
			if out := runHook(args[0], assistantName); out != "" {
				fmt.Print(out)
			}
			return nil
		},
	}
	// Defaults to claude-code so hook entries installed before multi-assistant
	// support (which carry no flag) keep resolving to the Claude adapter.
	cmd.Flags().StringVar(&assistantName, "assistant", claude.Assistant, "assistant whose hook payload this is")
	return cmd
}

func fallbackLogger() *diag.Logger {
	path, err := store.DiagLogPath()
	if err != nil {
		return &diag.Logger{}
	}
	return &diag.Logger{FallbackPath: path}
}

// validEvent reports whether event is one of the neutral hook events.
func validEvent(event string) bool {
	switch event {
	case assistant.EventSessionStart, assistant.EventPrompt, assistant.EventToolUse,
		assistant.EventModelUsage, assistant.EventSubagentStop, assistant.EventStop,
		assistant.EventSessionEnd:
		return true
	}
	return false
}

// scopedExternalID namespaces a native session key by adapter so two
// assistants can never collide on the globally-unique external_id. Claude Code
// stays bare for upgrade continuity with sessions already on disk.
func scopedExternalID(name, sessionKey string) string {
	if name == claude.Assistant {
		return sessionKey
	}
	return name + ":" + sessionKey
}

func runHook(event, assistantName string) (stdout string) {
	defer func() { _ = recover() }()

	adapter, ok := assistant.ByName(assistantName)
	if !ok {
		fallbackLogger().Log("hook", "unknown assistant %q", assistantName)
		return ""
	}

	st, err := store.Open()
	if err != nil {
		fallbackLogger().Log("hook", "open store: %v", err)
		return ""
	}
	defer st.Close()
	logPath, _ := store.DiagLogPath()
	logger := &diag.Logger{DB: st.DB, Rebind: st.Rebind, FallbackPath: logPath}

	if !validEvent(event) {
		logger.Log("hook", "unknown event %q", event)
		return ""
	}

	input, err := adapter.ParseHook(event, os.Stdin)
	if err != nil {
		logger.Log("hook", "%s: bad payload: %v", event, err)
		return ""
	}
	// A session key containing the sub-session namespace separator would make
	// this session's external_id collide with a sub-session row's
	// ("<parent>/agent-<id>"); no assistant emits such a key, so the payload
	// is hostile or corrupt and is dropped.
	if strings.Contains(input.SessionKey, "/agent-") {
		logger.Log("hook", "%s: session key %q collides with the sub-session namespace", event, input.SessionKey)
		return ""
	}

	at := store.Now()
	proj := attribution.Resolve(input.Cwd)
	projectID, err := st.UpsertProject(store.ProjectIdentity{
		Kind: proj.Kind, Identity: proj.Identity, DisplayName: proj.DisplayName,
	}, at)
	if err != nil {
		logger.Log("hook", "%s: upsert project: %v", event, err)
		return ""
	}
	externalID := scopedExternalID(adapter.Name(), input.SessionKey)
	sessionID, err := st.EnsureSession(externalID, projectID, at, input.TranscriptPath, adapter.Name())
	if err != nil {
		logger.Log("hook", "%s: ensure session: %v", event, err)
		return ""
	}

	// A model named in a base hook field (Cursor) is recorded without touching
	// token counters. model-usage carries its own model via AddUsage.
	if input.Model != "" && event != assistant.EventModelUsage {
		if err := st.SetModel(sessionID, input.Model); err != nil {
			logger.Log("hook", "set model: %v", err)
		}
	}

	switch event {
	case assistant.EventSessionStart:
		appendEvent(st, logger, sessionID, "session_start", "", at)
		reconcile(st, logger)
		pack := sessionStartInjection(st, logger, sessionID, projectID, input.Cwd)
		// The link offer rides the same injection; it must go out even when the
		// pack is empty (a brand-new project has no memories yet).
		text := pack.Text
		if offer := linkOfferNote(logger, input.Cwd); offer != "" {
			if text != "" {
				text += "\n"
			}
			text += offer
		}
		if text != "" {
			if n, ok := adapter.(assistant.InjectionNotifier); ok {
				stdout = n.InjectionResponseWithNotice(text, injectionNotice(pack))
			} else {
				stdout = adapter.InjectionResponse(text)
			}
		}
	case assistant.EventPrompt:
		appendEvent(st, logger, sessionID, "prompt", "", at)
		// Adapters whose pre-prompt hook can inject (Claude, Gemini) refresh
		// memory before the model acts — once per prompt, which the prompt event
		// already is. The rest fall through to PromptResponder (Cursor's
		// continue pass-through) and rely on the tool-use fallback below.
		if pi, ok := adapter.(assistant.PromptInjector); ok {
			if pack := promptInjection(st, logger, sessionID, projectID, input.Cwd); pack.Text != "" {
				recordPackRetrieval(st, logger, sessionID, projectID, pack)
				stdout = pi.PromptInjectionResponse(pack.Text)
			}
		} else if pr, ok := adapter.(assistant.PromptResponder); ok {
			stdout = pr.PromptResponse()
		}
	case assistant.EventToolUse:
		appendEvent(st, logger, sessionID, "tool_use", input.ToolName, at)
		// Only claim a checkpoint for an adapter that can actually push the
		// prompt back to the model mid-turn; otherwise the claim would advance
		// memory_checkpointed_at and burn the re-arm baseline with nothing
		// emitted.
		if mr, ok := adapter.(assistant.MidTurnResponder); ok {
			if reason := checkpointCapture(st, logger, sessionID, projectID, checkpointToolThreshold); reason != "" {
				stdout = mr.MidTurnResponse(reason)
			}
		}
		// Tool-use memory-injection fallback for adapters that cannot inject
		// pre-prompt (Cursor, Copilot): once per turn, on the first tool-use
		// after a prompt, and only when a checkpoint did not already claim this
		// invocation's single stdout — the injection stays armed for the next
		// tool-use if so.
		//
		// The pack is built BEFORE the claim so a gated, failed, or empty build
		// never burns the turn's single injection with nothing emitted (the same
		// reasoning as the checkpoint claim above); the claim still runs before
		// anything is emitted, so concurrent tool-use hooks resolve to one winner.
		if stdout == "" {
			if ti, ok := adapter.(assistant.ToolUseInjector); ok {
				if pack := promptInjection(st, logger, sessionID, projectID, input.Cwd); pack.Text != "" {
					won, err := st.MarkSessionPromptInjected(sessionID, at)
					if err != nil {
						logger.Log("hook", "mark prompt injected: %v", err)
					} else if won {
						recordPackRetrieval(st, logger, sessionID, projectID, pack)
						stdout = ti.ToolUseInjectionResponse(pack.Text)
					}
				}
			}
		}
	case assistant.EventModelUsage:
		// Usage is session state, not an activity event: no event row is
		// appended, so event counts stay comparable across assistants.
		if input.Usage != nil {
			if err := st.AddUsage(sessionID, input.Model, *input.Usage); err != nil {
				logger.Log("hook", "add usage: %v", err)
			}
		}
	case assistant.EventSubagentStop:
		captureSubagents(st, logger, adapter, sessionID, projectID, externalID, input)
	case assistant.EventStop:
		if reason := stopDistillation(st, logger, sessionID, projectID); reason != "" {
			stdout = adapter.StopResponse(reason)
		}
		appendEvent(st, logger, sessionID, "stop", "", at)
		captureSubagents(st, logger, adapter, sessionID, projectID, externalID, input)
	case assistant.EventSessionEnd:
		appendEvent(st, logger, sessionID, "session_end", "", at)
		backfillUsage(st, logger, adapter, sessionID, input)
		captureSubagents(st, logger, adapter, sessionID, projectID, externalID, input)
		recordCitations(st, logger, adapter, sessionID, projectID, input)
		if err := st.CloseSession(sessionID, at, "normal"); err != nil {
			logger.Log("hook", "close session: %v", err)
		}
		reconcile(st, logger)
	}
	return stdout
}

func appendEvent(st *store.Store, logger *diag.Logger, sessionID int64, kind, detail, at string) {
	if err := st.AppendEvent(sessionID, kind, detail, at); err != nil {
		logger.Log("hook", "append %s: %v", kind, err)
	}
}

// backfillUsage recovers absolute usage totals at session-end for adapters that
// support it (Claude transcript, Copilot events.jsonl). Adapters that report
// usage live or not at all don't implement UsageBackfiller and are skipped. The
// transcript path stored at an earlier event fills in when this payload omits
// it.
func backfillUsage(st *store.Store, logger *diag.Logger, adapter assistant.Adapter, sessionID int64, input assistant.HookInput) {
	bf, ok := adapter.(assistant.UsageBackfiller)
	if !ok {
		return
	}
	if input.TranscriptPath == "" {
		var stored sql.NullString
		if err := st.QueryRow(`SELECT transcript_path FROM sessions WHERE id = ?`, sessionID).Scan(&stored); err == nil && stored.Valid {
			input.TranscriptPath = stored.String
		}
	}
	usage, model, models, ok := bf.BackfillUsage(input)
	if !ok {
		return
	}
	if err := st.SetUsage(sessionID, model, usage); err != nil {
		logger.Log("hook", "set usage: %v", err)
	}
	if err := st.ReplaceModelUsage(sessionID, models); err != nil {
		logger.Log("hook", "replace model usage: %v", err)
	}
}

// captureSubagents upserts the session's subagent runs as sub-sessions
// (subagent-stop for live capture; stop and session-end re-scan to catch any
// run a missed hook left behind). The stored transcript path is preferred over
// the payload's: hook payloads always name the main session, so the stored
// path is the one the subagents directory sits next to.
func captureSubagents(st *store.Store, logger *diag.Logger, adapter assistant.Adapter, sessionID, projectID int64, parentExternalID string, input assistant.HookInput) {
	sc, ok := adapter.(assistant.SubagentScanner)
	if !ok {
		return
	}
	var stored sql.NullString
	if err := st.QueryRow(`SELECT transcript_path FROM sessions WHERE id = ?`, sessionID).Scan(&stored); err == nil && stored.Valid {
		input.TranscriptPath = stored.String
	}
	for _, sa := range sc.ScanSubagents(input) {
		if sa.AgentID == "" {
			continue
		}
		ss := store.SubSession{
			ExternalID:     parentExternalID + "/agent-" + sa.AgentID,
			ProjectID:      projectID,
			ParentID:       sessionID,
			Assistant:      adapter.Name(),
			AgentType:      sa.AgentType,
			Model:          sa.Model,
			StartedAt:      sa.StartedAt,
			EndedAt:        sa.EndedAt,
			TranscriptPath: sa.TranscriptPath,
			Summary:        sa.Summary,
			Prompt:         sa.Prompt,
			Usage:          sa.Usage,
		}
		if ss.StartedAt == "" {
			ss.StartedAt = store.Now()
		}
		if _, err := st.UpsertSubSession(ss); err != nil {
			logger.Log("hook", "upsert sub-session %s: %v", ss.ExternalID, err)
		}
	}
}

// reconcile closes stale sessions, backfilling usage through the owning
// assistant's adapter only — a transcript is never read with another
// assistant's parser, so a foreign format can't overwrite honest counters.
func reconcile(st *store.Store, logger *diag.Logger) {
	_, err := st.ReconcileStale(func(assistantName, transcriptPath string) (store.Usage, string, []store.ModelUsage, bool) {
		adapter, ok := assistant.ByName(assistantName)
		if !ok {
			return store.Usage{}, "", nil, false
		}
		bf, ok := adapter.(assistant.UsageBackfiller)
		if !ok {
			return store.Usage{}, "", nil, false
		}
		return bf.BackfillUsage(assistant.HookInput{TranscriptPath: transcriptPath})
	})
	if err != nil {
		logger.Log("reconcile", "%v", err)
	}
}
