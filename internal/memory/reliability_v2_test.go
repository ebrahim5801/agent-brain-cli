package memory_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func TestCompletedTaskKeepsOriginalAndCanReopen(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.Store
			dir := t.TempDir()
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			id, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "Retry worker test is unfinished", Kind: "task_state", Origin: "auto"}, store.Now())
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
			content := "Retry worker acceptance is complete"
			path, e := userEvidence(t, dir, "user", fmt.Sprintf("Completed #%d: %s", id, content))
			b, err := st.EnsureBoundSession(pid, "codex", "task", dir, path, "")
			if err != nil {
				t.Fatal(err)
			}
			result, err := memory.Save(st, memory.SaveInput{ProjectID: pid, SessionHandle: b.Handle, Dir: dir, Content: content, Kind: "task_state", Origin: "auto", Completes: []int64{id}, Evidence: []memory.EvidenceInput{e}})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Completed) != 1 {
				t.Fatalf("completion not acknowledged: %+v", result)
			}
			after, err := st.GetMemory(id)
			if err != nil || before != after {
				t.Fatal("original task was rewritten")
			}
			snapshot, err := st.MemorySnapshot(pid, nil)
			if err != nil {
				t.Fatal(err)
			}
			var revision int64
			for _, m := range snapshot.Entries {
				if m.ID == id {
					if m.Status != "completed" {
						t.Fatalf("effective task not completed: %+v", m)
					}
					revision = m.Revision
				}
			}
			if err := st.ReopenMemory(pid, id, revision); err != nil {
				t.Fatal(err)
			}
			active, err := memory.List(st, pid, dir, false, 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, m := range active {
				found = found || m.Entry.ID == id
			}
			if !found {
				t.Fatal("reopened task not readable")
			}
		})
	}
}

func TestQuotedChangedAndMissingUserEvidenceRemainUnverified(t *testing.T) {
	for _, variant := range []string{"quoted", "changed-digest", "missing-message", "unsupported-host"} {
		t.Run(variant, func(t *testing.T) {
			st := storetest.SQLite(t)
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			content := "Use local receipts"
			text := "Decision: " + content
			if variant == "quoted" {
				text = "Example only: Decision: " + content
			}
			path, e := userEvidence(t, dir, "user", text)
			if variant == "changed-digest" {
				e.Digest = strings.Repeat("0", 64)
			}
			if variant == "missing-message" {
				e.Reference = "absent"
			}
			host := "codex"
			if variant == "unsupported-host" {
				host = "cursor"
			}
			b, err := st.EnsureBoundSession(pid, host, "source", dir, path, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = memory.Save(st, memory.SaveInput{ProjectID: pid, SessionHandle: b.Handle, Dir: dir, Content: content, Kind: "decision", Origin: "explicit", Evidence: []memory.EvidenceInput{e}})
			if err != nil {
				t.Fatal(err)
			}
			found, err := memory.Search(st, pid, dir, "receipts", memory.Filter{}, 10, 0, time.Now())
			if err != nil || len(found) != 1 {
				t.Fatalf("lost unverified memory: %v", err)
			}
			if found[0].Entry.Verification == "verified_for_scope" {
				t.Fatal("unverifiable or quoted source became verified")
			}
		})
	}
}

func TestNewMetadataIsRedactedAndLegacyMetadataIsNotBackfilled(t *testing.T) {
	st := storetest.SQLite(t)
	dir := t.TempDir()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	old, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "historical fact", Kind: "fact", Origin: "auto"}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	secret := "ghp_" + strings.Repeat("s", 30)
	_, err = memory.Save(st, memory.SaveInput{ProjectID: pid, Dir: dir, Content: "new durable fact", Kind: "fact", Origin: "auto", Rationale: "Credential was " + secret, Subject: "privacy", Evidence: []memory.EvidenceInput{{Type: "assertion", Reference: "note", Excerpt: secret}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.MemorySnapshot(pid, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range snapshot.Entries {
		if strings.Contains(m.Details.Rationale, secret) {
			t.Fatal("rationale leaked secret")
		}
		if m.ID == old && m.Details.Completeness != "" {
			t.Fatal("historical metadata backfilled")
		}
	}
}
