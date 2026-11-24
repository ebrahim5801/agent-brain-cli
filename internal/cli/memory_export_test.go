package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMemoryExportMarkdown(t *testing.T) {
	work := setupMemoryList(t, 4)
	cmd := newMemoryExportCmd()
	var err error
	out := capture(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Project memory: ", "## Decisions", "## Facts", "cli-memory-001", "cli-memory-004", " · critical · auto · 2026-01-01", "agent-brain memory edit <id>"} {
		if !strings.Contains(out, want) {
			t.Errorf("export lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "## Decisions") > strings.Index(out, "## Facts") || strings.Contains(out, "## Retired") {
		t.Errorf("sections out of order or retired shown by default:\n%s", out)
	}

	// An existing world-readable file is replaced by a private one, and a
	// symlink at the destination is replaced rather than written through.
	file := filepath.Join(work, "memory.md")
	if err := os.WriteFile(file, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		elsewhere := filepath.Join(t.TempDir(), "elsewhere.md")
		if err := os.WriteFile(elsewhere, []byte("untouched"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(work, "linked.md")
		if err := os.Symlink(elsewhere, link); err != nil {
			t.Fatal(err)
		}
		cmd = newMemoryExportCmd()
		cmd.SetArgs([]string{"--out", link})
		capture(t, func() { err = cmd.Execute() })
		if err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(elsewhere); string(data) != "untouched" {
			t.Fatalf("export wrote through a symlink: %q", data)
		}
	}
	cmd = newMemoryExportCmd()
	cmd.SetArgs([]string{"--out", file})
	capture(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(data), "## Facts") || !strings.Contains(string(data), "cli-memory-004") {
		t.Fatalf("file export: %v\n%s", err, data)
	}
	if fi, err := os.Stat(file); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("export file mode: %v %v", fi.Mode(), err)
	}
}
