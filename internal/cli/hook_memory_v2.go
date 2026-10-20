package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/internal/diag"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

func v2CaptureReason(st *store.Store, logger *diag.Logger, sessionID, projectID int64, trigger string, threshold int) string {
	if os.Getenv("AGENT_BRAIN_MEMORY_V2_AUTOMATION") == "off" {
		return ""
	}
	var handle, checkout string
	if err := st.QueryRow(`SELECT handle,checkout FROM memory_session_bindings WHERE session_id=? AND project_id=?`, sessionID, projectID).Scan(&handle, &checkout); err != nil {
		logger.Log("memory", "capture binding unavailable")
		return ""
	}
	binding, err := st.ResolveBinding(handle, projectID, checkout)
	if err != nil {
		return ""
	}
	var watermark, last int64
	var tools, prompts int
	if err := st.QueryRow(`SELECT COALESCE(MAX(id),0),COUNT(CASE WHEN kind='tool_use' THEN 1 END),COUNT(CASE WHEN kind='prompt' THEN 1 END) FROM events WHERE session_id=? AND kind IN ('prompt','tool_use')`, sessionID).Scan(&watermark, &tools, &prompts); err != nil {
		return ""
	}
	if prompts == 0 || (trigger != "stop" && tools == 0) {
		return ""
	}
	if trigger == "stop" {
		var lastSaved int64
		if err := st.QueryRow(`SELECT COALESCE(MAX(watermark),0) FROM memory_capture_batches WHERE source_handle=? AND state IN ('persisted','no_durable_changes')`, handle).Scan(&lastSaved); err != nil {
			return ""
		}
		var since int
		if err := st.QueryRow(`SELECT COUNT(*) FROM events WHERE session_id=? AND kind='tool_use' AND id>?`, sessionID, lastSaved).Scan(&since); err != nil || since < threshold {
			return ""
		}
	}
	if trigger != "stop" {
		var lastAt string
		if err := st.QueryRow(`SELECT COALESCE(MAX(watermark),0),COALESCE(MAX(created_at),'') FROM memory_capture_batches WHERE source_handle=?`, handle).Scan(&last, &lastAt); err != nil {
			return ""
		}
		var since int
		if err := st.QueryRow(`SELECT COUNT(*) FROM events WHERE session_id=? AND kind='tool_use' AND id>?`, sessionID, last).Scan(&since); err != nil {
			return ""
		}
		if since < threshold {
			if lastAt == "" {
				_ = st.QueryRow(`SELECT started_at FROM sessions WHERE id=?`, sessionID).Scan(&lastAt)
			}
			at, err := time.Parse(store.TimeLayout, lastAt)
			if err != nil || time.Since(at) < 5*time.Minute || since == 0 {
				return ""
			}
			trigger = "periodic"
		}
	}
	// A request that was delivered but never begun is not followed by another of
	// the same kind: an agent that cannot act on it would be blocked at every
	// stop without progress. Beginning or deferring it re-arms capture.
	class := "trigger='stop'"
	if trigger != "stop" {
		class = "trigger IN ('checkpoint','periodic')"
	}
	var unanswered int
	if err := st.QueryRow(`SELECT COUNT(*) FROM memory_capture_batches WHERE source_handle=? AND `+class+` AND state='pending' AND executor_handle IS NULL AND retries>0 AND watermark<>?`, handle, watermark).Scan(&unanswered); err != nil || unanswered > 0 {
		return ""
	}
	batch, err := st.RequestCapture(binding, trigger, watermark, time.Now())
	if err != nil {
		logger.Log("memory", "capture request failed: %v", err)
		return ""
	}
	if batch.State == "persisted" || batch.State == "no_durable_changes" {
		return ""
	}
	// Request issuance is explicitly not successful capture. Persist a bounded
	// delivery claim, preserving the batch and receipts for recovery.
	at := store.Now()
	next := time.Now().Add(30 * time.Second).UTC().Format(store.TimeLayout)
	maxAttempts := 3
	if trigger == "stop" {
		maxAttempts = 1
	}
	res, err := st.Exec(`UPDATE memory_capture_batches SET retries=retries+1,next_retry=?,updated_at=? WHERE id=? AND project_id=? AND retries<? AND next_retry<=?`, next, at, batch.ID, projectID, maxAttempts, at)
	if err != nil {
		return ""
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return ""
	}
	return fmt.Sprintf("Local memory capture checkpoint: session_handle=%s, batch_id=%s. Call memory_capture_begin with that batch and stable item labels; save each durable decision/task/fact using its returned operation_id, generation, batch_id and session_handle. Include rationale and referenced evidence. Then call memory_capture_complete after every item is persisted, or explicitly no_durable_changes for an empty manifest. Never guess user evidence or re-save acknowledged items. If context is unavailable, use memory_capture_defer(reason=context_unavailable) and report the gap. Then continue your task.", handle, batch.ID)
}

func v2RecoveryNote(st *store.Store, projectID int64) string {
	batches, err := st.PendingCaptures(projectID)
	if err != nil || len(batches) == 0 {
		return ""
	}
	return fmt.Sprintf("\nLocal memory recovery: %d incomplete capture batches. Inspect memory_capture_status using this session's handle. Recover persisted receipts and remaining context only; unsaved thoughts cannot be reconstructed reliably.\n", len(batches))
}
