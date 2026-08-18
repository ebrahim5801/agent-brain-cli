package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRegisterMCPIsIdempotentAndPreservesUserEntries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, ".claude.json")
	seed := `{"mcpServers":{"user-server":{"type":"stdio","command":"other"}},"theme":"dark"}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RegisterMCP("/usr/local/bin/agent-brain"); err != nil {
		t.Fatal(err)
	}
	if err := RegisterMCP("/new/path/agent-brain"); err != nil {
		t.Fatal(err)
	}

	root := readJSON(t, path)
	if root["theme"] != "dark" {
		t.Error("unrelated key lost")
	}
	servers := root["mcpServers"].(map[string]any)
	if _, ok := servers["user-server"]; !ok {
		t.Error("user MCP server lost")
	}
	ours := servers[MCPServerName].(map[string]any)
	if ours["command"] != "/new/path/agent-brain" {
		t.Errorf("re-register did not update command: %v", ours["command"])
	}
	args := ours["args"].([]any)
	if len(args) != 1 || args[0] != "mcp" {
		t.Errorf("args = %v", args)
	}

	ok, cmd, err := MCPRegistered()
	if err != nil || !ok || cmd != "/new/path/agent-brain" {
		t.Errorf("MCPRegistered = %v %q %v", ok, cmd, err)
	}
}

func TestUnregisterMCPRemovesOnlyOurEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, ".claude.json")
	if err := RegisterMCP("/usr/local/bin/agent-brain"); err != nil {
		t.Fatal(err)
	}
	root := readJSON(t, path)
	servers := root["mcpServers"].(map[string]any)
	servers["user-server"] = map[string]any{"type": "stdio", "command": "other"}
	data, _ := json.Marshal(root)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UnregisterMCP(); err != nil {
		t.Fatal(err)
	}
	root = readJSON(t, path)
	servers = root["mcpServers"].(map[string]any)
	if _, ok := servers[MCPServerName]; ok {
		t.Error("our entry survived unregister")
	}
	if _, ok := servers["user-server"]; !ok {
		t.Error("user entry removed")
	}

	// Removing the last entry drops the mcpServers key entirely.
	delete(servers, "user-server")
	data, _ = json.Marshal(root)
	_ = os.WriteFile(path, data, 0o644)
	_ = RegisterMCP("/x")
	if err := UnregisterMCP(); err != nil {
		t.Fatal(err)
	}
	if _, ok := readJSON(t, path)["mcpServers"]; ok {
		t.Error("empty mcpServers key left behind")
	}

	// Unregister with no file at all is a no-op.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if err := UnregisterMCP(); err != nil {
		t.Errorf("unregister without file: %v", err)
	}
}
