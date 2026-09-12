package tomlfile

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TOML says [a.b], [ a . b ] and [a."b"] all name the same table, so a header
// written any of those ways is ours. Missing one is the dangerous direction:
// Contains would report the table absent, Upsert would append a second
// definition of it, and a duplicated table makes the whole file invalid TOML —
// so Codex would stop loading the user's config entirely, not just ignore us.
func TestEquivalentHeaderSpellingsAreTheSameTable(t *testing.T) {
	for _, spelling := range []string{
		`[mcp_servers.agent-brain-memory]`,
		`[ mcp_servers.agent-brain-memory ]`,
		`[mcp_servers . agent-brain-memory]`,
		`[mcp_servers."agent-brain-memory"]`,
		`["mcp_servers"."agent-brain-memory"]`,
		`[mcp_servers.'agent-brain-memory']`,
		`[mcp_servers.agent-brain-memory]  # installed by agent-brain`,
	} {
		t.Run(spelling, func(t *testing.T) {
			path := tmpFile(t)
			write(t, path, spelling+"\ncommand = \"/old/agent-brain\"\nargs = [\"mcp\"]\n\n[tui]\ntheme = \"dark\"\n")

			got, err := Contains(path, header)
			if err != nil {
				t.Fatal(err)
			}
			if !got {
				t.Fatal("spelling not recognized as our table")
			}
			if _, err := Upsert(path, ourTable()); err != nil {
				t.Fatal(err)
			}
			out := read(t, path)
			if n := strings.Count(out, "agent-brain-memory"); n != 1 {
				t.Errorf("table defined %d times after upsert, want 1:\n%s", n, out)
			}
			if strings.Contains(out, "/old/agent-brain") {
				t.Errorf("stale command survived:\n%s", out)
			}
			if !strings.HasSuffix(out, "[tui]\ntheme = \"dark\"\n") {
				t.Errorf("neighbour damaged:\n%s", out)
			}
		})
	}
}

// A header we cannot parse that nonetheless names us is ambiguous. Treating it
// as foreign would append a duplicate; treating it as ours would rewrite a line
// we do not understand. Refusing is the only safe answer, and it must leave the
// file alone.
func TestUnparseableHeaderNamingUsIsRefused(t *testing.T) {
	for _, line := range []string{
		`[mcp_servers.agent-brain-memory`,
		`[[mcp_servers.agent-brain-memory]]`,
		`[mcp_servers."agent-brain-memory]`,
	} {
		t.Run(line, func(t *testing.T) {
			path := tmpFile(t)
			seed := line + "\ncommand = \"/x\"\n"
			write(t, path, seed)

			if _, err := Upsert(path, ourTable()); err == nil {
				t.Error("expected a refusal")
			}
			if got := read(t, path); got != seed {
				t.Errorf("file was modified despite the refusal:\n%q", got)
			}
			if _, err := Contains(path, header); err == nil {
				t.Error("Contains reported an answer for an ambiguous header")
			}
		})
	}
}

// A header we cannot parse that has nothing to do with us is not our business,
// and must not block an install.
func TestUnparseableForeignHeaderIsIgnored(t *testing.T) {
	path := tmpFile(t)
	write(t, path, "[[projects]]\nname = \"a\"\n[unclosed\n")
	if _, err := Upsert(path, ourTable()); err != nil {
		t.Fatalf("a foreign malformed header blocked the install: %v", err)
	}
	if !strings.Contains(read(t, path), "[mcp_servers.agent-brain-memory]") {
		t.Error("table not written")
	}
}

// A file that mixes line endings must keep each line's own ending. Normalizing
// the document to one style would rewrite lines the user owns, which is exactly
// what this package promises not to do.
func TestMixedLineEndingsLeaveForeignLinesAlone(t *testing.T) {
	path := tmpFile(t)
	seed := "model = \"gpt-6\"\r\n\r\n[tui]\ntheme = \"dark\"\n"
	write(t, path, seed)

	if _, err := Upsert(path, ourTable()); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.HasPrefix(got, seed) {
		t.Errorf("existing lines were re-terminated:\n%q\nwant prefix\n%q", got, seed)
	}
}

// Two installs racing on one config must not share a temp file: a fixed name
// lets each write into the other's, and a failed rename leaves it beside the
// user's config forever.
func TestConcurrentUpsertsDoNotCorruptOrLitter(t *testing.T) {
	path := tmpFile(t)
	write(t, path, "model = \"gpt-6\"\n")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Upsert(path, ourTable()); err != nil {
				t.Errorf("concurrent upsert: %v", err)
			}
		}()
	}
	wg.Wait()

	got := read(t, path)
	if n := strings.Count(got, "[mcp_servers.agent-brain-memory]"); n != 1 {
		t.Errorf("table written %d times, want 1:\n%s", n, got)
	}
	if !strings.HasPrefix(got, "model = \"gpt-6\"\n") {
		t.Errorf("existing content damaged:\n%s", got)
	}
	if leftovers := mustGlob(t, path+".agent-brain-tmp-*"); len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
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

// Status asks one question per hook event against the same file. ContainsAll is
// what keeps that a single read, and it has to agree with Contains exactly.
func TestContainsAllAgreesWithContainsOverOneRead(t *testing.T) {
	path := tmpFile(t)
	write(t, path, "[a]\nx = 1\n\n[b.c]\ny = 2\n")
	headers := []string{"a", "b.c", "missing", "b"}
	found, err := ContainsAll(path, headers)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range headers {
		want, err := Contains(path, h)
		if err != nil {
			t.Fatal(err)
		}
		if found[h] != want {
			t.Errorf("ContainsAll[%q] = %v, Contains = %v", h, found[h], want)
		}
	}
	if !found["a"] || !found["b.c"] || found["missing"] || found["b"] {
		t.Errorf("found = %v", found)
	}
}

func TestContainsAllOnAMissingFileFindsNothing(t *testing.T) {
	found, err := ContainsAll(filepath.Join(t.TempDir(), "absent.toml"), []string{"a"})
	if err != nil {
		t.Fatalf("a missing file is absence, not an error: %v", err)
	}
	if found["a"] {
		t.Error("found a table in a file that does not exist")
	}
}

// Codex names a trusted hook with a table whose last segment is a filesystem
// path carrying colons and dots. Quote plus the header parser have to survive a
// round trip, or status reads every approval as missing.
func TestQuotedPathSegmentRoundTrips(t *testing.T) {
	for _, key := range []string{
		"/home/dev/.codex/hooks.json:session_start:0:0",
		`/home/dev/my "quoted" dir/hooks.json:stop:1:0`,
		`/home/dev/back\slash/hooks.json:stop:0:0`,
	} {
		header := "hooks.state." + Quote(key)
		path := tmpFile(t)
		write(t, path, "[other]\nx = 1\n\n["+header+"]\ntrusted_hash = \"sha256:beef\"\n")
		got, err := Contains(path, header)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if !got {
			t.Errorf("did not find [%s]", header)
		}
	}
}
