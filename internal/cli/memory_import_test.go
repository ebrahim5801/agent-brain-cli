package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func runMemoryCmd(t *testing.T, newCmd func() *cobra.Command, args ...string) (string, error) {
	t.Helper()
	cmd := newCmd()
	cmd.SetArgs(args)
	var err error
	out := capture(t, func() { err = cmd.Execute() })
	return out, err
}

// The JSON export moves a project's memories to another machine: the import
// previews, refuses to run unattended without --yes, adds each entry once, and
// checks it is in the project the file came from.
func TestMemoryExportImportJSON(t *testing.T) {
	work := setupMemoryList(t, 3)
	file := filepath.Join(t.TempDir(), "memory.json")
	if _, err := runMemoryCmd(t, newMemoryExportCmd, "--format", "json", "--out", file); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(file); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("export file mode %v, want 0600", info.Mode().Perm())
	}
	if _, err := runMemoryCmd(t, newMemoryExportCmd, "--format", "yaml"); err == nil {
		t.Fatal("unknown format accepted")
	}

	// The other machine: a new store, same project folder.
	other := t.TempDir()
	t.Setenv("AGENT_BRAIN_DATA_DIR", filepath.Join(other, "data"))
	t.Setenv("AGENT_BRAIN_CONFIG_DIR", filepath.Join(other, "config"))
	out, err := runMemoryCmd(t, newMemoryImportCmd, file, "--dry-run")
	if err != nil || !strings.Contains(out, "New: 3") || !strings.Contains(out, "Nothing written") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}

	// No terminal to ask in: nothing is imported without --yes.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	stdin := os.Stdin
	os.Stdin = r
	_, err = runMemoryCmd(t, newMemoryImportCmd, file)
	os.Stdin = stdin
	r.Close()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("import without a terminal or --yes: %v", err)
	}

	out, err = runMemoryCmd(t, newMemoryImportCmd, file, "--yes")
	if err != nil || !strings.Contains(out, "Imported 3 entries") {
		t.Fatalf("import: %v\n%s", err, out)
	}
	out, err = runMemoryCmd(t, newMemoryImportCmd, file, "--yes")
	if err != nil || !strings.Contains(out, "Already here: 3") || !strings.Contains(out, "Nothing to import") {
		t.Fatalf("second import: %v\n%s", err, out)
	}
	out, err = runMemoryCmd(t, newMemoryListCmd)
	if err != nil || !strings.Contains(out, "cli-memory-003") {
		t.Fatalf("list after import: %v\n%s", err, out)
	}

	elsewhere := t.TempDir()
	if err := os.Chdir(elsewhere); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(work) })
	if _, err := runMemoryCmd(t, newMemoryImportCmd, file, "--yes"); err == nil || !strings.Contains(err.Error(), "--into-current-project") {
		t.Fatalf("import into another project: %v", err)
	}
	if out, err = runMemoryCmd(t, newMemoryImportCmd, file, "--yes", "--into-current-project"); err != nil || !strings.Contains(out, "Imported 3 entries") {
		t.Fatalf("import with --into-current-project: %v\n%s", err, out)
	}
}
