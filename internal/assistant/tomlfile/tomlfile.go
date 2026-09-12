// Package tomlfile edits a single named table in a TOML file at the text
// level, without parsing and re-serializing the document. It is the TOML
// counterpart of internal/assistant/settingsfile and carries the same four
// obligations: idempotence before write, refuse-on-damage, atomic write, and
// backup-before-change.
//
// The text-level approach is deliberate. Codex CLI's config.toml is a
// hand-edited file carrying comments, key order, and formatting that a
// parse-and-reserialize round-trip would silently destroy; agent-brain
// advertises that a user's other settings are untouched, and for this file that
// has to mean byte-untouched. The cost is that only whole-table add and remove
// are supported, which is all MCP server registration needs.
package tomlfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Table is a TOML table to add or remove: a header like
// "mcp_servers.agent-brain-memory" plus its key lines, in the order they should
// be written.
type Table struct {
	Header string
	Lines  []string
}

// Render returns the table's canonical text, header first, newline-terminated.
func (t Table) Render() string {
	var b strings.Builder
	b.WriteString("[" + t.Header + "]\n")
	for _, l := range t.Lines {
		b.WriteString(l + "\n")
	}
	return b.String()
}

func headerLine(header string) string { return "[" + header + "]" }

// isHeader reports whether a line opens any TOML table. Leading whitespace is
// tolerated; a '[' inside a string value never starts a line in practice, and
// treating one as a header only ends our removal span early, which is the safe
// direction.
func isHeader(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "[")
}

// tableKey parses a table header line into its dotted key segments, unquoted.
// TOML treats [a.b], [ a . b ] and [a."b"] as naming the same table, so
// comparing header lines byte-for-byte would miss a user's variant spelling —
// and a missed match is the dangerous direction, not the safe one: Contains
// reports the table absent, Upsert appends a second definition of it, and a
// duplicated table makes the whole file invalid TOML, so Codex stops loading
// any of the user's config rather than just ignoring our entry. ok is false for
// anything that is not a plain table header, an array-of-tables [[a]] included.
func tableKey(line string) ([]string, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "[") || strings.HasPrefix(s, "[[") {
		return nil, false
	}
	var parts []string
	i := 1
	for {
		i = skipSpace(s, i)
		seg, next, ok := readKeySegment(s, i)
		if !ok {
			return nil, false
		}
		parts = append(parts, seg)
		i = skipSpace(s, next)
		if i >= len(s) {
			return nil, false
		}
		switch s[i] {
		case '.':
			i++
		case ']':
			// Only a comment may follow the closing bracket.
			rest := strings.TrimSpace(s[i+1:])
			return parts, rest == "" || strings.HasPrefix(rest, "#")
		default:
			return nil, false
		}
	}
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func isBareKeyByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// readKeySegment reads one segment of a dotted key — bare, "quoted" or
// 'literal' — returning its value and the index just past it.
func readKeySegment(s string, i int) (string, int, bool) {
	if i >= len(s) {
		return "", i, false
	}
	switch s[i] {
	case '"':
		var b strings.Builder
		for j := i + 1; j < len(s); j++ {
			switch s[j] {
			case '\\':
				// Only the escapes a table name can realistically carry are
				// decoded. Anything else stays literal, which at worst leaves an
				// exotic spelling unrecognized — and an unrecognized header that
				// names us is caught by findTable rather than ignored.
				if j+1 < len(s) && (s[j+1] == '"' || s[j+1] == '\\') {
					j++
				}
				b.WriteByte(s[j])
			case '"':
				return b.String(), j + 1, true
			default:
				b.WriteByte(s[j])
			}
		}
		return "", i, false
	case '\'':
		if j := strings.IndexByte(s[i+1:], '\''); j >= 0 {
			return s[i+1 : i+1+j], i + j + 2, true
		}
		return "", i, false
	}
	start := i
	for i < len(s) && isBareKeyByte(s[i]) {
		i++
	}
	if i == start {
		return "", i, false
	}
	return s[start:i], i, true
}

func equalKey(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// splitLines splits content on LF, leaving each line's carriage return attached
// to it, and reports whether CRLF is the file's prevailing style so lines we
// author match it. Keeping each terminator with its own line is what lets an
// edit reproduce every untouched line byte-for-byte even in a file that mixes
// CRLF and LF — normalizing the document to one style would rewrite the user's
// other lines, which is exactly what this package promises not to do.
func splitLines(data []byte) (lines []string, crlf bool, trailingNewline bool) {
	s := string(data)
	if s == "" {
		return nil, false, false
	}
	trailingNewline = strings.HasSuffix(s, "\n")
	if trailingNewline {
		s = strings.TrimSuffix(s, "\n")
	}
	lines = strings.Split(s, "\n")
	withCR := 0
	for _, l := range lines {
		if strings.HasSuffix(l, "\r") {
			withCR++
		}
	}
	return lines, withCR*2 > len(lines), trailingNewline
}

func joinLines(lines []string, trailingNewline bool) []byte {
	s := strings.Join(lines, "\n")
	if trailingNewline && s != "" {
		s += "\n"
	}
	return []byte(s)
}

// findTable locates the sole occurrence of header, returning the index of its
// header line and of the first line past the table. A table runs to the next
// table header or EOF. A duplicate header is an error: with two candidates
// there is no way to tell which one is live, and guessing risks deleting the
// user's.
func findTable(lines []string, header string) (start, end int, found bool, err error) {
	want, ok := tableKey(headerLine(header))
	if !ok {
		return 0, 0, false, fmt.Errorf("%q is not a usable TOML table name", header)
	}
	last := want[len(want)-1]
	start = -1
	for i, l := range lines {
		got, ok := tableKey(l)
		if !ok {
			// A header we cannot parse that nonetheless mentions our name is
			// ambiguous, and both answers are unsafe: calling it foreign makes
			// Upsert append a duplicate of a table that may already exist, and
			// calling it ours would edit a line we do not understand. Refuse.
			if isHeader(l) && strings.Contains(l, last) {
				return 0, 0, false, fmt.Errorf("cannot parse the table header %q, which may already be [%s]; refusing to touch the file", strings.TrimSpace(l), header)
			}
			continue
		}
		if !equalKey(got, want) {
			continue
		}
		if start != -1 {
			return 0, 0, false, fmt.Errorf("table [%s] appears more than once; refusing to guess which is live", header)
		}
		start = i
	}
	if start == -1 {
		return 0, 0, false, nil
	}
	end = len(lines)
	for i := start + 1; i < len(lines); i++ {
		if isHeader(lines[i]) {
			end = i
			break
		}
	}
	// Blank and comment lines immediately before the next header document that
	// header, not ours — a comment written above [tui] belongs to [tui]. Walking
	// the span back past them keeps a replace or remove from swallowing the
	// user's prose. The cost is that a comment a user wrote inside our own table
	// survives a remove, which is the safe direction to err.
	for end > start+1 {
		prev := strings.TrimSpace(lines[end-1])
		if prev == "" || strings.HasPrefix(prev, "#") {
			end--
			continue
		}
		break
	}
	return start, end, true, nil
}

// Upsert ensures the file at path contains exactly table, appending it at the
// end when absent and replacing it in place when present with different
// content. A file already carrying the table verbatim is left byte-identical
// and yields no backup. When a write does happen and the file existed, its
// original bytes are copied to <path>.agent-brain-backup-<UTC> first.
func Upsert(path string, table Table) (backup string, err error) {
	return edit(path, true, func(lines []string, crlf bool) ([]string, bool, error) {
		start, end, found, err := findTable(lines, table.Header)
		if err != nil {
			return nil, false, err
		}
		want := renderLines(table, crlf)
		if found {
			if equalIgnoringTrailingBlanks(lines[start:end], want) {
				return lines, false, nil
			}
			out := append([]string{}, lines[:start]...)
			out = append(out, want...)
			out = append(out, lines[end:]...)
			return out, true, nil
		}
		out := append([]string{}, lines...)
		// Separate from whatever precedes us, but never open a file with a
		// leading blank line.
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, lineEnding(crlf))
		}
		return append(out, want...), true, nil
	})
}

// lineEnding is the content of a blank line in a file of the given style: a
// bare carriage return under CRLF, since joinLines supplies only the LF.
func lineEnding(crlf bool) string {
	if crlf {
		return "\r"
	}
	return ""
}

// renderLines is Table.Render split into lines carrying the file's own line
// ending, so a table written into a CRLF document does not introduce bare LFs.
func renderLines(t Table, crlf bool) []string {
	suffix := lineEnding(crlf)
	out := make([]string, 0, len(t.Lines)+1)
	out = append(out, headerLine(t.Header)+suffix)
	for _, l := range t.Lines {
		out = append(out, l+suffix)
	}
	return out
}

// Remove deletes the named table and the key lines belonging to it, leaving
// every neighbouring table byte-identical. A file without the table, or no file
// at all, is a success no-op. No backup is written: the pre-integration backup
// taken at install time is the restore path, matching settingsfile.WriteIfChanged.
func Remove(path string, header string) error {
	_, err := edit(path, false, func(lines []string, _ bool) ([]string, bool, error) {
		start, end, found, err := findTable(lines, header)
		if err != nil {
			return nil, false, err
		}
		if !found {
			return lines, false, nil
		}
		out := append([]string{}, lines[:start]...)
		out = append(out, lines[end:]...)
		// Collapse the blank separator line Upsert inserted ahead of the table,
		// so an install/uninstall cycle round-trips instead of slowly accreting
		// blank lines. Only the line immediately before the removed span is a
		// candidate, and only when it now sits at EOF or against another table
		// header — a blank line between two foreign tables is the user's.
		if start > 0 && strings.TrimSpace(out[start-1]) == "" {
			atEOF := start == len(out)
			beforeHeader := start < len(out) && isHeader(out[start])
			if atEOF || beforeHeader {
				out = append(out[:start-1], out[start:]...)
			}
		}
		return out, true, nil
	})
	return err
}

// Contains reports whether the file carries the named table. A missing file is
// not an error — it is simply absent.
func Contains(path string, header string) (bool, error) {
	found, err := ContainsAll(path, []string{header})
	if err != nil {
		return false, err
	}
	return found[header], nil
}

// ContainsAll answers Contains for several tables off a single read, keyed by
// the header string the caller passed. A missing file carries none of them.
func ContainsAll(path string, headers []string) (map[string]bool, error) {
	found := make(map[string]bool, len(headers))
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return found, nil
	}
	if err != nil {
		return nil, err
	}
	lines, _, _ := splitLines(data)
	for _, header := range headers {
		_, _, ok, err := findTable(lines, header)
		if err != nil {
			return nil, err
		}
		found[header] = ok
	}
	return found, nil
}

// Quote renders s as a TOML basic string, for callers assembling a dotted
// header whose segments are not bare keys — a filesystem path, say. It is the
// inverse of the quoted-segment case in readKeySegment.
func Quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// equalIgnoringTrailingBlanks compares a table's existing span against the
// wanted one, ignoring blank lines at the end of the existing span — those
// belong to the separation between tables, not to the table's content.
func equalIgnoringTrailingBlanks(got, want []string) bool {
	for len(got) > 0 && strings.TrimSpace(got[len(got)-1]) == "" {
		got = got[:len(got)-1]
	}
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		// Trailing carriage returns are the file's line ending, not content: a
		// table already present as LF inside a mostly-CRLF file still matches,
		// so an install stays a no-op instead of rewriting it every run.
		if strings.TrimRight(got[i], "\r") != strings.TrimRight(want[i], "\r") {
			return false
		}
	}
	return true
}

// edit loads, applies mutate, and writes only when the bytes actually change,
// backing up the original first when takeBackup is set — Upsert does (it is an
// install-time change to a file the user owns), Remove does not (the backup
// Install already took is the restore path). The changed flag mutate returns is advisory;
// the byte comparison is what gates the write, so a mutation that happens to
// produce identical content still writes nothing.
func edit(path string, takeBackup bool, mutate func(lines []string, crlf bool) ([]string, bool, error)) (backup string, err error) {
	data, err := os.ReadFile(path)
	existed := true
	if os.IsNotExist(err) {
		data, existed, err = nil, false, nil
	}
	if err != nil {
		return "", err
	}
	lines, crlf, trailingNewline := splitLines(data)
	if !existed {
		trailingNewline = true
	}
	out, changed, err := mutate(lines, crlf)
	if err != nil {
		return "", err
	}
	after := joinLines(out, trailingNewline)
	if !changed || bytes.Equal(data, after) {
		return "", nil
	}
	if existed && takeBackup {
		perm := os.FileMode(0o600)
		if fi, err := os.Stat(path); err == nil {
			perm = fi.Mode().Perm()
		}
		backup = path + ".agent-brain-backup-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.WriteFile(backup, data, perm); err != nil {
			return "", err
		}
	}
	if err := writeAtomic(path, after, existed); err != nil {
		return backup, err
	}
	return backup, nil
}

// writeAtomic installs content via temp file + rename so a reader never sees a
// partial write, preserving an existing file's permission bits (config.toml can
// hold credentials and must not be widened) and creating a new file 0600.
func writeAtomic(path string, content []byte, existed bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o600)
	if existed {
		if fi, err := os.Stat(path); err == nil {
			perm = fi.Mode().Perm()
		}
	}
	// A unique temp name per write: a fixed one lets two concurrent installs
	// clobber each other's half-written file, and leaves a stale one sitting
	// beside the user's config when a rename fails.
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".agent-brain-tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// Cleans up every path out of here except the successful rename, where the
	// file is already gone and the error is the expected one.
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
