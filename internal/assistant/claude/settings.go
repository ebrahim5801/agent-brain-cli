// Package claude is the adapter for Claude Code: settings integration, hook
// payload parsing, and transcript usage extraction. Everything Claude-specific
// stays behind this boundary (constitution VI); no message text or tool bodies
// ever cross it into storage (constitution II).
package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant/settingsfile"
)

// Assistant is the adapter name recorded on sessions.
const Assistant = "claude-code"

var hookEvents = []struct {
	Event string // Claude Code hook event name
	Sub   string // agent-brain hook subcommand
}{
	{"SessionStart", "session-start"},
	{"UserPromptSubmit", "prompt"},
	{"PostToolUse", "tool-use"},
	{"SubagentStop", "subagent-stop"},
	{"Stop", "stop"},
	{"SessionEnd", "session-end"},
}

func configDir() (string, error) {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

func SettingsPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// Detected reports whether Claude Code appears present on this machine.
func Detected() bool {
	if dir, err := configDir(); err == nil {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return true
		}
	}
	_, err := exec.LookPath("claude")
	return err == nil
}

func isOurs(command string) bool {
	return strings.Contains(command, "agent-brain") && strings.Contains(command, " hook ")
}

// removeOurEntries strips agent-brain hook commands from every event's matcher
// groups, dropping groups (and events) left empty. User entries are untouched.
// removeOurEntries strips agent-brain hook entries from a Claude hooks map and
// reports whether anything was actually removed.
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
				hook, ok := h.(map[string]any)
				if ok {
					if cmd, _ := hook["command"].(string); isOurs(cmd) {
						changed = true
						continue
					}
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

// Install merges agent-brain's hook entries into the global Claude Code
// settings, additively: existing user configuration is preserved, a timestamped
// backup is written first when the file changes, and re-running updates our
// entries in place (a no-op re-install writes nothing).
func Install(binPath string) (backupPath string, err error) {
	path, err := SettingsPath()
	if err != nil {
		return "", err
	}
	return settingsfile.BackupIfChanged(path, func(root map[string]any) (bool, error) {
		hooks, ok := root["hooks"].(map[string]any)
		if !ok {
			hooks = map[string]any{}
			root["hooks"] = hooks
		}
		removeOurEntries(hooks)
		for _, he := range hookEvents {
			entry := map[string]any{
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": binPath + " hook " + he.Sub,
					"timeout": 10,
				}},
			}
			groups, _ := hooks[he.Event].([]any)
			hooks[he.Event] = append(groups, entry)
		}
		return true, nil
	})
}

// Uninstall removes exactly the entries Install added. No backup is written;
// the pre-integration backup from Install is the restore path. The write is
// change-guarded, so uninstalling an assistant that was never integrated (or a
// second uninstall) leaves the user's settings byte-for-byte untouched (FR-009).
func Uninstall() error {
	path, err := SettingsPath()
	if err != nil {
		return err
	}
	return settingsfile.WriteIfChanged(path, func(root map[string]any) (bool, error) {
		hooks, ok := root["hooks"].(map[string]any)
		if !ok {
			return false, nil
		}
		removed := removeOurEntries(hooks)
		if len(hooks) == 0 {
			delete(root, "hooks")
		}
		return removed, nil
	})
}

// InstalledEvents returns the Claude Code events that currently carry an
// agent-brain hook entry in the settings file.
func InstalledEvents() ([]string, error) {
	path, err := SettingsPath()
	if err != nil {
		return nil, err
	}
	root, existed, err := settingsfile.Load(path)
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
		for _, g := range groups {
			group, ok := g.(map[string]any)
			if !ok {
				continue
			}
			inner, _ := group["hooks"].([]any)
			for _, h := range inner {
				if hook, ok := h.(map[string]any); ok {
					if cmd, _ := hook["command"].(string); isOurs(cmd) {
						events = append(events, he.Event)
					}
				}
			}
		}
	}
	return events, nil
}

// ExpectedEventCount is how many hook events a healthy install registers.
func ExpectedEventCount() int {
	return len(hookEvents)
}

// MCPServerName is the registration key for the memory server.
const MCPServerName = "agent-brain-memory"

// MCPConfigPath is Claude Code's user-level MCP registry (~/.claude.json).
// CLAUDE_CONFIG_DIR relocates it for tests, mirroring configDir.
func MCPConfigPath() (string, error) {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return filepath.Join(v, ".claude.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

// RegisterMCP idempotently adds (or updates) the memory MCP server entry,
// preserving every unrelated key and server registration. It writes in place
// without a backup, matching the pre-multi-assistant behavior (the settings
// backup from Install already covers the integration).
func RegisterMCP(binPath string) error {
	path, err := MCPConfigPath()
	if err != nil {
		return err
	}
	// Change-guarded: ~/.claude.json holds a large amount of unrelated user
	// state, so re-running install or `memory enable` when the entry is already
	// identical must not rewrite (and reformat) the whole file (FR-006/008).
	return settingsfile.WriteIfChanged(path, func(root map[string]any) (bool, error) {
		servers, ok := root["mcpServers"].(map[string]any)
		if !ok {
			servers = map[string]any{}
			root["mcpServers"] = servers
		}
		servers[MCPServerName] = map[string]any{
			"type":    "stdio",
			"command": binPath,
			"args":    []any{"mcp"},
		}
		return true, nil
	})
}

// UnregisterMCP removes exactly the entry RegisterMCP added.
func UnregisterMCP() error {
	path, err := MCPConfigPath()
	if err != nil {
		return err
	}
	return settingsfile.WriteIfChanged(path, func(root map[string]any) (bool, error) {
		servers, ok := root["mcpServers"].(map[string]any)
		if !ok {
			return false, nil
		}
		if _, present := servers[MCPServerName]; !present {
			return false, nil
		}
		delete(servers, MCPServerName)
		if len(servers) == 0 {
			delete(root, "mcpServers")
		}
		return true, nil
	})
}

// MCPRegistered reports whether the memory server entry exists and the
// command it points at.
func MCPRegistered() (bool, string, error) {
	path, err := MCPConfigPath()
	if err != nil {
		return false, "", err
	}
	root, existed, err := settingsfile.Load(path)
	if err != nil || !existed {
		return false, "", err
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		return false, "", nil
	}
	entry, ok := servers[MCPServerName].(map[string]any)
	if !ok {
		return false, "", nil
	}
	cmd, _ := entry["command"].(string)
	return true, cmd, nil
}
