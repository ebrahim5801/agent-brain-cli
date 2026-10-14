package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func TestSessionSummaryRedactsBeforeStorageAndTruncation(t *testing.T) {
	st := storetest.SQLite(t)
	dir := t.TempDir()
	p := attribution.Resolve(dir)
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: p.Kind, Identity: p.Identity}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	binding, err := st.EnsureBoundSession(pid, "codex", "summary", dir, "", "")
	sid := binding.SessionID
	if err != nil {
		t.Fatal(err)
	}
	secret := "ghp_" + strings.Repeat("x", 30)
	s := New(st, dir, "test")
	_, _, err = s.sessionSummary(context.Background(), nil, summaryIn{SessionHandle: binding.Handle, Summary: strings.Repeat("a", 490) + " " + secret})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := st.QueryRow("SELECT summary FROM sessions WHERE id = ?", sid).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "ghp_") || len([]rune(got)) > 500 {
		t.Fatal("summary persisted a secret fragment or exceeded its limit")
	}
}
