package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func listedTools(t *testing.T) map[string]*mcp.Tool {
	t.Helper()
	ctx := context.Background()
	s := New(nil, t.TempDir(), "test")
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := s.mcp.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "contract-test"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		tools[tool.Name] = tool
	}
	return tools
}

// Every tool says whether it only reads, and none claims to delete.
func TestToolHints(t *testing.T) {
	readOnly := map[string]bool{
		"memory_search": true, "memory_list": true, "memory_capture_status": true, "memory_history": true,
		"memory_source_reference": true, "memory_status": true, "usage_stats": true,
	}
	for name, tool := range listedTools(t) {
		a := tool.Annotations
		if a == nil {
			t.Errorf("%s has no annotations", name)
			continue
		}
		if a.ReadOnlyHint != readOnly[name] {
			t.Errorf("%s readOnlyHint = %v", name, a.ReadOnlyHint)
		}
		if !a.ReadOnlyHint && (a.DestructiveHint == nil || *a.DestructiveHint) {
			t.Errorf("%s may be taken for a destructive tool", name)
		}
	}
}

// A memory_source_reference result, status included, is valid evidence for
// memory_save.
func TestSaveAcceptsSourceReferenceResult(t *testing.T) {
	tool := listedTools(t)["memory_save"]
	schema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("input schema is %T", tool.InputSchema)
	}
	items := schema["properties"].(map[string]any)["evidence"].(map[string]any)["items"].(map[string]any)
	props := items["properties"].(map[string]any)
	for _, field := range []string{"type", "reference", "digest", "excerpt", "status"} {
		if _, ok := props[field]; !ok {
			t.Errorf("evidence item lacks %q, so a memory_source_reference result is rejected", field)
		}
	}
}
