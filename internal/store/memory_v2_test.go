package store_test

import (
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func TestV2UpgradeIsExplicitAndPreservesLegacy(t *testing.T) {
	for _, b := range storetest.Both(t) {
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			v, err := st.SchemaVersion()
			if err != nil || v != 15 {
				t.Fatalf("default version %d: %v", v, err)
			}
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/legacy"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			id, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "legacy reasoning", Kind: "decision", Origin: "explicit"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			before, err := st.GetMemory(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			after, err := st.GetMemory(id)
			if err != nil || before != after {
				t.Fatalf("historical row changed: %v", err)
			}
			if !st.MemoryV2 {
				t.Fatal("V2 unavailable after upgrade")
			}
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeSessionsAreScopedAndSummariesCannotCrossSessions(t *testing.T) {
	for _, b := range storetest.Both(t) {
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/first"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			other, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: "/second"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			a, err := st.EnsureBoundSession(pid, "claude-code", "same", "/first", "", "")
			if err != nil {
				t.Fatal(err)
			}
			c, err := st.EnsureBoundSession(pid, "codex", "same", "/first", "", "")
			if err != nil {
				t.Fatal(err)
			}
			d, err := st.EnsureBoundSession(other, "codex", "same", "/second", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if a.SessionID == c.SessionID || c.SessionID == d.SessionID {
				t.Fatal("sessions collided")
			}
			again, err := st.EnsureBoundSession(pid, "claude-code", "same", "/first", "", "")
			if err != nil || again.Handle != a.Handle {
				t.Fatalf("repeat binding: %v", err)
			}
			if _, err := st.ResolveBinding(a.Handle, other, "/second"); err == nil {
				t.Fatal("foreign handle accepted")
			}
			if err := st.SetBoundSummary(a, "Claude outcome"); err != nil {
				t.Fatal(err)
			}
			if err := st.SetBoundSummary(c, "Codex outcome"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
