// Package cursor is the adapter for Cursor: hooks.json / mcp.json settings
// integration and hook payload parsing. Everything Cursor-specific stays
// behind this boundary (constitution VI); no message text or tool bodies ever
// cross it into storage (constitution II).
package cursor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/assistant/settingsfile"
)

// Assistant is the adapter name recorded on sessions.
const Assistant = "cursor"

// MCPServerName is the registration key for the memory server.
const MCPServerName = "agent-brain-memory"

type Adapter struct{}

func init() {
	assistant.Register(assistant.OrderCursor, Adapter{})
}

func (Adapter) Name() string        { return Assistant }
func (Adapter) DisplayName() string { return "Cursor" }
func (Adapter) Detected() bool      { return Detected() }

func (Adapter) Install(binPath string) (backups []string, err error) {
	return Install(binPath)
}

func (Adapter) Uninstall() error {
	return Uninstall()
}

func (Adapter) State() (assistant.State, error) {
	s := assistant.State{Detected: Detected(), HookEventsWanted: ExpectedEventCount()}
	events, err := InstalledEvents()
	if err != nil {
		return s, err
	}
	s.HookEvents = len(events)
	registered, err := MCPRegistered()
	if err != nil {
		return s, err
	}
	s.MCPRegistered = registered
	s.Tier = assistant.DeriveTier(s.HookEvents, s.HookEventsWanted, s.MCPRegistered)
	if s.Integrated() {
		s.Notes = append(s.Notes, "token usage not reported by this assistant")
	}
	return s, nil
}

var hookEvents = []struct {
	Event string // Cursor hook event name
	Sub   string // agent-brain hook subcommand
}{
	{"sessionStart", "session-start"},
	{"beforeSubmitPrompt", "prompt"},
	{"postToolUse", "tool-use"},
	{"stop", "stop"},
	{"sessionEnd", "session-end"},
}

func configDir() (string, error) {
	if v := os.Getenv("AGENT_BRAIN_CURSOR_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cursor"), nil
}

func hooksPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hooks.json"), nil
}

func mcpPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mcp.json"), nil
}

// Detected reports whether Cursor appears present on this machine.
func Detected() bool {
	if v := os.Getenv("AGENT_BRAIN_CURSOR_HOME"); v != "" {
		fi, err := os.Stat(v)
		return err == nil && fi.IsDir()
	}
	if dir, err := configDir(); err == nil {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return true
		}
	}
	if _, err := exec.LookPath("cursor"); err == nil {
		return true
	}
	_, err := exec.LookPath("cursor-agent")
	return err == nil
}

func isOurs(command string) bool {
	return strings.Contains(command, "agent-brain") && strings.Contains(command, " hook ")
}

// removeOurHookEntries strips agent-brain hook commands from every event's
// flat entry array, dropping events left empty. User entries are untouched.
// removeOurHookEntries strips agent-brain hook entries and reports whether
// anything was removed.
func removeOurHookEntries(hooks map[string]any) bool {
	changed := false
	for event, v := range hooks {
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, e := range arr {
			if entry, ok := e.(map[string]any); ok {
				if cmd, _ := entry["command"].(string); isOurs(cmd) {
					changed = true
					continue
				}
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	return changed
}

// Install merges agent-brain's hook entries into hooks.json and its MCP entry
// into mcp.json, additively: existing user configuration is preserved, a
// timestamped backup is written first when a file changes, and re-running
// updates our entries in place (a no-op re-install writes nothing).
func Install(binPath string) (backups []string, err error) {
	hp, err := hooksPath()
	if err != nil {
		return nil, err
	}
	hooksBackup, err := settingsfile.BackupIfChanged(hp, func(root map[string]any) (bool, error) {
		// A fresh (or empty) file gets the schema version; an existing file's
		// own version value — or absence — is preserved.
		if len(root) == 0 {
			root["version"] = 1
		}
		hooks, ok := root["hooks"].(map[string]any)
		if !ok {
			hooks = map[string]any{}
			root["hooks"] = hooks
		}
		removeOurHookEntries(hooks)
		for _, he := range hookEvents {
			entry := map[string]any{
				"command": binPath + " hook " + he.Sub + " --assistant cursor",
				"timeout": 10,
			}
			arr, _ := hooks[he.Event].([]any)
			hooks[he.Event] = append(arr, entry)
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if hooksBackup != "" {
		backups = append(backups, hooksBackup)
	}

	mp, err := mcpPath()
	if err != nil {
		return backups, err
	}
	mcpBackup, err := settingsfile.BackupIfChanged(mp, func(root map[string]any) (bool, error) {
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
		return backups, err
	}
	if mcpBackup != "" {
		backups = append(backups, mcpBackup)
	}
	return backups, nil
}

// Uninstall removes exactly the entries Install added. No backup is written;
// the pre-integration backup from Install is the restore path.
func Uninstall() error {
	hp, err := hooksPath()
	if err != nil {
		return err
	}
	if err := settingsfile.WriteIfChanged(hp, func(root map[string]any) (bool, error) {
		hooks, ok := root["hooks"].(map[string]any)
		if !ok {
			return false, nil
		}
		removed := removeOurHookEntries(hooks)
		if len(hooks) == 0 {
			delete(root, "hooks")
		}
		return removed, nil
	}); err != nil {
		return err
	}

	mp, err := mcpPath()
	if err != nil {
		return err
	}
	return settingsfile.WriteIfChanged(mp, func(root map[string]any) (bool, error) {
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

// InstalledEvents returns the Cursor events that currently carry an
// agent-brain hook entry in hooks.json.
func InstalledEvents() ([]string, error) {
	hp, err := hooksPath()
	if err != nil {
		return nil, err
	}
	root, existed, err := settingsfile.Load(hp)
	if err != nil || !existed {
		return nil, err
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return nil, nil
	}
	var events []string
	for _, he := range hookEvents {
		arr, ok := hooks[he.Event].([]any)
		if !ok {
			continue
		}
		for _, e := range arr {
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := entry["command"].(string); isOurs(cmd) {
				events = append(events, he.Event)
				break
			}
		}
	}
	return events, nil
}

// ExpectedEventCount is how many hook events a healthy install registers.
func ExpectedEventCount() int {
	return len(hookEvents)
}

// MCPRegistered reports whether the memory server entry exists in mcp.json.
func MCPRegistered() (bool, error) {
	mp, err := mcpPath()
	if err != nil {
		return false, err
	}
	root, existed, err := settingsfile.Load(mp)
	if err != nil || !existed {
		return false, err
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		return false, nil
	}
	_, ok = servers[MCPServerName]
	return ok, nil
}
