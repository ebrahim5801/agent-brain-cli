package mcpserver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/entitlement"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP clients reject structured content that is not an object, so the list
// mode of memory_capture_status wraps its batches.
func TestCaptureStatusListIsAnObject(t *testing.T) {
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := config.Update(func(c *config.Config) error {
		c.Entitlement = &config.Entitlement{Tier: entitlement.TierPro, VerifiedAt: now}
		c.Consent.Memory = &config.Consent{GrantedAt: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	proj := attribution.Resolve(dir)
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: proj.Kind, Identity: proj.Identity, DisplayName: proj.DisplayName}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.EnsureBoundSession(pid, "claude-code", "status-session", dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, dir, "test")

	for _, want := range []int{0, 1} {
		if want == 1 {
			if _, err := st.RequestCapture(b, "stop", 1, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		res, out, err := s.captureStatus(context.Background(), &mcp.CallToolRequest{}, captureIn{SessionHandle: b.Handle})
		if err != nil {
			t.Fatal(err)
		}
		obj, ok := out.(map[string]any)
		if !ok {
			t.Fatalf("structured content is %T, want an object", out)
		}
		batches, ok := obj["batches"].([]store.CaptureBatch)
		if !ok || len(batches) != want {
			t.Fatalf("batches = %#v, want %d", obj["batches"], want)
		}
		if text := res.Content[0].(*mcp.TextContent).Text; !strings.HasPrefix(text, `{"batches":[`) {
			t.Fatalf("text result %q is not the batches object", text)
		}
	}
}

// A session that 0.5.5 split over several rows (one per folder) is reported
// as one session.
func TestUsageTotalsEveryRowOfANativeSession(t *testing.T) {
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	var want int64
	for i, folder := range []string{t.TempDir(), t.TempDir()} {
		b, err := st.EnsureBoundSession(pid, "claude-code", "split-session", folder, "", "")
		if err != nil {
			t.Fatal(err)
		}
		in := int64(10 * (i + 1))
		want += in
		if err := st.SetUsage(b.SessionID, "claude-sonnet", store.Usage{Input: in, Output: 1}); err != nil {
			t.Fatal(err)
		}
	}
	s := New(st, dir, "test")
	result, _, err := s.usage(context.Background(), nil, usageIn{SessionID: "split-session"})
	if err != nil {
		t.Fatal(err)
	}
	report := decodeUsageReport(t, result)
	if report.Session == nil || report.Session.InputTokens != want || report.Session.OutputTokens != 2 {
		t.Fatalf("split session = %+v, want %d in and 2 out", report.Session, want)
	}
}
