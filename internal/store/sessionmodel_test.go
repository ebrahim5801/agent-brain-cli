package store_test

import (
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/ebrahim5801/agent-brain-cli/internal/storetest"
)

// TestAddUsageAccumulatesPerModel mirrors the reported case: a session that
// starts on one model and switches to another mid-run ends with one per-model
// row each, summing to the session aggregate, and the scalar model is the last
// reporter (existing behavior).
func TestAddUsageAccumulatesPerModel(t *testing.T) {
	for _, b := range storetest.Both(t) {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			_, sessionID := seedUsageSession(t, st)

			if err := st.AddUsage(sessionID, "claude-opus-4-8", store.Usage{Input: 100, Output: 40, CacheRead: 10}); err != nil {
				t.Fatalf("add opus: %v", err)
			}
			if err := st.AddUsage(sessionID, "claude-opus-4-8", store.Usage{Input: 50, Output: 20}); err != nil {
				t.Fatalf("add opus 2: %v", err)
			}
			if err := st.AddUsage(sessionID, "claude-haiku-4-5", store.Usage{Input: 30, Output: 5}); err != nil {
				t.Fatalf("add haiku: %v", err)
			}

			models, err := st.SessionModelUsage(sessionID)
			if err != nil {
				t.Fatalf("session model usage: %v", err)
			}
			if len(models) != 2 {
				t.Fatalf("got %d models, want 2: %+v", len(models), models)
			}
			// Heaviest first: opus (220 tok) before haiku (35 tok).
			if models[0].Model != "claude-opus-4-8" {
				t.Errorf("models[0] = %q, want claude-opus-4-8", models[0].Model)
			}
			if got := models[0].Usage; got.Input != 150 || got.Output != 60 || got.CacheRead != 10 {
				t.Errorf("opus usage = %+v, want {150 60 10 0}", got)
			}
			if models[1].Model != "claude-haiku-4-5" {
				t.Errorf("models[1] = %q, want claude-haiku-4-5", models[1].Model)
			}
			if got := models[1].Usage; got.Input != 30 || got.Output != 5 {
				t.Errorf("haiku usage = %+v, want {30 5 0 0}", got)
			}

			// Per-model sums must equal the session aggregate.
			var in, out int64
			for _, m := range models {
				in += m.Usage.Input
				out += m.Usage.Output
			}
			var sIn, sOut int64
			if err := st.QueryRow(`SELECT input_tokens, output_tokens FROM sessions WHERE id = ?`, sessionID).Scan(&sIn, &sOut); err != nil {
				t.Fatalf("read session aggregate: %v", err)
			}
			if in != sIn || out != sOut {
				t.Errorf("per-model sums (%d/%d) != session aggregate (%d/%d)", in, out, sIn, sOut)
			}

			var scalar string
			if err := st.QueryRow(`SELECT model FROM sessions WHERE id = ?`, sessionID).Scan(&scalar); err != nil {
				t.Fatalf("read scalar model: %v", err)
			}
			if scalar != "claude-haiku-4-5" {
				t.Errorf("scalar model = %q, want claude-haiku-4-5 (last reporter)", scalar)
			}
		})
	}
}

// TestReplaceModelUsageIsAbsoluteAndDirties verifies the backfill path: it
// swaps rows for an absolute snapshot (shrinking as well as growing), skips
// empty models, and marks the session sync-dirty.
func TestReplaceModelUsageIsAbsoluteAndDirties(t *testing.T) {
	for _, b := range storetest.Both(t) {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			st := b.Store
			_, sessionID := seedUsageSession(t, st)

			if err := st.ReplaceModelUsage(sessionID, []store.ModelUsage{
				{Model: "claude-opus-4-8", Usage: store.Usage{Input: 100, Output: 50}},
				{Model: "claude-haiku-4-5", Usage: store.Usage{Input: 10, Output: 5}},
				{Model: "", Usage: store.Usage{Input: 999}},
			}); err != nil {
				t.Fatalf("replace: %v", err)
			}
			clearSessionDirty(t, st, sessionID)

			// Second snapshot drops haiku and grows opus: absolute replace.
			if err := st.ReplaceModelUsage(sessionID, []store.ModelUsage{
				{Model: "claude-opus-4-8", Usage: store.Usage{Input: 200, Output: 90}},
			}); err != nil {
				t.Fatalf("replace 2: %v", err)
			}
			if !sessionDirty(t, st, sessionID) {
				t.Error("session not marked dirty after replace")
			}

			models, err := st.SessionModelUsage(sessionID)
			if err != nil {
				t.Fatalf("session model usage: %v", err)
			}
			if len(models) != 1 {
				t.Fatalf("got %d models, want 1 (empty model skipped, haiku dropped): %+v", len(models), models)
			}
			if models[0].Model != "claude-opus-4-8" || models[0].Usage.Input != 200 || models[0].Usage.Output != 90 {
				t.Errorf("model = %+v, want opus {200 90 0 0}", models[0])
			}
		})
	}
}
