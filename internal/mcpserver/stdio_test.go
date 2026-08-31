package mcpserver_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stdioSession builds the real binary and connects to `agent-brain mcp` over
// pipes. Every state directory is pinned to a throwaway root — including
// AGENT_BRAIN_DATA_DIR, which takes precedence over XDG in store path
// resolution — so the test can never touch the developer's real store.
func stdioSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary; skipped under -short")
	}

	bin := filepath.Join(t.TempDir(), "agent-brain")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/ebrahim5801/agent-brain-cli/cmd/agent-brain").CombinedOutput(); err != nil {
		t.Fatalf("build agent-brain: %v\n%s", err, out)
	}

	root := t.TempDir()
	cmd := exec.Command(bin, "mcp")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"HOME="+root,
		"AGENT_BRAIN_DATA_DIR="+filepath.Join(root, "data"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
	)
	cmd.Stderr = os.Stderr

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-smoke-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("mcp handshake over stdio failed: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// The MCP server is the surface the assistants actually talk to: if the stdio
// handshake or the tool schemas break, the assistant silently loses memory and
// the user blames the assistant. The unit tests exercise handlers in-process,
// which cannot catch a transport- or schema-level regression, so this one drives
// the real binary over real pipes.
func TestStdioHandshakeExposesTools(t *testing.T) {
	session := stdioSession(t)
	ctx := context.Background()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	want := []string{"memory_list", "memory_save", "memory_search", "session_summary"}
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	for _, name := range want {
		tool, ok := got[name]
		if !ok {
			t.Errorf("tool %q missing from tools/list", name)
			continue
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description; assistants use it to decide when to call", name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", name)
		}
	}
	if len(res.Tools) != len(want) {
		t.Errorf("tools/list returned %d tools, want %d (%v)", len(res.Tools), len(want), want)
	}
}

// A tool call must survive the round trip, not just appear in the listing.
func TestStdioToolCallRoundTrips(t *testing.T) {
	session := stdioSession(t)
	ctx := context.Background()

	out, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "memory_search",
		Arguments: map[string]any{"query": "nothing should match this"},
	})
	if err != nil {
		t.Fatalf("memory_search over stdio: %v", err)
	}
	if len(out.Content) == 0 {
		t.Fatal("memory_search returned no content; assistants would see an empty result")
	}
}

// The schema description is the entire calibration mechanism for priority: the
// model has nothing else to go on when deciding whether an entry is critical.
// Unit tests exercise the handler struct directly and would not notice the
// field failing to reach the wire schema at all.
func TestStdioSchemaCarriesPriority(t *testing.T) {
	session := stdioSession(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "memory_save" && tool.Name != "memory_search" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("%s schema: %v", tool.Name, err)
		}
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s schema: %v", tool.Name, err)
		}
		prop, ok := schema.Properties["priority"]
		if !ok {
			t.Errorf("%s schema has no priority property", tool.Name)
			continue
		}
		if tool.Name == "memory_save" {
			for _, must := range []string{"critical", "normal", "background", "rare"} {
				if !strings.Contains(prop.Description, must) {
					t.Errorf("memory_save priority description missing %q: %q", must, prop.Description)
				}
			}
		}
	}
}
