// Package copilot is the adapter for GitHub Copilot CLI: hook installation
// into a dedicated owned file, MCP registration, hook payload parsing, and
// best-effort usage extraction. Everything Copilot-specific stays behind this
// boundary (constitution VI); no prompt text or tool bodies ever cross it into
// storage (constitution II).
package copilot

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/assistant/settingsfile"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// Assistant is the adapter name recorded on sessions.
const Assistant = "copilot-cli"

func init() {
	assistant.Register(assistant.OrderCopilot, Adapter{})
}

// Adapter is the GitHub Copilot CLI implementation of assistant.Adapter.
type Adapter struct{}

func (Adapter) Name() string        { return Assistant }
func (Adapter) DisplayName() string { return "Copilot CLI" }
func (Adapter) Detected() bool      { return Detected() }

func (Adapter) Install(binPath string) ([]string, error) {
	return Install(binPath)
}

func (Adapter) Uninstall() error {
	return Uninstall()
}

// BackfillUsage best-effort recovers absolute token totals from the session's
// events.jsonl at session-end. The path comes from the payload's transcriptPath
// when present, else the conventional session-state location under the Copilot
// home. A missing file or unrecognized shape yields ok=false (honest absence).
func (Adapter) BackfillUsage(input assistant.HookInput) (store.Usage, string, []store.ModelUsage, bool) {
	path := input.TranscriptPath
	if path == "" && input.SessionKey != "" {
		home, err := configHome()
		if err != nil {
			return store.Usage{}, "", nil, false
		}
		path = filepath.Join(home, "session-state", input.SessionKey, "events.jsonl")
	}
	if path == "" {
		return store.Usage{}, "", nil, false
	}
	usage, model, ok := ParseEvents(path)
	if !ok {
		return store.Usage{}, "", nil, false
	}
	// Copilot's events.jsonl reports one cumulative model per session; the
	// breakdown is that single model so the session view is uniform across
	// assistants.
	var models []store.ModelUsage
	if model != "" {
		models = []store.ModelUsage{{Model: model, Usage: usage}}
	}
	return usage, model, models, true
}

func (Adapter) State() (assistant.State, error) {
	return State()
}

// configHome returns $COPILOT_HOME if set, else ~/.copilot. COPILOT_HOME is a
// real Copilot CLI environment variable; honoring it everywhere also gives
// tests a seam.
func configHome() (string, error) {
	if v := os.Getenv("COPILOT_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".copilot"), nil
}

// Detected reports whether Copilot CLI appears present on this machine.
func Detected() bool {
	if v := os.Getenv("COPILOT_HOME"); v != "" {
		fi, err := os.Stat(v)
		return err == nil && fi.IsDir()
	}
	home, err := configHome()
	if err == nil {
		if fi, err := os.Stat(home); err == nil && fi.IsDir() {
			return true
		}
	}
	_, err = exec.LookPath("copilot")
	return err == nil
}

func hooksPath() (string, error) {
	dir, err := configHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hooks", "agent-brain.json"), nil
}

func mcpConfigPath() (string, error) {
	dir, err := configHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mcp-config.json"), nil
}

// MCPServerName is the registration key for the memory server.
const MCPServerName = "agent-brain-memory"

type hookCommand struct {
	Type       string `json:"type"`
	Bash       string `json:"bash"`
	PowerShell string `json:"powershell"`
	TimeoutSec int    `json:"timeoutSec"`
}

type hooksFile struct {
	Version int                       `json:"version"`
	Hooks   map[string][]*hookCommand `json:"hooks"`
}

var hookEvents = []struct {
	Event string // Copilot CLI hook event name
	Sub   string // agent-brain hook subcommand
}{
	{"sessionStart", "session-start"},
	{"userPromptSubmitted", "prompt"},
	{"postToolUse", "tool-use"},
	{"agentStop", "stop"},
	{"sessionEnd", "session-end"},
}

func buildHooksFile(binPath string) hooksFile {
	hooks := make(map[string][]*hookCommand, len(hookEvents))
	for _, he := range hookEvents {
		bash := binPath + " hook " + he.Sub + " --assistant " + Assistant
		ps := "& '" + binPath + "' hook " + he.Sub + " --assistant " + Assistant
		hooks[he.Event] = []*hookCommand{{
			Type:       "command",
			Bash:       bash,
			PowerShell: ps,
			TimeoutSec: 10,
		}}
	}
	return hooksFile{Version: 1, Hooks: hooks}
}

func marshalHooksFile(hf hooksFile) ([]byte, error) {
	data, err := json.MarshalIndent(hf, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Install idempotently writes the owned hooks file and merges our MCP server
// entry into mcp-config.json, backing that one up first when it changes. The
// hooks file is entirely ours: no merge, no backup, rewritten only when its
// content actually differs.
func Install(binPath string) (backups []string, err error) {
	hPath, err := hooksPath()
	if err != nil {
		return nil, err
	}
	want, err := marshalHooksFile(buildHooksFile(binPath))
	if err != nil {
		return nil, err
	}
	existing, readErr := os.ReadFile(hPath)
	if readErr != nil || !bytes.Equal(existing, want) {
		if err := os.MkdirAll(filepath.Dir(hPath), 0o755); err != nil {
			return nil, err
		}
		tmp := hPath + ".agent-brain-tmp"
		if err := os.WriteFile(tmp, want, 0o644); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, hPath); err != nil {
			return nil, err
		}
	}

	mPath, err := mcpConfigPath()
	if err != nil {
		return nil, err
	}
	backup, err := settingsfile.BackupIfChanged(mPath, func(root map[string]any) (bool, error) {
		servers, ok := root["mcpServers"].(map[string]any)
		if !ok {
			servers = map[string]any{}
			root["mcpServers"] = servers
		}
		servers[MCPServerName] = map[string]any{
			"type":    "local",
			"command": binPath,
			"args":    []any{"mcp"},
		}
		return true, nil
	})
	if err != nil {
		return backups, err
	}
	if backup != "" {
		backups = append(backups, backup)
	}
	return backups, nil
}

// Uninstall removes the owned hooks file outright and strips our entry from
// mcp-config.json. A never-integrated adapter is a success no-op.
func Uninstall() error {
	hPath, err := hooksPath()
	if err != nil {
		return err
	}
	if err := os.Remove(hPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	mPath, err := mcpConfigPath()
	if err != nil {
		return err
	}
	return settingsfile.WriteIfChanged(mPath, func(root map[string]any) (bool, error) {
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

func isOurs(command string) bool {
	return strings.Contains(command, "agent-brain") && strings.Contains(command, " hook ")
}

// installedEvents reads the owned hooks file and returns which of our events
// carry an agent-brain command.
func installedEvents() ([]string, error) {
	hPath, err := hooksPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(hPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hf hooksFile
	if err := json.Unmarshal(data, &hf); err != nil {
		return nil, nil
	}
	var events []string
	for _, he := range hookEvents {
		cmds, ok := hf.Hooks[he.Event]
		if !ok {
			continue
		}
		for _, c := range cmds {
			if c != nil && isOurs(c.Bash) {
				events = append(events, he.Event)
				break
			}
		}
	}
	return events, nil
}

// mcpRegistered reports whether the memory server entry exists in
// mcp-config.json.
func mcpRegistered() (bool, error) {
	mPath, err := mcpConfigPath()
	if err != nil {
		return false, err
	}
	root, existed, err := settingsfile.Load(mPath)
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

// State reads current on-disk integration truth.
func State() (assistant.State, error) {
	s := assistant.State{Detected: Detected(), HookEventsWanted: len(hookEvents)}
	events, err := installedEvents()
	if err != nil {
		return s, err
	}
	s.HookEvents = len(events)
	mcp, err := mcpRegistered()
	if err != nil {
		return s, err
	}
	s.MCPRegistered = mcp
	s.Tier = assistant.DeriveTier(s.HookEvents, s.HookEventsWanted, s.MCPRegistered)
	if s.Integrated() {
		s.Notes = append(s.Notes, "token usage best-effort; may be absent")
	}
	return s, nil
}
