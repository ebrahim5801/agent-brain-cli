package assistant

import (
	"sort"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// ModelBreakdown turns a per-model usage map into a deterministic slice ordered
// heaviest first (by total tokens, then model name) and picks the dominant
// model — the one with the most input+output tokens, matching the historical
// single-model selection carried on the session's scalar column. Usage booked
// against an empty model name is dropped from both: it carries no attributable
// cost, and ReplaceModelUsage skips such rows anyway.
//
// The ordering is a display contract shared by every adapter that recovers a
// multi-model session, so it lives here rather than in whichever adapter needed
// it first.
func ModelBreakdown(perModel map[string]store.Usage) (dominant string, rows []store.ModelUsage) {
	rows = make([]store.ModelUsage, 0, len(perModel))
	for m, u := range perModel {
		if m == "" {
			continue
		}
		rows = append(rows, store.ModelUsage{Model: m, Usage: u})
	}
	sort.Slice(rows, func(i, j int) bool {
		ti := rows[i].Usage.Input + rows[i].Usage.Output + rows[i].Usage.CacheRead + rows[i].Usage.CacheWrite
		tj := rows[j].Usage.Input + rows[j].Usage.Output + rows[j].Usage.CacheRead + rows[j].Usage.CacheWrite
		if ti != tj {
			return ti > tj
		}
		return rows[i].Model < rows[j].Model
	})
	var max int64 = -1
	for _, r := range rows {
		if n := r.Usage.Input + r.Usage.Output; n > max {
			dominant, max = r.Model, n
		}
	}
	return dominant, rows
}
