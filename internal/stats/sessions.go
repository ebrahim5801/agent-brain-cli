package stats

import (
	"strings"

	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

type SessionRow struct {
	ID              string `json:"id,omitempty"`
	Project         string `json:"project"`
	StartedAt       string `json:"started_at"`
	DurationSeconds int64  `json:"duration_seconds"`
	Model           string `json:"model,omitempty"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	Summary         string `json:"summary,omitempty"`
	Open            bool   `json:"open,omitempty"`
	// AgentType marks a sub-session (a subagent run); ParentID is the parent
	// session's sync uid; Prompt is the task the parent assistant spawned the
	// agent with (shown in --json output, like the full summary).
	AgentType string `json:"agent_type,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
	// Models is the per-model token breakdown, set only when the session used
	// more than one model; Model above still names the dominant one.
	Models []store.ModelUsage `json:"models,omitempty"`
}

// Sessions lists individual sessions newest first, with the agent-written
// summary when one was captured at distillation.
func Sessions(st *store.Store, f Filter) ([]SessionRow, error) {
	d := dialectFor(st.Backend)
	rows, err := st.Query(`
        SELECT COALESCE(s.sync_uid, ''), p.display_name, s.started_at,
               `+d.max0(d.durationSeconds("s"))+`,
               COALESCE(s.model, ''), s.input_tokens, s.output_tokens,
               COALESCE(s.summary, ''), s.ended_at IS NULL,
               COALESCE(s.agent_type, ''), COALESCE(parent.sync_uid, ''), COALESCE(s.agent_prompt, '')
        FROM sessions s
        JOIN projects p ON p.id = s.project_id
        LEFT JOIN sessions parent ON parent.id = s.parent_session_id
        WHERE (? = '' OR s.started_at >= ?)
          AND (? = '' OR p.display_name = ?)
          AND (? = '' OR s.sync_uid LIKE ? || '%' OR parent.sync_uid LIKE ? || '%')
        ORDER BY s.started_at DESC
        LIMIT 100`,
		f.Since, f.Since, f.Project, f.Project, f.SessionID, f.SessionID, f.SessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(&r.ID, &r.Project, &r.StartedAt, &r.DurationSeconds,
			&r.Model, &r.InputTokens, &r.OutputTokens, &r.Summary, &r.Open,
			&r.AgentType, &r.ParentID, &r.Prompt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachModelUsage(st, out); err != nil {
		return nil, err
	}
	return out, nil
}

// attachModelUsage fills each row's per-model breakdown in one query, keyed by
// the session's sync uid, and keeps it only for sessions that used more than
// one model (a single-model session is fully described by Model).
func attachModelUsage(st *store.Store, rows []SessionRow) error {
	uids := make([]string, 0, len(rows))
	idx := map[string]int{}
	for i, r := range rows {
		if r.ID == "" {
			continue
		}
		uids = append(uids, r.ID)
		idx[r.ID] = i
	}
	if len(uids) == 0 {
		return nil
	}
	placeholders := make([]string, len(uids))
	args := make([]any, len(uids))
	for i, u := range uids {
		placeholders[i] = "?"
		args[i] = u
	}
	q := `
        SELECT s.sync_uid, smu.model,
               smu.input_tokens, smu.output_tokens, smu.cache_read_tokens, smu.cache_write_tokens
        FROM session_model_usage smu
        JOIN sessions s ON s.id = smu.session_id
        WHERE s.sync_uid IN (` + strings.Join(placeholders, ",") + `)
        ORDER BY s.sync_uid,
                 (smu.input_tokens + smu.output_tokens + smu.cache_read_tokens + smu.cache_write_tokens) DESC,
                 smu.model`
	mrows, err := st.Query(q, args...)
	if err != nil {
		return err
	}
	defer mrows.Close()
	byUID := map[string][]store.ModelUsage{}
	for mrows.Next() {
		var uid string
		var m store.ModelUsage
		if err := mrows.Scan(&uid, &m.Model, &m.Usage.Input, &m.Usage.Output, &m.Usage.CacheRead, &m.Usage.CacheWrite); err != nil {
			return err
		}
		byUID[uid] = append(byUID[uid], m)
	}
	if err := mrows.Err(); err != nil {
		return err
	}
	for uid, models := range byUID {
		if len(models) < 2 {
			continue
		}
		rows[idx[uid]].Models = models
	}
	return nil
}
