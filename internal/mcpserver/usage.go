package mcpserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/stats"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type usageIn struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"Claude Code session id from the hook payload; omit for aggregate usage only."`
}

type usageReport struct {
	Session *usageSession `json:"session"`
	Today   usagePeriod   `json:"today"`
	Week    usagePeriod   `json:"week"`
}

type usageSession struct {
	ID               string `json:"id"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	DurationSeconds  int64  `json:"duration_seconds"`
	LaneRuns         int64  `json:"lane_runs"`
	LaneInputTokens  int64  `json:"lane_input_tokens"`
	LaneOutputTokens int64  `json:"lane_output_tokens"`
}

type usagePeriod struct {
	Since            string `json:"since"`
	Sessions         int64  `json:"sessions"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

type unavailableUsage struct {
	Available bool `json:"available"`
}

func (s *Server) addUsageTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "usage_stats",
		Description: "Read this machine's token usage: the given session (with its subagent runs), today, and this week. Local only; works without memory.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.usage)
}

// usage reads token counts from the local store without consulting memory
// entitlement, consent, project state, or attribution.
func (s *Server) usage(ctx context.Context, req *mcp.CallToolRequest, in usageIn) (*mcp.CallToolResult, any, error) {
	if s.st == nil {
		return captureResult(unavailableUsage{Available: false})
	}

	now := time.Now().UTC()
	todaySince := now.Format("2006-01-02")
	weekSince := currentWeekStart(now)
	today, err := usageTotals(s.st, todaySince)
	if err != nil {
		return nil, nil, err
	}
	week, err := usageTotals(s.st, weekSince)
	if err != nil {
		return nil, nil, err
	}

	var session *usageSession
	if in.SessionID != "" {
		session, err = sessionUsage(s.st, in.SessionID)
		if err != nil {
			return nil, nil, err
		}
	}

	return captureResult(usageReport{
		Session: session,
		Today:   periodUsage(todaySince, today),
		Week:    periodUsage(weekSince, week),
	})
}

func usageTotals(st *store.Store, since string) (stats.Totals, error) {
	rows, err := stats.Query(st, stats.Filter{Since: since})
	if err != nil {
		return stats.Totals{}, err
	}
	return stats.Sum(rows), nil
}

func periodUsage(since string, totals stats.Totals) usagePeriod {
	return usagePeriod{
		Since:            since,
		Sessions:         totals.Sessions,
		InputTokens:      totals.InputTokens,
		OutputTokens:     totals.OutputTokens,
		CacheReadTokens:  totals.CacheReadTokens,
		CacheWriteTokens: totals.CacheWriteTokens,
	}
}

func currentWeekStart(now time.Time) string {
	now = now.UTC()
	daysSinceMonday := (int(now.Weekday()) + 6) % 7
	return now.AddDate(0, 0, -daysSinceMonday).Format("2006-01-02")
}

func sessionUsage(st *store.Store, sessionID string) (*usageSession, error) {
	query := `
        SELECT s.id, s.input_tokens, s.output_tokens, s.started_at,
               COALESCE(s.ended_at, (SELECT MAX(e.occurred_at) FROM events e WHERE e.session_id = s.id), s.started_at)
        FROM sessions s
        WHERE s.assistant = 'claude-code' AND s.parent_session_id IS NULL AND s.external_id = ?
        ORDER BY s.started_at DESC, s.id DESC
        LIMIT 1`
	args := []any{sessionID}
	if st.MemoryV2 {
		query = `
        SELECT s.id, s.input_tokens, s.output_tokens, s.started_at,
               COALESCE(s.ended_at, (SELECT MAX(e.occurred_at) FROM events e WHERE e.session_id = s.id), s.started_at)
        FROM sessions s
        WHERE s.assistant = 'claude-code' AND s.parent_session_id IS NULL
          AND (s.external_id = ? OR EXISTS (
              SELECT 1 FROM memory_session_bindings b
              WHERE b.session_id = s.id AND b.assistant = 'claude-code' AND b.native_id = ?
          ))
        ORDER BY s.started_at DESC, s.id DESC
        LIMIT 1`
		args = []any{sessionID, sessionID}
	}

	var localID int64
	var ownInput, ownOutput int64
	var startedAt, endedAt string
	err := st.QueryRow(query, args...).Scan(&localID, &ownInput, &ownOutput, &startedAt, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// An unparseable timestamp costs only the duration, not the whole report.
	duration, _ := sessionDurationSeconds(startedAt, endedAt)

	result := &usageSession{
		ID:              sessionID,
		InputTokens:     ownInput,
		OutputTokens:    ownOutput,
		DurationSeconds: duration,
	}
	rows, err := st.Query(`SELECT input_tokens, output_tokens FROM sessions WHERE parent_session_id = ?`, localID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var input, output int64
		if err := rows.Scan(&input, &output); err != nil {
			return nil, err
		}
		result.LaneRuns++
		result.LaneInputTokens += input
		result.LaneOutputTokens += output
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func sessionDurationSeconds(startedAt, endedAt string) (int64, error) {
	start, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return 0, fmt.Errorf("parse session start time: %w", err)
	}
	end, err := time.Parse(time.RFC3339Nano, endedAt)
	if err != nil {
		return 0, fmt.Errorf("parse session end time: %w", err)
	}
	seconds := end.Unix() - start.Unix()
	if seconds < 0 {
		return 0, nil
	}
	return seconds, nil
}
