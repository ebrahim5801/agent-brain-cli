// Package settingsfile owns the JSON settings-editing mechanics every adapter
// shares: load (refuse-on-unparseable), atomic write (temp + rename), and
// backup-before-change with an idempotence guard so a no-op re-install writes
// nothing and creates no backup (FR-006/007/008). Extracting it here means the
// four adapters inherit the safety obligations from one audited implementation
// instead of four copies.
package settingsfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Load reads a JSON-object settings file. A missing file yields an empty root
// and existed=false. A present file that fails to parse, or whose top level is
// not a JSON object, is returned as an error with the file left byte-identical
// (refuse-on-unparseable — the caller aborts that adapter without touching it).
func Load(path string) (root map[string]any, existed bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	root = map[string]any{}
	if len(strings.TrimSpace(string(data))) > 0 {
		dec := json.NewDecoder(bytes.NewReader(data))
		// Decode numbers as json.Number, not float64, so a >2^53 integer in the
		// user's config (e.g. a 64-bit id) survives a round-trip byte-exact
		// instead of being silently rounded on write (FR-008).
		dec.UseNumber()
		if err := dec.Decode(&root); err != nil {
			return nil, true, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return root, true, nil
}

func marshal(root map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// WriteAtomic serializes root as indented JSON and installs it via a temp file
// + rename, so a reader never observes a partial write. An existing file's
// permission bits are preserved (a settings file may be 0600 to protect tokens;
// the rewrite must not widen it), and a new file is created 0600.
func WriteAtomic(path string, root map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := marshal(root)
	if err != nil {
		return err
	}
	perm := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	tmp := path + ".agent-brain-tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WriteIfChanged applies mutate and writes only when the normalized content
// actually changes — the same idempotence guard as BackupIfChanged but without
// taking a backup. It is the in-place writer for paths whose restore point is
// the pre-integration backup Install already took (Uninstall, MCP registration):
// a no-op call (nothing of ours to remove, entry already present and identical)
// leaves the user's file byte-for-byte untouched (FR-006/008/009).
func WriteIfChanged(path string, mutate func(root map[string]any) (bool, error)) error {
	root, _, err := Load(path)
	if err != nil {
		return err
	}
	before, err := marshal(root)
	if err != nil {
		return err
	}
	changed, err := mutate(root)
	if err != nil {
		return err
	}
	after, err := marshal(root)
	if err != nil {
		return err
	}
	if !changed || bytes.Equal(before, after) {
		return nil
	}
	return WriteAtomic(path, root)
}

// BackupIfChanged loads path, applies mutate, and writes the result only when
// the normalized content actually changes. mutate reports whether it altered
// root; the write is additionally guarded by a normalized before/after byte
// comparison, so a re-run that yields identical content writes nothing and
// creates no backup (idempotence-before-write, FR-006/007). When a write does
// happen and the file already existed, its original bytes are copied to
// <path>.agent-brain-backup-<UTC yyyymmddThhmmssZ> first.
func BackupIfChanged(path string, mutate func(root map[string]any) (bool, error)) (backup string, err error) {
	root, existed, err := Load(path)
	if err != nil {
		return "", err
	}
	before, err := marshal(root)
	if err != nil {
		return "", err
	}
	changed, err := mutate(root)
	if err != nil {
		return "", err
	}
	after, err := marshal(root)
	if err != nil {
		return "", err
	}
	if !changed || bytes.Equal(before, after) {
		return "", nil
	}
	if existed {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		// Mirror the source file's mode onto the backup. A settings file may be
		// 0600 to protect a token; a hardcoded 0644 backup would leak that content
		// to every local user. Default to 0600 when the mode can't be read.
		perm := os.FileMode(0o600)
		if fi, err := os.Stat(path); err == nil {
			perm = fi.Mode().Perm()
		}
		backup = path + ".agent-brain-backup-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.WriteFile(backup, raw, perm); err != nil {
			return "", err
		}
	}
	if err := WriteAtomic(path, root); err != nil {
		return backup, err
	}
	return backup, nil
}
