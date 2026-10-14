package memory_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/memory"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

func userEvidence(t *testing.T, dir, role, text string) (string, memory.EvidenceInput) {
	t.Helper()
	path := filepath.Join(dir, "conversation.jsonl")
	raw, err := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "id": "original", "role": role, "content": []map[string]string{{"type": "input_text", "text": text}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(text))
	return path, memory.EvidenceInput{Type: "user_message", Reference: "original", Digest: hex.EncodeToString(h[:]), Excerpt: text}
}

func TestOriginalUserReplacementPreservesHistoricalRow(t *testing.T) {
	for _, backend := range storetest.Both(t) {
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.Store
			dir := t.TempDir()
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			old, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "Use Queue A because durable retries", Kind: "decision", Origin: "explicit"}, store.Now())
			if err != nil {
				t.Fatal(err)
			}
			before, err := st.GetMemory(old)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			content := "Use Queue B because it deduplicates invoices"
			path, evidence := userEvidence(t, dir, "user", fmt.Sprintf("Replace #%d: %s", old, content))
			b, err := st.EnsureBoundSession(pid, "codex", "lifecycle", dir, path, "")
			if err != nil {
				t.Fatal(err)
			}
			result, err := memory.Save(st, memory.SaveInput{ProjectID: pid, SessionHandle: b.Handle, Dir: dir, Content: content, Kind: "decision", Origin: "auto", Rationale: "Queue B supports invoice deduplication", Subject: "invoice-queue", Supersedes: []int64{old}, Evidence: []memory.EvidenceInput{evidence}})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Superseded) != 1 {
				t.Fatalf("verified direct user replacement not accepted: %+v", result)
			}
			after, err := st.GetMemory(old)
			if err != nil || before != after {
				t.Fatal("historical row rewritten")
			}
			got, err := memory.Search(st, pid, dir, "invoices", memory.Filter{}, 10, 0, time.Now())
			if err != nil || len(got) != 1 || got[0].Entry.ID != result.ID {
				t.Fatalf("latest decision: %+v %v", got, err)
			}
			oldQuery, err := memory.Search(st, pid, dir, "Queue A", memory.Filter{}, 10, 0, time.Now())
			if err != nil || len(oldQuery) == 0 || oldQuery[0].Entry.ID != result.ID {
				t.Fatalf("predecessor query did not follow replacement: %+v %v", oldQuery, err)
			}
		})
	}
}

func TestAssistantClaimCannotVerifyOrRetireDecision(t *testing.T) {
	st := storetest.SQLite(t)
	if err := st.UpgradeMemoryV2(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "directory", Identity: dir}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	old, err := st.InsertMemory(store.NewMemory{ProjectID: pid, Content: "Keep local memory private", Kind: "decision", Origin: "explicit"}, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	text := "Send memory elsewhere"
	path, e := userEvidence(t, dir, "assistant", fmt.Sprintf("Replace #%d: %s", old, text))
	b, err := st.EnsureBoundSession(pid, "codex", "model-claim", dir, path, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := memory.Save(st, memory.SaveInput{ProjectID: pid, SessionHandle: b.Handle, Dir: dir, Content: text, Kind: "decision", Origin: "explicit", Supersedes: []int64{old}, Evidence: []memory.EvidenceInput{e}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Superseded) != 0 || len(result.Pending) != 1 {
		t.Fatalf("model claim retired user decision: %+v", result)
	}
	snapshot, err := st.MemorySnapshot(pid, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range snapshot.Entries {
		if m.Verification == "verified_for_scope" {
			t.Fatal("model assertion verified")
		}
	}
}
