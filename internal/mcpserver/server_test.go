package mcpserver

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/entitlement"
	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// The MCP server takes only a store and a cwd — no assistant identity appears
// anywhere in its surface (New/save/search/list). Memory saved through it is
// keyed purely on the project resolved from cwd and served back identically
// regardless of which assistant's client made the call (FR-016/FR-017,
// Constitution II/V). This is what lets an MCP-only-tier assistant get full
// memory serving with zero telemetry coupling.
func TestMCPSaveIsAssistantBlindAndServesBack(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", cfgDir)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := config.Update(func(c *config.Config) error {
		c.Entitlement = &config.Entitlement{Tier: entitlement.TierPro, VerifiedAt: now}
		c.Consent.Memory = &config.Consent{GrantedAt: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	s := New(st, dir, "test")
	if _, _, err := s.save(context.Background(), &mcp.CallToolRequest{}, saveIn{
		Content: "prefer pgx over lib/pq", Kind: "decision", Origin: "explicit",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	proj := attribution.Resolve(dir)
	pid, err := st.UpsertProject(store.ProjectIdentity{
		Kind: proj.Kind, Identity: proj.Identity, DisplayName: proj.DisplayName,
	}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	pack, err := memory.BuildPack(st, pid, dir, 2000, gitBudget, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pack.Text, "prefer pgx over lib/pq") {
		t.Errorf("saved memory not served back in pack:\n%s", pack.Text)
	}
}

// A save made while the project has tracked sessions is attributed to the
// newest one (the session the distillation prompt runs in); with no session
// yet, session_id stays NULL per the data-model contract.
func TestMCPSaveAttributesLatestSession(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", cfgDir)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := config.Update(func(c *config.Config) error {
		c.Entitlement = &config.Entitlement{Tier: entitlement.TierPro, VerifiedAt: now}
		c.Consent.Memory = &config.Consent{GrantedAt: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	st, err := store.OpenAt(filepath.Join(t.TempDir(), "agent-brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(st, dir, "test")

	save := func(content string) store.Memory {
		t.Helper()
		res, _, err := s.save(context.Background(), &mcp.CallToolRequest{}, saveIn{
			Content: content, Kind: "fact", Origin: "auto",
		})
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		var id int64
		text := res.Content[0].(*mcp.TextContent).Text
		if _, err := fmt.Sscanf(text, "saved memory %d", &id); err != nil {
			t.Fatalf("result %q: %v", text, err)
		}
		m, err := st.GetMemory(id)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	if m := save("saved before any session"); m.SessionID.Valid {
		t.Errorf("session with no sessions = %v, want NULL", m.SessionID)
	}

	proj := attribution.Resolve(dir)
	pid, err := st.UpsertProject(store.ProjectIdentity{
		Kind: proj.Kind, Identity: proj.Identity, DisplayName: proj.DisplayName,
	}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureSession("sess-old", pid, store.Now(), "", "claude-code"); err != nil {
		t.Fatal(err)
	}
	newest, err := st.EnsureSession("sess-new", pid, "2999-01-01T00:00:00Z", "", "claude-code")
	if err != nil {
		t.Fatal(err)
	}

	if m := save("saved mid-session"); !m.SessionID.Valid || m.SessionID.Int64 != newest {
		t.Errorf("session = %v, want %d", m.SessionID, newest)
	}
}
