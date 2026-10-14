package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
)

// Status has no project upsert/retrieval telemetry: it can safely identify a
// store/capability mismatch before a host attempts a memory write.
type statusIn struct {
	SessionHandle string `json:"session_handle,omitempty"`
}

func (s *Server) addStatusTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "memory_status", Description: "Read local store/schema capability and binding health before capture. Compare store_fingerprint across assistants; inactive V2 or restart_required must not be treated as acknowledged capture.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.status)
}
func (s *Server) status(ctx context.Context, req *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, any, error) {
	if s.st == nil {
		return captureResult(map[string]any{"available": false})
	}
	version, err := s.st.SchemaVersion()
	if err != nil {
		return nil, nil, err
	}
	h := sha256.Sum256([]byte(string(s.st.Backend) + ":" + s.st.Path))
	report := map[string]any{"available": true, "schema": version, "backend": s.st.Backend, "store_fingerprint": hex.EncodeToString(h[:]), "v2_active": s.st.MemoryV2 && version == 16, "restart_required": s.st.AssertMemoryCapability() != nil, "attribution": "unbound"}
	proj := attribution.Resolve(s.dir)
	var pid int64
	if err := s.st.QueryRow(`SELECT id FROM projects WHERE identity=?`, proj.Identity).Scan(&pid); err == nil && s.st.MemoryV2 {
		if in.SessionHandle != "" {
			b, err := s.st.ResolveBinding(in.SessionHandle, pid, s.dir)
			if err != nil {
				return nil, nil, err
			}
			report["attribution"] = "bound"
			report["assistant"] = b.Assistant
		}
		var count int
		if err := s.st.QueryRow(`SELECT COUNT(*) FROM memory_capture_batches WHERE project_id=? AND state NOT IN ('persisted','no_durable_changes')`, pid).Scan(&count); err != nil {
			return nil, nil, err
		}
		report["pending_captures"] = count
	}
	return captureResult(report)
}
