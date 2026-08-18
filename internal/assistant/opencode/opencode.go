// Package opencode is the adapter for OpenCode: an owned JS plugin shim
// (OpenCode's extension point is a plugin module, not a shell-command hook
// list), MCP registration, and hook payload parsing. Everything
// OpenCode-specific stays behind this boundary (constitution VI); no prompt
// text, tool bodies, or model message content ever cross it into storage
// (constitution II).
//
// Confirmed live against a real OpenCode install (see
// plans/spikes/opencode.md): the plugin shim's "event" hook receives every
// SDK lifecycle event and shells out to `agent-brain hook <event> --assistant
// opencode`, piping a small whitelisted JSON payload to stdin — the same
// stdin-JSON hook protocol every other adapter uses. The JS-to-neutral-event
// translation lives entirely in the shim; ParseHook only ever decodes that
// JSON.
package opencode

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/assistant/settingsfile"
)

// Assistant is the adapter name recorded on sessions.
const Assistant = "opencode"

// MCPServerName is the registration key for the memory server.
const MCPServerName = "agent-brain-memory"

const (
	configFileName = "opencode.jsonc"
	pluginFileName = "agent-brain-plugin.js"
	// pluginMarker is a distinctive string only agent-brain's generated shim
	// contains, used to recognize the owned file across reinstalls.
	pluginMarker = "AgentBrainPlugin"
	// PluginRelPath is how the shim is referenced from opencode.jsonc's
	// "plugin" array — relative so it travels with the config directory.
	PluginRelPath = "./" + pluginFileName
)

// hookEvents lists the neutral events the shim can honestly produce. Unlike
// Gemini/Copilot's per-event hook entries, OpenCode's single plugin file
// either carries all of these (installed) or none (absent) — there is no
// partial-by-event state on disk.
var hookEvents = []string{assistant.EventSessionStart, assistant.EventModelUsage, assistant.EventStop}

func configDir() (string, error) {
	if v := os.Getenv("AGENT_BRAIN_OPENCODE_HOME"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode"), nil
}

// ConfigPath is OpenCode's global settings file, carrying both the plugin
// registration and mcp server entries.
func ConfigPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

func pluginPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, pluginFileName), nil
}

// Detected reports whether OpenCode appears present on this machine. When
// AGENT_BRAIN_OPENCODE_HOME is set (test seam), only that directory's
// presence is checked — PATH lookup is skipped so tests stay hermetic.
func Detected() bool {
	if v := os.Getenv("AGENT_BRAIN_OPENCODE_HOME"); v != "" {
		fi, err := os.Stat(v)
		return err == nil && fi.IsDir()
	}
	if dir, err := configDir(); err == nil {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return true
		}
	}
	_, err := exec.LookPath("opencode")
	return err == nil
}

// buildPluginJS renders the owned shim. It is entirely ours — never merged
// with foreign content — so Install rewrites it directly when it drifts from
// this template, with no backup (the same treatment Copilot CLI's owned
// hooks file gets).
func buildPluginJS(binPath string) string {
	return fmt.Sprintf(`// Managed by agent-brain. Do not edit — reinstalling overwrites this file.
// Translates OpenCode's plugin event stream into agent-brain's neutral hook
// protocol: shells out to "agent-brain hook <event> --assistant opencode"
// with a small JSON payload on stdin, exactly like every other adapter's
// hook command. A capture failure here must never disturb the session.
// Piped through a shell "echo | bin hook ..." rather than writing the
// BunShellPromise's stdin WritableStream directly, since the latter's
// close()/EOF handling was unreliable in testing; the shell-pipe form uses
// BunShell's own escaping for the JSON argument and is the documented,
// straightforward path.
const AGENT_BRAIN_BIN = %q;

async function fire(input, event, payload) {
  try {
    const json = JSON.stringify(payload);
    await input.$`+"`echo ${json} | ${AGENT_BRAIN_BIN} hook ${event} --assistant opencode`"+`.nothrow().quiet();
  } catch {
    // Swallow: a collector failure must never surface to the assistant.
  }
}

export const AgentBrainPlugin = async (input) => {
  const cwd = input.directory;
  // OpenCode emits several finished "message.updated" events for the same
  // assistant message; each carries the message's final cumulative tokens.
  // AddUsage on the collector side is a pure accumulator, so reporting more
  // than once per message id would multiply the true usage. Report each
  // message's usage exactly once.
  const reportedUsage = new Set();
  return {
    event: async ({ event }) => {
      try {
        switch (event.type) {
          case "session.created":
            await fire(input, "session-start", { session_id: event?.properties?.sessionID, cwd });
            break;
          case "session.idle":
            await fire(input, "stop", { session_id: event?.properties?.sessionID, cwd });
            break;
          case "message.updated": {
            const info = event?.properties?.info;
            if (info && info.role === "assistant" && info.finish && info.tokens && !reportedUsage.has(info.id)) {
              reportedUsage.add(info.id);
              await fire(input, "model-usage", {
                session_id: info.sessionID,
                cwd,
                model: info.modelID,
                usage: {
                  input: info.tokens.input || 0,
                  output: info.tokens.output || 0,
                  reasoning: info.tokens.reasoning || 0,
                  cache_read: (info.tokens.cache && info.tokens.cache.read) || 0,
                  cache_write: (info.tokens.cache && info.tokens.cache.write) || 0,
                },
              });
            }
            break;
          }
        }
      } catch {
        // Swallow: a collector failure must never surface to the assistant.
      }
    },
  };
};
`, binPath)
}

func removeOurPluginRef(plugins []any) []any {
	var kept []any
	for _, p := range plugins {
		if s, ok := p.(string); ok && s == PluginRelPath {
			continue
		}
		kept = append(kept, p)
	}
	return kept
}

// Install merges the plugin registration + our MCP server entry into
// opencode.jsonc (backing that file up first when it changes), then writes the
// owned plugin shim. Config editing happens first so that installing over an
// unparseable opencode.jsonc aborts before any shim file is written, leaving
// the config directory untouched rather than littered with an orphan shim. The
// shim file is entirely ours: no merge, no backup, rewritten only when its
// content actually differs (a no-op re-install writes nothing).
func Install(binPath string) (backups []string, err error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	cfgPath, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	backup, err := settingsfile.BackupIfChanged(cfgPath, func(root map[string]any) (bool, error) {
		plugins, _ := root["plugin"].([]any)
		plugins = removeOurPluginRef(plugins)
		plugins = append(plugins, PluginRelPath)
		root["plugin"] = plugins

		mcp, ok := root["mcp"].(map[string]any)
		if !ok {
			mcp = map[string]any{}
			root["mcp"] = mcp
		}
		mcp[MCPServerName] = map[string]any{
			"type":    "local",
			"command": []any{binPath, "mcp"},
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if backup != "" {
		backups = append(backups, backup)
	}

	pPath, err := pluginPath()
	if err != nil {
		return nil, err
	}
	want := []byte(buildPluginJS(binPath))
	if existing, readErr := os.ReadFile(pPath); readErr != nil || !bytes.Equal(existing, want) {
		tmp := pPath + ".agent-brain-tmp"
		if err := os.WriteFile(tmp, want, 0o644); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, pPath); err != nil {
			return nil, err
		}
	}
	return backups, nil
}

// Uninstall removes the owned shim outright and strips exactly our plugin
// reference and MCP server entry from opencode.jsonc, leaving every foreign
// plugin, mcp server, and setting untouched. A never-integrated adapter is a
// success no-op.
func Uninstall() error {
	pPath, err := pluginPath()
	if err != nil {
		return err
	}
	if err := os.Remove(pPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	cfgPath, err := ConfigPath()
	if err != nil {
		return err
	}
	return settingsfile.WriteIfChanged(cfgPath, func(root map[string]any) (bool, error) {
		changed := false
		if plugins, ok := root["plugin"].([]any); ok {
			filtered := removeOurPluginRef(plugins)
			if len(filtered) != len(plugins) {
				changed = true
			}
			if len(filtered) == 0 {
				delete(root, "plugin")
			} else {
				root["plugin"] = filtered
			}
		}
		if mcp, ok := root["mcp"].(map[string]any); ok {
			if _, present := mcp[MCPServerName]; present {
				changed = true
				delete(mcp, MCPServerName)
				if len(mcp) == 0 {
					delete(root, "mcp")
				}
			}
		}
		return changed, nil
	})
}

// pluginInstalled reports whether the owned shim file is present and looks
// like ours (byte-exact match isn't required here — only InstalledEventCount
// needs to know "is this our shim", not "is it up to date"; a stale-but-ours
// shim still fully counts since Install rewrites it unconditionally).
func pluginInstalled() bool {
	pPath, err := pluginPath()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(pPath)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte(pluginMarker))
}

func configReferencesPlugin() (bool, error) {
	cfgPath, err := ConfigPath()
	if err != nil {
		return false, err
	}
	root, existed, err := settingsfile.Load(cfgPath)
	if err != nil || !existed {
		return false, err
	}
	plugins, _ := root["plugin"].([]any)
	for _, p := range plugins {
		if s, ok := p.(string); ok && s == PluginRelPath {
			return true, nil
		}
	}
	return false, nil
}

// InstalledEventCount reports how many of the neutral events the current
// on-disk state honestly carries: all of them, if the shim is present and
// referenced from opencode.jsonc, else none — the shim is all-or-nothing.
func InstalledEventCount() (int, error) {
	ref, err := configReferencesPlugin()
	if err != nil {
		return 0, err
	}
	if !ref || !pluginInstalled() {
		return 0, nil
	}
	return len(hookEvents), nil
}

// ExpectedEventCount is how many hook events a healthy install registers.
func ExpectedEventCount() int {
	return len(hookEvents)
}

// MCPRegistered reports whether the memory server entry exists.
func MCPRegistered() (bool, error) {
	cfgPath, err := ConfigPath()
	if err != nil {
		return false, err
	}
	root, existed, err := settingsfile.Load(cfgPath)
	if err != nil || !existed {
		return false, nil
	}
	mcp, ok := root["mcp"].(map[string]any)
	if !ok {
		return false, nil
	}
	_, ok = mcp[MCPServerName].(map[string]any)
	return ok, nil
}

// State reads current on-disk integration truth.
func State() (assistant.State, error) {
	s := assistant.State{Detected: Detected(), HookEventsWanted: ExpectedEventCount()}
	n, err := InstalledEventCount()
	if err != nil {
		return s, err
	}
	s.HookEvents = n
	mcp, err := MCPRegistered()
	if err != nil {
		return s, err
	}
	s.MCPRegistered = mcp
	s.Tier = assistant.DeriveTier(s.HookEvents, s.HookEventsWanted, s.MCPRegistered)
	if s.Integrated() {
		s.Notes = append(s.Notes, "no session-end, prompt, or tool-use capture; no injection or distillation yet (see plans/spikes/opencode.md)")
	}
	return s, nil
}
