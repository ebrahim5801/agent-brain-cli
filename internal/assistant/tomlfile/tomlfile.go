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

// splitLines splits content into lines, remembering whether CRLF was used so a
// write can reproduce the file's existing line ending rather than converting it.
func splitLines(data []byte) (lines []string, crlf bool, trailingNewline bool) {
	s := string(data)
	if strings.Contains(s, "\r\n") {
		crlf = true
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	if s == "" {
		return nil, crlf, false
	}
	trailingNewline = strings.HasSuffix(s, "\n")
	if trailingNewline {
		s = strings.TrimSuffix(s, "\n")
	}
	return strings.Split(s, "\n"), crlf, trailingNewline
}

func joinLines(lines []string, crlf, trailingNewline bool) []byte {
	s := strings.Join(lines, "\n")
	if trailingNewline && s != "" {
		s += "\n"
	}
	if crlf {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	return []byte(s)
}

// findTable locates the sole occurrence of header, returning the index of its
// header line and of the first line past the table. A table runs to the next
// table header or EOF. A duplicate header is an error: with two candidates
// there is no way to tell which one is live, and guessing risks deleting the
// user's.
func findTable(lines []string, header string) (start, end int, found bool, err error) {
	want := headerLine(header)
	start = -1
	for i, l := range lines {
		if strings.TrimSpace(l) != want {
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
	return edit(path, true, func(lines []string) ([]string, bool, error) {
		start, end, found, err := findTable(lines, table.Header)
		if err != nil {
			return nil, false, err
		}
		want := strings.Split(strings.TrimSuffix(table.Render(), "\n"), "\n")
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
			out = append(out, "")
		}
		return append(out, want...), true, nil
	})
}

// Remove deletes the named table and the key lines belonging to it, leaving
// every neighbouring table byte-identical. A file without the table, or no file
// at all, is a success no-op. No backup is written: the pre-integration backup
// taken at install time is the restore path, matching settingsfile.WriteIfChanged.
func Remove(path string, header string) error {
	_, err := edit(path, false, func(lines []string) ([]string, bool, error) {
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
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lines, _, _ := splitLines(data)
	_, _, found, err := findTable(lines, header)
	if err != nil {
		return false, err
	}
	return found, nil
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
		if got[i] != want[i] {
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
func edit(path string, takeBackup bool, mutate func(lines []string) ([]string, bool, error)) (backup string, err error) {
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
	out, changed, err := mutate(lines)
	if err != nil {
		return "", err
	}
	after := joinLines(out, crlf, trailingNewline)
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
	tmp := path + ".agent-brain-tmp"
	if err := os.WriteFile(tmp, content, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
