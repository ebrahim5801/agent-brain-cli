// Package gemini is the adapter for Gemini CLI: settings integration (hooks +
// MCP in one file) and hook payload parsing. Everything Gemini-specific stays
// behind this boundary (constitution VI); no message text, tool bodies, or
// model response content ever cross it into storage (constitution II).
package gemini

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant/settingsfile"
)

// Assistant is the adapter name recorded on sessions.
const Assistant = "gemini-cli"

// MCPServerName is the registration key for the memory server.
const MCPServerName = "agent-brain-memory"

var hookEvents = []struct {
	Event string // Gemini CLI hook event name
	Sub   string // agent-brain hook subcommand
	Name  string // agent-brain hook entry name
}{
	{"SessionStart", "session-start", "agent-brain-session-start"},
	{"BeforeAgent", "prompt", "agent-brain-prompt"},
	{"AfterTool", "tool-use", "agent-brain-tool-use"},
	{"AfterModel", "model-usage", "agent-brain-model-usage"},
	{"AfterAgent", "stop", "agent-brain-stop"},
	{"SessionEnd", "session-end", "agent-brain-session-end"},
}

func configDir() (string, error) {
	if v := os.Getenv("AGENT_BRAIN_GEMINI_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini"), nil
}

// SettingsPath is Gemini CLI's single global settings file, carrying both
// hooks and mcpServers.
func SettingsPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// Detected reports whether Gemini CLI appears present on this machine. When
// AGENT_BRAIN_GEMINI_HOME is set (test seam), only that directory's presence
// is checked — PATH lookup is skipped so tests stay hermetic.
func Detected() bool {
	if v := os.Getenv("AGENT_BRAIN_GEMINI_HOME"); v != "" {
		fi, err := os.Stat(v)
		return err == nil && fi.IsDir()
	}
	if dir, err := configDir(); err == nil {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return true
		}
	}
	_, err := exec.LookPath("gemini")
	return err == nil
}

func isOursCommand(command string) bool {
	return strings.Contains(command, "agent-brain") && strings.Contains(command, " hook ")
}

func isOursHook(hook map[string]any) bool {
	if name, _ := hook["name"].(string); strings.HasPrefix(name, "agent-brain-") {
		return true
	}
	cmd, _ := hook["command"].(string)
	return isOursCommand(cmd)
}

// removeOurEntries strips agent-brain hook entries from every event's
// matcher groups, dropping groups (and events) left empty. Foreign entries
// are untouched.
// removeOurEntries strips agent-brain hook entries and reports whether anything
// was removed.
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

// Install merges agent-brain's hook entries and MCP server registration into
// the global Gemini CLI settings file, additively: existing user
// configuration is preserved, a single timestamped backup covers both
// surfaces when the file changes, and re-running updates our entries in
// place (a no-op re-install writes nothing).
func Install(binPath string) (backups []string, err error) {
	path, err := SettingsPath()
	if err != nil {
		return nil, err
	}
	backup, err := settingsfile.BackupIfChanged(path, func(root map[string]any) (bool, error) {
		hooks, ok := root["hooks"].(map[string]any)
		if !ok {
			hooks = map[string]any{}
			root["hooks"] = hooks
		}
		removeOurEntries(hooks)
		for _, he := range hookEvents {
			entry := map[string]any{
				"matcher": "*",
				"hooks": []any{map[string]any{
					"name":    he.Name,
					"type":    "command",
					"command": binPath + " hook " + he.Sub + " --assistant " + Assistant,
				}},
			}
			groups, _ := hooks[he.Event].([]any)
			hooks[he.Event] = append(groups, entry)
		}

		servers, ok := root["mcpServers"].(map[string]any)
		if !ok {
			servers = map[string]any{}
			root["mcpServers"] = servers
		}
		servers[MCPServerName] = map[string]any{
			"command": binPath,
			"args":    []any{"mcp"},
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if backup != "" {
		backups = append(backups, backup)
	}
	return backups, nil
}

// Uninstall removes exactly the entries Install added — hook entries and the
// agent-brain-memory MCP server registration — leaving every foreign key,
// event, matcher-group, and server untouched. No backup is written; the
// pre-integration backup from Install is the restore path.
func Uninstall() error {
	path, err := SettingsPath()
	if err != nil {
		return err
	}
	return settingsfile.WriteIfChanged(path, func(root map[string]any) (bool, error) {
		changed := false
		if hooks, ok := root["hooks"].(map[string]any); ok {
			if removeOurEntries(hooks) {
				changed = true
			}
			if len(hooks) == 0 {
				delete(root, "hooks")
			}
		}
		if servers, ok := root["mcpServers"].(map[string]any); ok {
			if _, present := servers[MCPServerName]; present {
				changed = true
				delete(servers, MCPServerName)
				if len(servers) == 0 {
					delete(root, "mcpServers")
				}
			}
		}
		return changed, nil
	})
}

// InstalledEvents returns the Gemini CLI events that currently carry an
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
				if hook, ok := h.(map[string]any); ok && isOursHook(hook) {
					events = append(events, he.Event)
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

// MCPRegistered reports whether the memory server entry exists.
func MCPRegistered() (bool, error) {
	path, err := SettingsPath()
	if err != nil {
		return false, err
	}
	root, existed, err := settingsfile.Load(path)
	if err != nil || !existed {
		return false, err
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		return false, nil
	}
	_, ok = servers[MCPServerName].(map[string]any)
	return ok, nil
}
