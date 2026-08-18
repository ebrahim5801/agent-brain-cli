package tests

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The extraction from the private monorepo rewrote the module path across .go
// files but missed config that names it as a plain string — .golangci.yml's
// errcheck exclusion silently stopped matching, which surfaced only as CI lint
// noise. Fail loudly if any stale reference comes back.
func TestNoStaleModulePathReferences(t *testing.T) {
	stale := "gitlab.com/" + "ebrahim580/agent-brain"
	self, err := filepath.Abs("module_path_test.go")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if abs == self {
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".yml", ".yaml", ".md", ".mod", ".sum", ".json":
		default:
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, stale) {
				t.Errorf("%s:%d references the private module path", path, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
