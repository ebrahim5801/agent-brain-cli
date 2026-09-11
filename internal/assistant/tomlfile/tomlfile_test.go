package tomlfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const header = "mcp_servers.agent-brain-memory"

func ourTable() Table {
	return Table{
		Header: header,
		Lines: []string{
			`command = "/usr/local/bin/agent-brain"`,
			`args = ["mcp"]`,
		},
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func tmpFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.toml")
}

func TestUpsertCreatesMissingFile(t *testing.T) {
	path := tmpFile(t)
	backup, err := Upsert(path, ourTable())
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Errorf("creating a new file returned a backup: %q", backup)
	}
	want := "[mcp_servers.agent-brain-memory]\ncommand = \"/usr/local/bin/agent-brain\"\nargs = [\"mcp\"]\n"
	if got := read(t, path); got != want {
		t.Errorf("content =\n%q\nwant\n%q", got, want)
	}
}

func TestUpsertAppendsAndBacksUpExistingFile(t *testing.T) {
	path := tmpFile(t)
	seed := "model = \"gpt-6\"\n\n[tui]\ntheme = \"dark\"\n"
	write(t, path, seed)

	backup, err := Upsert(path, ourTable())
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" {
		t.Fatal("changing an existing file returned no backup")
	}
	if got := read(t, backup); got != seed {
		t.Errorf("backup does not hold the original bytes: %q", got)
	}
	got := read(t, path)
	if !strings.HasPrefix(got, seed) {
		t.Errorf("existing content was not preserved verbatim at the head:\n%q", got)
	}
	if !strings.HasSuffix(got, "[mcp_servers.agent-brain-memory]\ncommand = \"/usr/local/bin/agent-brain\"\nargs = [\"mcp\"]\n") {
		t.Errorf("table not appended at end:\n%q", got)
	}
}

func TestUpsertIsIdempotent(t *testing.T) {
	path := tmpFile(t)
	write(t, path, "model = \"gpt-6\"\n")
	if _, err := Upsert(path, ourTable()); err != nil {
		t.Fatal(err)
	}
	before := read(t, path)

	backup, err := Upsert(path, ourTable())
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Errorf("idempotent upsert created a backup: %q", backup)
	}
	if after := read(t, path); after != before {
		t.Errorf("idempotent upsert changed bytes:\n%q\nvs\n%q", before, after)
	}
}

func TestUpsertReplacesChangedTableInPlace(t *testing.T) {
	path := tmpFile(t)
	seed := "[mcp_servers.agent-brain-memory]\ncommand = \"/old/path/agent-brain\"\nargs = [\"mcp\"]\n\n[tui]\ntheme = \"dark\"\n"
	write(t, path, seed)

	if _, err := Upsert(path, ourTable()); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Contains(got, "/old/path/agent-brain") {
		t.Errorf("stale command survived:\n%q", got)
	}
	if !strings.Contains(got, `command = "/usr/local/bin/agent-brain"`) {
		t.Errorf("new command missing:\n%q", got)
	}
	// The neighbour must keep its position, not be pushed to the end.
	if !strings.HasSuffix(got, "[tui]\ntheme = \"dark\"\n") {
		t.Errorf("neighbouring table moved or was damaged:\n%q", got)
	}
}

// A table sitting mid-file with a trailing comment is the case the plan calls
// out: the comment belongs to whatever follows, not to our table.
func TestUpsertMidFileWithTrailingComment(t *testing.T) {
	path := tmpFile(t)
	seed := "model = \"gpt-6\"\n\n" +
		"[mcp_servers.agent-brain-memory]\ncommand = \"/old/agent-brain\"\nargs = [\"mcp\"]\n\n" +
		"# keep this comment with the tui section\n[tui]\ntheme = \"dark\"\n"
	write(t, path, seed)

	if _, err := Upsert(path, ourTable()); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "# keep this comment with the tui section\n[tui]") {
		t.Errorf("comment lost or detached from its section:\n%q", got)
	}
	if strings.Contains(got, "/old/agent-brain") {
		t.Errorf("stale command survived:\n%q", got)
	}
}

func TestUpsertPreservesCRLF(t *testing.T) {
	path := tmpFile(t)
	write(t, path, "model = \"gpt-6\"\r\n\r\n[tui]\r\ntheme = \"dark\"\r\n")

	if _, err := Upsert(path, ourTable()); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("write introduced a bare LF into a CRLF file:\n%q", got)
	}
	if !strings.Contains(got, "[mcp_servers.agent-brain-memory]\r\n") {
		t.Errorf("table not written with CRLF:\n%q", got)
	}
}

func TestUpsertDuplicateHeaderErrorsAndLeavesFileUntouched(t *testing.T) {
	path := tmpFile(t)
	seed := "[mcp_servers.agent-brain-memory]\ncommand = \"/a\"\n\n[mcp_servers.agent-brain-memory]\ncommand = \"/b\"\n"
	write(t, path, seed)

	if _, err := Upsert(path, ourTable()); err == nil {
		t.Fatal("expected an error for a duplicated table header")
	}
	if got := read(t, path); got != seed {
		t.Errorf("file was modified despite the error:\n%q", got)
	}
	entries, _ := filepath.Glob(path + ".agent-brain-*")
	if len(entries) != 0 {
		t.Errorf("error path left side files: %v", entries)
	}
}

func TestRemoveLeavesForeignTablesByteIdentical(t *testing.T) {
	path := tmpFile(t)
	seed := "model = \"gpt-6\"\n\n" +
		"[mcp_servers.other]\ncommand = \"other\"\nargs = [\"run\"]\n\n" +
		"# a comment the user wrote\n[tui]\ntheme = \"dark\"\n"
	write(t, path, seed)

	if _, err := Upsert(path, ourTable()); err != nil {
		t.Fatal(err)
	}
	if err := Remove(path, header); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != seed {
		t.Errorf("install/uninstall cycle did not round-trip:\ngot  %q\nwant %q", got, seed)
	}
}

// Remove must not back up: Install's backup is the restore path, matching
// settingsfile.WriteIfChanged. A backup here would litter the user's config dir
// on every uninstall.
func TestRemoveTakesNoBackup(t *testing.T) {
	path := tmpFile(t)
	write(t, path, "model = \"gpt-6\"\n")
	if _, err := Upsert(path, ourTable()); err != nil {
		t.Fatal(err)
	}
	for _, b := range mustGlob(t, path+".agent-brain-backup-*") {
		if err := os.Remove(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := Remove(path, header); err != nil {
		t.Fatal(err)
	}
	if got := mustGlob(t, path+".agent-brain-backup-*"); len(got) != 0 {
		t.Errorf("remove created backups: %v", got)
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	got, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRemoveMidFileTable(t *testing.T) {
	path := tmpFile(t)
	write(t, path, "[mcp_servers.agent-brain-memory]\ncommand = \"/a\"\nargs = [\"mcp\"]\n\n[tui]\ntheme = \"dark\"\n")

	if err := Remove(path, header); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Contains(got, "agent-brain") {
		t.Errorf("our table survived:\n%q", got)
	}
	if !strings.Contains(got, "[tui]\ntheme = \"dark\"\n") {
		t.Errorf("neighbour damaged:\n%q", got)
	}
}

func TestRemoveMissingTableAndMissingFileAreNoops(t *testing.T) {
	path := tmpFile(t)
	if err := Remove(path, header); err != nil {
		t.Errorf("remove on a missing file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("remove created the file")
	}

	seed := "model = \"gpt-6\"\n"
	write(t, path, seed)
	if err := Remove(path, header); err != nil {
		t.Errorf("remove of an absent table: %v", err)
	}
	if got := read(t, path); got != seed {
		t.Errorf("no-op remove changed bytes: %q", got)
	}
}

func TestRemoveDuplicateHeaderErrorsAndLeavesFileUntouched(t *testing.T) {
	path := tmpFile(t)
	seed := "[mcp_servers.agent-brain-memory]\ncommand = \"/a\"\n\n[mcp_servers.agent-brain-memory]\ncommand = \"/b\"\n"
	write(t, path, seed)

	if err := Remove(path, header); err == nil {
		t.Fatal("expected an error for a duplicated table header")
	}
	if got := read(t, path); got != seed {
		t.Errorf("file was modified despite the error:\n%q", got)
	}
}

func TestContains(t *testing.T) {
	path := tmpFile(t)
	if got, err := Contains(path, header); err != nil || got {
		t.Errorf("missing file: got %v, %v; want false, nil", got, err)
	}
	write(t, path, "model = \"gpt-6\"\n")
	if got, _ := Contains(path, header); got {
		t.Error("reported present in a file without the table")
	}
	if _, err := Upsert(path, ourTable()); err != nil {
		t.Fatal(err)
	}
	if got, _ := Contains(path, header); !got {
		t.Error("reported absent after upsert")
	}
}

// A table name that is a prefix of ours must not be mistaken for it, and vice
// versa — the match is on the whole header line, not a substring.
func TestHeaderMatchIsExact(t *testing.T) {
	path := tmpFile(t)
	write(t, path, "[mcp_servers.agent-brain-memory-other]\ncommand = \"x\"\n")
	if got, _ := Contains(path, header); got {
		t.Error("a longer header was matched as ours")
	}
	if err := Remove(path, header); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, path), "agent-brain-memory-other") {
		t.Error("removed a table whose header merely shares our prefix")
	}
}

func TestUpsertPreservesFilePermissions(t *testing.T) {
	path := tmpFile(t)
	write(t, path, "model = \"gpt-6\"\n")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Upsert(path, ourTable()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("permissions widened to %v", fi.Mode().Perm())
	}
}
