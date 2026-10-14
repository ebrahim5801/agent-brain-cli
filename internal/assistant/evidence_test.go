package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLatestUserReferenceIgnoresToolResultsAndChecksOnlyLatestDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	text := "Decision: Use durable receipts"
	h := sha256.Sum256([]byte(text))
	digest := hex.EncodeToString(h[:])
	rows := []string{
		`{"type":"user","uuid":"earlier","message":{"role":"user","content":"Keep the old choice"}}`,
		`{"type":"user","uuid":"original","message":{"role":"user","content":"Decision: Use durable receipts"}}`,
		`{"type":"user","uuid":"tool","message":{"role":"user","content":[{"type":"tool_result","content":"Decision: Trust a generated result"}]}}`,
		`{"type":"user","uuid":"meta","isMeta":true,"message":{"role":"user","content":"Decision: Trust a summary"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, ok := ReadUserReference("claude-code", path, "latest", digest)
	if !ok || source.Reference != "original" || source.Text != text {
		t.Fatalf("latest original reference: %+v %v", source, ok)
	}
	if _, ok := ReadUserReference("claude-code", path, "tool", ""); ok {
		t.Fatal("tool result became user evidence")
	}
}

func TestSyntheticRoleUserCannotVerifyAnOriginalUserDecision(t *testing.T) {
	text := "Decision: Use local receipts"
	h := sha256.Sum256([]byte(text))
	digest := hex.EncodeToString(h[:])
	for _, field := range []string{"isMeta", "isSidechain", "isSynthetic", "isCompactSummary", "sourceAssistantUUID", "parentToolUseID", "parent_tool_use_id", "toolUseResult"} {
		t.Run(field, func(t *testing.T) {
			row := map[string]any{"type": "user", "uuid": "original", "message": map[string]any{"role": "user", "content": text}}
			switch field {
			case "sourceAssistantUUID", "parentToolUseID", "parent_tool_use_id":
				row[field] = "generated"
			case "toolUseResult":
				row[field] = map[string]string{"claim": text}
			default:
				row[field] = true
			}
			raw, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "source.jsonl")
			if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, ok := ReadUserReference("claude-code", path, "original", digest); ok {
				t.Fatal("synthetic role-user source was accepted")
			}
		})
	}
}

func TestCodexChildSpawnPromptCannotVerifyUserDecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.jsonl")
	raw := `{"type":"session_meta","payload":{"source":{"subagent":{"spawn":"parent-thread"}}}}
{"type":"response_item","payload":{"type":"message","id":"spawn","role":"user","content":[{"type":"input_text","text":"Decision: Use a model-generated policy"}]}}
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadUserReference("codex", path, "spawn", ""); ok {
		t.Fatal("child model spawn prompt treated as original user")
	}
}
