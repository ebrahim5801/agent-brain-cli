// Package codex is the adapter for OpenAI's Codex CLI: hook installation into
// the shared hooks.json, memory MCP registration in config.toml, and hook
// payload parsing. Everything Codex-specific stays behind this boundary
// (constitution VI); no prompt text, tool inputs, tool output, or assistant
// message bodies ever cross it into storage (constitution II).
//
// Verified against codex-cli 0.154.0. Two host behaviors shape this adapter and
// are documented where they bite: Codex will not run a hook until the user has
// approved it (see PostInstallNotice), and it clamps the SessionEnd hook
// timeout to 3 seconds regardless of what the file asks for.
package codex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant/settingsfile"
	"github.com/ebrahim5801/agent-brain-cli/internal/assistant/tomlfile"
)

// Assistant is the adapter name recorded on sessions.
const Assistant = "codex"

// MCPServerName is the registration key for the memory server.
const MCPServerName = "agent-brain-memory"

// mcpTableHeader is the TOML table our MCP registration owns in config.toml.
const mcpTableHeader = "mcp_servers." + MCPServerName

// hookEvents maps Codex CLI's native hook events onto agent-brain's neutral
// ones. Events Codex supports but we deliberately don't install: PreToolUse
// (redundant with PostToolUse, and the one event where returning
// additionalContext is an error), PermissionRequest (a gate, not an
// observation — agent-brain never alters an approval flow), Compact, and
// Interrupt. An adapter that cannot honestly produce an event installs no hook
// for it rather than synthesizing one.
var hookEvents = []struct {
	Event   string // Codex CLI hook event name
	Sub     string // agent-brain hook subcommand
	Trust   string // how Codex spells the event inside a [hooks.state] key
	Status  string // Codex-specific, shown to the user while the hook runs
	Timeout int    // seconds
}{
	{"SessionStart", "session-start", "session_start", "agent-brain: loading project memory", 10},
	{"UserPromptSubmit", "prompt", "user_prompt_submit", "agent-brain: refreshing project memory", 10},
	{"PostToolUse", "tool-use", "post_tool_use", "", 10},
	// SubagentStop fires silently — unlike every other event, Codex prints no
	// "hook: SubagentStop" line in the transcript — so its only observable
	// effect is the sub-session row it produces.
	{"SubagentStop", "subagent-stop", "subagent_stop", "", 10},
	{"Stop", "stop", "stop", "", 10},
	// Codex clamps SessionEnd to 3 seconds whatever the file asks for, and warns
	// on every session start when asked for more. Asking for 3 keeps the
	// warning away and states the real budget the usage backfill has to fit in.
	{"SessionEnd", "session-end", "session_end", "", 3},
}

// configHome returns $CODEX_HOME if set, else ~/.codex. CODEX_HOME is Codex
// CLI's own environment variable, so honoring it means a user who relocates
// their Codex home is followed correctly; it doubles as the test seam.
func configHome() (string, error) {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// HooksPath is Codex CLI's global hooks file. Users write their own hooks here,
// so it is shared and merged into, never owned.
func HooksPath() (string, error) {
	dir, err := configHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hooks.json"), nil
}

// ConfigPath is Codex CLI's global TOML config, which carries mcp_servers.
func ConfigPath() (string, error) {
	dir, err := configHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// Detected reports whether Codex CLI appears present on this machine. When
// CODEX_HOME is set, only that directory's presence is checked — the PATH
// lookup is skipped so tests stay hermetic instead of inheriting the
// developer's real binary.
func Detected() bool {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		fi, err := os.Stat(v)
		return err == nil && fi.IsDir()
	}
	if dir, err := configHome(); err == nil {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return true
		}
	}
	_, err := exec.LookPath("codex")
	return err == nil
}

func isOursCommand(command string) bool {
	return strings.Contains(command, "agent-brain") && strings.Contains(command, " hook ")
}

func isOursHook(hook map[string]any) bool {
	cmd, _ := hook["command"].(string)
	return isOursCommand(cmd)
}

// removeOurEntries strips agent-brain hook entries from every event the
// selector picks, dropping groups left with no hooks and events left with no
// groups. Foreign events, foreign groups, and foreign hooks inside our own
// groups are untouched. Install selects only the events it no longer installs;
// Uninstall selects them all.
func removeOurEntries(hooks map[string]any, selected func(event string) bool) bool {
	changed := false
	for event, v := range hooks {
		if !selected(event) {
			continue
		}
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		var keptGroups []any
		for _, g := range groups {
			group, ok := g.(map[string]any)
			if !ok {
				keptGroups = append(keptGroups, g)
				continue
			}
			inner, ok := group["hooks"].([]any)
			if !ok {
				keptGroups = append(keptGroups, g)
				continue
			}
			var keptHooks []any
			for _, h := range inner {
				if hook, ok := h.(map[string]any); ok && isOursHook(hook) {
					changed = true
					continue
				}
				keptHooks = append(keptHooks, h)
			}
			if len(keptHooks) == 0 {
				continue
			}
			group["hooks"] = keptHooks
			keptGroups = append(keptGroups, group)
		}
		if len(keptGroups) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keptGroups
		}
	}
	return changed
}

// hookCommand builds one event's hook entry.
func hookCommand(binPath, sub, status string, timeout int) map[string]any {
	// --assistant is mandatory. The hook subcommand's flag defaults to
	// claude-code, so omitting it would file every Codex session under Claude.
	h := map[string]any{
		"type":    "command",
		"command": binPath + " hook " + sub + " --assistant " + Assistant,
		"timeout": timeout,
	}
	if status != "" {
		h["statusMessage"] = status
	}
	return h
}

// hookEntry wraps a hook in its matcher group. The matcher key is omitted
// deliberately: an absent matcher matches every tool, which is what the
// collector wants, and avoids guessing at each event's matcher vocabulary.
func hookEntry(h map[string]any) map[string]any {
	return map[string]any{"hooks": []any{h}}
}

// upsertOurHook replaces our hook entry for one event where it already sits,
// rather than removing it and appending a fresh one at the end. Position
// matters for idempotence: a user who adds their own hook to an event we also
// use leaves the array as [ours, theirs], and remove-then-append would return
// [theirs, ours] — different bytes, so every reinstall would rewrite the file
// and take another backup. Returns the event's groups and whether ours was
// found; duplicates of ours are dropped so the entry stays unique.
func upsertOurHook(groups []any, h map[string]any) ([]any, bool) {
	replaced := false
	var kept []any
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			kept = append(kept, g)
			continue
		}
		inner, ok := group["hooks"].([]any)
		if !ok {
			kept = append(kept, g)
			continue
		}
		var keptHooks []any
		for _, x := range inner {
			if hook, ok := x.(map[string]any); ok && isOursHook(hook) {
				if replaced {
					continue
				}
				keptHooks = append(keptHooks, h)
				replaced = true
				continue
			}
			keptHooks = append(keptHooks, x)
		}
		if len(keptHooks) == 0 {
			continue
		}
		group["hooks"] = keptHooks
		kept = append(kept, group)
	}
	return kept, replaced
}

// Install merges agent-brain's hook entries into hooks.json and its MCP server
// registration into config.toml, additively. Each file is backed up
// individually before its first change, and a no-op re-install writes nothing
// to either and returns no backups.
func Install(binPath string) (backups []string, err error) {
	hPath, err := HooksPath()
	if err != nil {
		return nil, err
	}
	backup, err := settingsfile.BackupIfChanged(hPath, func(root map[string]any) (bool, error) {
		hooks, ok := root["hooks"].(map[string]any)
		if !ok {
			hooks = map[string]any{}
			root["hooks"] = hooks
		}
		wanted := map[string]bool{}
		for _, he := range hookEvents {
			wanted[he.Event] = true
		}
		// Events we used to install but no longer do still have to be cleaned.
		removeOurEntries(hooks, func(event string) bool { return !wanted[event] })
		for _, he := range hookEvents {
			groups, _ := hooks[he.Event].([]any)
			h := hookCommand(binPath, he.Sub, he.Status, he.Timeout)
			groups, replaced := upsertOurHook(groups, h)
			if !replaced {
				groups = append(groups, hookEntry(h))
			}
			hooks[he.Event] = groups
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if backup != "" {
		backups = append(backups, backup)
	}

	cPath, err := ConfigPath()
	if err != nil {
		return backups, err
	}
	backup, err = tomlfile.Upsert(cPath, mcpTable(binPath))
	if err != nil {
		return backups, err
	}
	if backup != "" {
		backups = append(backups, backup)
	}
	return backups, nil
}

// mcpTable is the config.toml block registering the memory server. The shape
// was confirmed by having Codex write it itself (`codex mcp add
// agent-brain-memory -- <bin> mcp`) rather than guessed.
func mcpTable(binPath string) tomlfile.Table {
	return tomlfile.Table{
		Header: mcpTableHeader,
		Lines: []string{
			`command = ` + tomlString(binPath),
			`args = ["mcp"]`,
		},
	}
}

// tomlString quotes a value as a TOML basic string. Only the escapes reachable
// from a filesystem path are handled; a path containing a control character is
// not something we can round-trip honestly.
func tomlString(s string) string { return tomlfile.Quote(s) }

// Uninstall removes exactly the entries Install added, leaving every foreign
// hook event, matcher group, hook, and MCP server untouched. A never-integrated
// machine is a success no-op. No backup is written; Install's backup is the
// restore path.
func Uninstall() error {
	hPath, err := HooksPath()
	if err != nil {
		return err
	}
	err = settingsfile.WriteIfChanged(hPath, func(root map[string]any) (bool, error) {
		hooks, ok := root["hooks"].(map[string]any)
		if !ok {
			return false, nil
		}
		changed := removeOurEntries(hooks, func(string) bool { return true })
		if len(hooks) == 0 {
			delete(root, "hooks")
		}
		return changed, nil
	})
	if err != nil {
		return err
	}

	cPath, err := ConfigPath()
	if err != nil {
		return err
	}
	return tomlfile.Remove(cPath, mcpTableHeader)
}

// InstalledEvents returns the Codex events that currently carry an agent-brain
// hook entry.
func InstalledEvents() ([]string, error) {
	hPath, err := HooksPath()
	if err != nil {
		return nil, err
	}
	root, existed, err := settingsfile.Load(hPath)
	if err != nil || !existed {
		return nil, err
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return nil, nil
	}
	var events []string
	for _, he := range hookEvents {
		groups, ok := hooks[he.Event].([]any)
		if !ok {
			continue
		}
		if anyOurs(groups) {
			events = append(events, he.Event)
		}
	}
	return events, nil
}

func anyOurs(groups []any) bool {
	_, _, ok := ourPosition(groups)
	return ok
}

// ourPosition locates our hook within one event's matcher groups, as the pair
// of indices Codex uses to name it in a trust key.
func ourPosition(groups []any) (group, hook int, ok bool) {
	for gi, g := range groups {
		m, isMap := g.(map[string]any)
		if !isMap {
			continue
		}
		inner, _ := m["hooks"].([]any)
		for hi, h := range inner {
			if entry, isMap := h.(map[string]any); isMap && isOursHook(entry) {
				return gi, hi, true
			}
		}
	}
	return 0, 0, false
}

// trustHeader is the config.toml table Codex writes when a user approves one
// hook: [hooks.state."<hooks.json path>:<event>:<group>:<hook>"], holding a
// trusted_hash of the approved definition.
func trustHeader(hooksPath, event string, group, hook int) string {
	key := fmt.Sprintf("%s:%s:%d:%d", hooksPath, event, group, hook)
	return "hooks.state." + tomlfile.Quote(key)
}

// TrustedEvents returns the events whose agent-brain hook the user has approved
// in Codex. Approval is what makes an installed hook actually run (§6), and
// Codex reports an unapproved one nowhere, so status reads the state directly.
//
// Presence of the table is the whole test; the hash it carries is not verified.
// Nothing in the file says how that digest is computed and it did not fall out
// of an exhaustive guess, so a stale entry — one approved before we rewrote the
// hook — still reads as approved. Both directions of the remaining error are
// survivable and the important one is exact: a hook that was never approved has
// no entry at all, which is the case that silently records nothing. And if our
// reading of the index pair is ever wrong, the lookup misses and status nags
// about approval that is already granted, rather than promising capture that
// is not happening.
func TrustedEvents() ([]string, error) {
	hPath, err := HooksPath()
	if err != nil {
		return nil, err
	}
	root, existed, err := settingsfile.Load(hPath)
	if err != nil || !existed {
		return nil, err
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return nil, nil
	}
	cPath, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	headers := map[string]string{}
	var want []string
	for _, he := range hookEvents {
		groups, ok := hooks[he.Event].([]any)
		if !ok {
			continue
		}
		group, hook, ok := ourPosition(groups)
		if !ok {
			continue
		}
		h := trustHeader(hPath, he.Trust, group, hook)
		headers[he.Event] = h
		want = append(want, h)
	}
	found, err := tomlfile.ContainsAll(cPath, want)
	if err != nil {
		return nil, err
	}
	var trusted []string
	for _, he := range hookEvents {
		if h, ok := headers[he.Event]; ok && found[h] {
			trusted = append(trusted, he.Event)
		}
	}
	return trusted, nil
}

// ExpectedEventCount is how many hook events a healthy install registers. It is
// derived from the table rather than hardcoded: DeriveTier compares installed
// against wanted, so a stale constant would report partial forever.
func ExpectedEventCount() int {
	return len(hookEvents)
}

// MCPRegistered reports whether the memory server table exists in config.toml.
func MCPRegistered() (bool, error) {
	cPath, err := ConfigPath()
	if err != nil {
		return false, err
	}
	return tomlfile.Contains(cPath, mcpTableHeader)
}
