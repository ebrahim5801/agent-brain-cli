package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/internal/assistant/claude"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/store"
)

// runCapture executes `memory capture` with args against a fresh sandbox and
// returns its stdout, failing the test on error when wantErr is false.
func runCapture(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newMemoryCaptureCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestMemoryCaptureShowsDefaults(t *testing.T) {
	sandbox(t)
	out, err := runCapture(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"End-of-reply save: after 10 tool calls (default)",
		"Mid-task checkpoint: every 25 tool calls (default)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestMemoryCaptureSetAndOff(t *testing.T) {
	sandbox(t)
	if _, err := runCapture(t, "--every", "0", "--checkpoint", "40"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.MemoryEvery(); got != 0 {
		t.Errorf("every after --every 0 = %d, want 0", got)
	}
	if got := cfg.MemoryCheckpoint(25); got != 40 {
		t.Errorf("checkpoint after --checkpoint 40 = %d, want 40", got)
	}
	out, err := runCapture(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "End-of-reply save: off") {
		t.Errorf("output does not report off:\n%s", out)
	}
	if !strings.Contains(out, "Mid-task checkpoint: every 40 tool calls\n") {
		t.Errorf("checkpoint line wrong (want 40 without default marker):\n%s", out)
	}
}

func TestMemoryCaptureRejectsNegative(t *testing.T) {
	sandbox(t)
	if _, err := runCapture(t, "--every", "-1"); err == nil {
		t.Error("--every -1 accepted, want error")
	}
	if _, err := runCapture(t, "--checkpoint", "-5"); err == nil {
		t.Error("--checkpoint -5 accepted, want error")
	}
	// A rejected value must not have been written.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MemoryCapture != nil {
		t.Errorf("negative input wrote config: %+v", cfg.MemoryCapture)
	}
}

func TestMemoryCaptureResetRestoresDefaults(t *testing.T) {
	sandbox(t)
	if _, err := runCapture(t, "--every", "3", "--checkpoint", "9"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCapture(t, "--reset"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MemoryEvery() != config.DefaultMemoryEvery || cfg.MemoryCheckpoint(25) != 25 {
		t.Errorf("after --reset every=%d checkpoint=%d, want defaults", cfg.MemoryEvery(), cfg.MemoryCheckpoint(25))
	}
	if _, err := runCapture(t, "--reset", "--every", "2"); err == nil {
		t.Error("--reset with --every accepted, want error")
	}
}

func TestStatusMemoryCaptureLine(t *testing.T) {
	cfg := &config.Config{}
	out := capture(t, func() { printMemoryCaptureStatus(cfg) })
	if !strings.Contains(out, "capture: end-of-reply after 10 tool calls (default); checkpoint every 25 tool calls (default)") {
		t.Errorf("default status line wrong:\n%s", out)
	}
	zero := 0
	cfg.MemoryCapture = &config.MemoryCaptureConfig{Every: &zero}
	out = capture(t, func() { printMemoryCaptureStatus(cfg) })
	if !strings.Contains(out, "end-of-reply off") {
		t.Errorf("off status line wrong:\n%s", out)
	}
}

// The store excludes the memory server's own tool calls by a name prefix that
// is spelled out there; it must track the server name the adapter registers.
func TestMemoryToolPrefixMatchesMCPServerName(t *testing.T) {
	if want := "mcp__" + claude.MCPServerName + "__"; store.MemoryToolPrefix != want {
		t.Errorf("store.MemoryToolPrefix = %q, want %q", store.MemoryToolPrefix, want)
	}
}
