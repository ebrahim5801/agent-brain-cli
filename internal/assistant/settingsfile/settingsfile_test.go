package settingsfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRefusesUnparseableLeavingFileByteIdentical(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{broken json`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected parse error on unparseable file")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Errorf("file mutated on refusal: %q", after)
	}
}

func TestBackupIfChangedNoOpMutateWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	seed := []byte(`{"theme":"dark"}`)
	if err := os.WriteFile(path, seed, 0o644); err != nil {
		t.Fatal(err)
	}

	backup, err := BackupIfChanged(path, func(root map[string]any) (bool, error) {
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Errorf("no-op mutate created a backup: %q", backup)
	}

	after, _ := os.ReadFile(path)
	if string(after) != string(seed) {
		t.Errorf("no-op mutate rewrote the file: %q", after)
	}
	if entries, _ := filepath.Glob(path + ".agent-brain-backup-*"); len(entries) != 0 {
		t.Errorf("no-op mutate left backups: %v", entries)
	}
}

func TestBackupIfChangedIdempotentContentWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	mutate := func(root map[string]any) (bool, error) {
		root["added"] = "value"
		return true, nil
	}

	// First run: file is created, no backup (did not exist).
	backup, err := BackupIfChanged(path, mutate)
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Errorf("backup created for a non-existent file: %q", backup)
	}
	first, _ := os.ReadFile(path)

	// Second run: mutate claims changed, but the normalized content is
	// identical, so nothing is written and no backup churns.
	backup, err = BackupIfChanged(path, mutate)
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Errorf("idempotent re-run created a backup: %q", backup)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Errorf("idempotent re-run rewrote the file")
	}
	if entries, _ := filepath.Glob(path + ".agent-brain-backup-*"); len(entries) != 0 {
		t.Errorf("idempotent re-run left backups: %v", entries)
	}
}

func TestBackupIfChangedPreservesForeignKeysAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	seed := []byte(`{"theme":"dark","nested":{"a":1}}`)
	if err := os.WriteFile(path, seed, 0o644); err != nil {
		t.Fatal(err)
	}

	backup, err := BackupIfChanged(path, func(root map[string]any) (bool, error) {
		root["ours"] = "x"
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" {
		t.Fatal("expected a backup for a changed existing file")
	}

	root, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if root["theme"] != "dark" {
		t.Error("foreign scalar key lost")
	}
	nested, ok := root["nested"].(map[string]any)
	if !ok || fmt.Sprint(nested["a"]) != "1" {
		t.Errorf("foreign nested key lost: %v", root["nested"])
	}
	if root["ours"] != "x" {
		t.Error("mutation not applied")
	}

	// The backup holds the original bytes verbatim.
	raw, _ := os.ReadFile(backup)
	if string(raw) != string(seed) {
		t.Errorf("backup does not hold original bytes: %q", raw)
	}
}

func TestWriteAtomicPreservesLargeIntegersAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// A 64-bit id above 2^53 would round if decoded as float64.
	seed := []byte(`{"bigId":9223372036854775807,"ours":"keep"}`)
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteIfChanged(path, func(root map[string]any) (bool, error) {
		root["added"] = "x"
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "9223372036854775807") {
		t.Errorf("large integer was corrupted on write: %s", raw)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions widened from 0600 to %o", perm)
	}
}

func TestWriteIfChangedNoOpLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// Deliberately unusual formatting; a no-op write must not reformat it.
	seed := []byte("{\n\t\"theme\":   \"dark\"\n}\n")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// mutate reports no change: nothing of ours to remove.
	if err := WriteIfChanged(path, func(root map[string]any) (bool, error) {
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	if string(raw) != string(seed) {
		t.Errorf("no-op write reformatted the file:\nwant %q\ngot  %q", seed, raw)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("no-op write touched the file (mtime changed)")
	}
}

func TestBackupPreservesRestrictiveMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// A 0600 settings file that holds a token; its backup must not be wider.
	if err := os.WriteFile(path, []byte(`{"token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := BackupIfChanged(path, func(root map[string]any) (bool, error) {
		root["added"] = true
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" {
		t.Fatal("expected a backup to be created")
	}
	fi, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("backup mode = %o, want 0600 (must mirror the 0600 source, not widen it)", perm)
	}
}
