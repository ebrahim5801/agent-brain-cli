package cli

import (
	"fmt"
	"strings"
	"testing"
)

// Public capture preferences and the subagent exclusion must survive V2.
func TestV2CaptureRespectsConfiguredThresholdsAndSubagents(t *testing.T) {
	for _, every := range []int{0, 2} {
		t.Run(fmt.Sprintf("every-%d", every), func(t *testing.T) {
			sandbox(t)
			proj := t.TempDir()
			enableMemoryForTest(t)
			setMemoryCapture(t, intPtr(every), intPtr(0))
			st := openTestStore(t)
			if err := st.UpgradeMemoryV2(); err != nil {
				t.Fatal(err)
			}
			payload := fmt.Sprintf(`{"session_id":"v2-settings","cwd":%q}`, proj)
			hookWith(t, "session-start", "claude-code", payload)
			hookWith(t, "prompt", "claude-code", payload)
			child := fmt.Sprintf(`{"session_id":"v2-settings","cwd":%q,"tool_name":"Edit","agent_id":"child"}`, proj)
			for i := 0; i < 6; i++ {
				if got := hookWith(t, "tool-use", "claude-code", child); got != "" {
					t.Fatal("subagent received parent context/capture")
				}
			}
			if got := hookWith(t, "stop", "claude-code", payload); got != "" {
				t.Fatal("subagent activity armed parent capture")
			}
			tool := fmt.Sprintf(`{"session_id":"v2-settings","cwd":%q,"tool_name":"Edit"}`, proj)
			for i := 0; i < 2; i++ {
				if got := hookWith(t, "tool-use", "claude-code", tool); got != "" {
					t.Fatal("disabled checkpoint emitted context")
				}
				got := hookWith(t, "stop", "claude-code", payload)
				if every == 0 || i == 0 {
					if got != "" {
						t.Fatal("stop capture ignored configured threshold")
					}
				} else if !strings.Contains(got, "memory_capture_begin") {
					t.Fatal("configured stop threshold did not request acknowledged capture")
				}
			}
			if got := hookWith(t, "stop", "claude-code", payload); got != "" {
				t.Fatal("same capture watermark emitted twice")
			}
		})
	}
}
