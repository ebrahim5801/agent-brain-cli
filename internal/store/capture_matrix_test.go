package store_test

import (
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

// TestMemoryCaptureGatesBothBackends runs the capture claims against every
// available backend: their SQL counts real tool calls with substr and a bound
// prefix length, which must behave the same on SQLite and Postgres.
func TestMemoryCaptureGatesBothBackends(t *testing.T) {
	for _, b := range storetest.Both(t) {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			pid, err := st.UpsertProject(store.ProjectIdentity{Kind: "repo_root", Identity: "/capture", DisplayName: "capture"}, store.Now())
			if err != nil {
				t.Fatalf("upsert project: %v", err)
			}
			sid, err := st.EnsureSession("ext-capture", pid, store.Now(), "", "claude-code")
			if err != nil {
				t.Fatalf("ensure session: %v", err)
			}
			add := func(kind, detail, at string) {
				t.Helper()
				if err := st.AppendEvent(sid, kind, detail, at); err != nil {
					t.Fatalf("append %s: %v", kind, err)
				}
			}

			add("prompt", "", "2026-01-01T00:00:00.000Z")
			add("tool_use", "Read", "2026-01-01T00:00:01.000Z")
			// Matches "mcp__agent-brain-memory__%" only if "_" were a LIKE
			// wildcard, so it must count as a real tool call.
			add("tool_use", "mcpXXagent-brain-memoryXXlookalike", "2026-01-01T00:00:02.000Z")
			for _, at := range []string{"2026-01-01T00:00:03.000Z", "2026-01-01T00:00:04.000Z", "2026-01-01T00:00:05.000Z"} {
				add("tool_use", store.MemoryToolPrefix+"memory_save", at)
			}

			// Two real calls (the memory saves do not count): below a threshold of 3.
			if won, err := st.MarkSessionDistilled(sid, "2026-01-01T00:00:06.000Z", 3); err != nil || won {
				t.Fatalf("distilled with 2 real calls: won=%v err=%v", won, err)
			}
			if won, err := st.MarkSessionCheckpointed(sid, "2026-01-01T00:00:06.000Z", 3); err != nil || won {
				t.Fatalf("checkpointed with 2 real calls: won=%v err=%v", won, err)
			}

			add("tool_use", "Edit", "2026-01-01T00:00:07.000Z")
			if won, err := st.MarkSessionDistilled(sid, "2026-01-01T00:00:08.000Z", 3); err != nil || !won {
				t.Fatalf("not distilled at 3 real calls: won=%v err=%v", won, err)
			}
			// No prompt since the claim: the post-distillation Stop passes through.
			if won, err := st.MarkSessionDistilled(sid, "2026-01-01T00:00:09.000Z", 3); err != nil || won {
				t.Fatalf("distilled twice without a new prompt: won=%v err=%v", won, err)
			}
			if won, err := st.MarkSessionCheckpointed(sid, "2026-01-01T00:00:09.000Z", 3); err != nil || !won {
				t.Fatalf("not checkpointed at 3 real calls: won=%v err=%v", won, err)
			}
			if won, err := st.MarkSessionCheckpointed(sid, "2026-01-01T00:00:10.000Z", 3); err != nil || won {
				t.Fatalf("checkpointed twice with no new calls: won=%v err=%v", won, err)
			}
		})
	}
}
