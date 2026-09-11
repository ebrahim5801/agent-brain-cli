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
	Event  string // Codex CLI hook event name
	Sub    string // agent-brain hook subcommand
	Status string // Codex-specific, shown to the user while the hook runs
}{
	{"SessionStart", "session-start", "agent-brain: loading project memory"},
	{"UserPromptSubmit", "prompt", "agent-brain: refreshing project memory"},
	{"PostToolUse", "tool-use", ""},
	{"Stop", "stop", ""},
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

// removeOurEntries strips agent-brain hook entries from every event's groups,
// dropping groups left with no hooks and events left with no groups. Foreign
// events, foreign groups, and foreign hooks inside our own groups are untouched.
func removeOurEntries(hooks map[string]any) bool {
	changed := false
	for event, v := range hooks {
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

// hookEntry builds one event's matcher group. The matcher key is omitted
// deliberately: an absent matcher matches every tool, which is what the
// collector wants, and avoids guessing at each event's matcher vocabulary.
func hookEntry(binPath, sub, status string) map[string]any {
	// --assistant is mandatory. The hook subcommand's flag defaults to
	// claude-code, so omitting it would file every Codex session under Claude.
	h := map[string]any{
		"type":    "command",
		"command": binPath + " hook " + sub + " --assistant " + Assistant,
		"timeout": 10,
	}
	if status != "" {
		h["statusMessage"] = status
	}
	return map[string]any{"hooks": []any{h}}
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
		removeOurEntries(hooks)
		for _, he := range hookEvents {
			groups, _ := hooks[he.Event].([]any)
			hooks[he.Event] = append(groups, hookEntry(binPath, he.Sub, he.Status))
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
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

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
		changed := removeOurEntries(hooks)
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
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := group["hooks"].([]any)
		for _, h := range inner {
			if hook, ok := h.(map[string]any); ok && isOursHook(hook) {
				return true
			}
		}
	}
	return false
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
