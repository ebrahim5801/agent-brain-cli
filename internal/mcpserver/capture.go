package mcpserver

import (
	"context"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

type captureIn struct {
	SessionHandle string   `json:"session_handle" jsonschema:"Hook-issued handle identifying this caller's session."`
	BatchID       string   `json:"batch_id,omitempty" jsonschema:"Stable capture batch to begin, resume or inspect; omit only to create or list."`
	Items         []string `json:"items,omitempty" jsonschema:"Stable unique item labels declaring durable memories to save; reuse the identical manifest on retry."`
	Generation    int64    `json:"generation,omitempty" jsonschema:"Current lease generation from begin."`
	Reason        string   `json:"reason,omitempty" jsonschema:"context_unavailable or persistence_retry; no raw error or transcript."`
	NoChanges     bool     `json:"no_durable_changes,omitempty" jsonschema:"True only if the declared manifest is empty and no durable memory was learned."`
}

func (s *Server) addCaptureTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "memory_capture_defer", Description: "Release a capture lease as needs_context or retry_wait without losing persisted receipts. Do not fabricate unavailable source context."}, s.captureDefer)
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "memory_capture_begin", Description: "Begin/resume acknowledged capture. Declare stable item labels, then save each using returned operation_id/batch_id/generation and this session_handle. Retry the same manifest; inspect status after lost replies."}, s.captureBegin)
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "memory_capture_complete", Description: "Acknowledge capture only after every declared item is persisted; explicitly acknowledge an empty manifest with no_durable_changes. A summary alone is not capture completion."}, s.captureComplete)
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "memory_capture_status", Description: "Inspect this project's pending capture work and durable receipts without assuming that a hook request succeeded. Missing context stays pending and is not fabricated."}, s.captureStatus)
}

func (s *Server) captureBinding(in captureIn) (store.SessionBinding, error) {
	pid, _, err := s.gate()
	if err != nil {
		return store.SessionBinding{}, err
	}
	return s.st.ResolveBinding(in.SessionHandle, pid, s.dir)
}
func captureResult(v any) (*mcp.CallToolResult, any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return textResult(string(raw)), v, nil
}
func (s *Server) captureBegin(ctx context.Context, req *mcp.CallToolRequest, in captureIn) (*mcp.CallToolResult, any, error) {
	b, err := s.captureBinding(in)
	if err != nil {
		return nil, nil, err
	}
	if in.BatchID == "" {
		var watermark int64
		if err := s.st.QueryRow(`SELECT COALESCE(MAX(id),0) FROM events WHERE session_id=? AND kind IN ('prompt','tool_use')`, b.SessionID).Scan(&watermark); err != nil {
			return nil, nil, err
		}
		batch, err := s.st.RequestCapture(b, "checkpoint", watermark, time.Now())
		if err != nil {
			return nil, nil, err
		}
		in.BatchID = batch.ID
	}
	batch, err := s.st.BeginCapture(in.BatchID, b, in.Items, time.Now())
	if err != nil {
		return nil, nil, err
	}
	return captureResult(batch)
}
func (s *Server) captureComplete(ctx context.Context, req *mcp.CallToolRequest, in captureIn) (*mcp.CallToolResult, any, error) {
	b, err := s.captureBinding(in)
	if err != nil {
		return nil, nil, err
	}
	batch, err := s.st.CompleteCapture(in.BatchID, b, in.Generation, in.NoChanges, time.Now())
	if err != nil {
		return nil, nil, err
	}
	return captureResult(batch)
}
func (s *Server) captureStatus(ctx context.Context, req *mcp.CallToolRequest, in captureIn) (*mcp.CallToolResult, any, error) {
	b, err := s.captureBinding(in)
	if err != nil {
		return nil, nil, err
	}
	if in.BatchID != "" {
		batch, err := s.st.CaptureStatus(in.BatchID, b.ProjectID)
		if err != nil {
			return nil, nil, err
		}
		return captureResult(batch)
	}
	batches, err := s.st.PendingCaptures(b.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	return captureResult(batches)
}

func (s *Server) captureDefer(ctx context.Context, req *mcp.CallToolRequest, in captureIn) (*mcp.CallToolResult, any, error) {
	b, err := s.captureBinding(in)
	if err != nil {
		return nil, nil, err
	}
	batch, err := s.st.DeferCapture(in.BatchID, b, in.Generation, in.Reason, time.Now())
	if err != nil {
		return nil, nil, err
	}
	return captureResult(batch)
}
