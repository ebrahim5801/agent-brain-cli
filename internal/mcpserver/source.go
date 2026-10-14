package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ebrahim5801/agent-brain-cli/internal/assistant"
	"github.com/ebrahim5801/agent-brain-cli/internal/redact"
)

type sourceIn struct {
	SessionHandle string `json:"session_handle" jsonschema:"Hook-issued handle for the original source conversation."`
	Reference     string `json:"reference" jsonschema:"Exact original user-message ID or line:N. Use latest only to reference the current session's latest user message."`
}
type sourceOut struct {
	Type      string `json:"type"`
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
	Excerpt   string `json:"excerpt"`
	Status    string `json:"status"`
}

func (s *Server) addSourceTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "memory_source_reference", Description: "Resolve a relevant original user message locally to a stable evidence reference/digest and a redacted excerpt. Does not store full messages/transcripts. Provenance is not proof that a paraphrased decision is true."}, s.sourceReference)
}
func (s *Server) sourceReference(ctx context.Context, req *mcp.CallToolRequest, in sourceIn) (*mcp.CallToolResult, any, error) {
	pid, _, err := s.gate()
	if err != nil {
		return nil, nil, err
	}
	b, err := s.st.ResolveBinding(in.SessionHandle, pid, s.dir)
	if err != nil {
		return nil, nil, err
	}
	var path string
	if err := s.st.QueryRow(`SELECT COALESCE(transcript_path,'') FROM sessions WHERE id=? AND project_id=?`, b.SessionID, pid).Scan(&path); err != nil {
		return nil, nil, err
	}
	src, ok := assistant.ReadUserReference(b.Assistant, path, in.Reference, "")
	if !ok {
		return nil, nil, fmt.Errorf("original user message is unavailable; keep evidence unverified")
	}
	excerpt := []rune(redact.Apply(src.Text))
	if len(excerpt) > 500 {
		excerpt = excerpt[:500]
	}
	return captureResult(sourceOut{Type: "user_message", Reference: src.Reference, Digest: src.Digest, Excerpt: string(excerpt), Status: "source_checked"})
}
