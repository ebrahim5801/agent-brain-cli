package mcpserver

import (
	"context"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
	"testing"
)

func TestStatusIdentifiesCapabilityWithoutCreatingProjectOrUsage(t *testing.T) {
	st := storetest.SQLite(t)
	s := New(st, t.TempDir(), "test")
	_, out, err := s.status(context.Background(), nil, statusIn{})
	if err != nil {
		t.Fatal(err)
	}
	report := out.(map[string]any)
	if report["schema"] != 15 || report["v2_active"] != false || report["attribution"] != "unbound" {
		t.Fatalf("status: %+v", report)
	}
	var n int
	if err := st.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("read-only status created project: %d %v", n, err)
	}
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	_, second, err := s.status(context.Background(), nil, statusIn{})
	if err != nil {
		t.Fatal(err)
	}
	if second.(map[string]any)["store_fingerprint"] != report["store_fingerprint"] {
		t.Fatal("schema upgrade changed store identity")
	}
}
