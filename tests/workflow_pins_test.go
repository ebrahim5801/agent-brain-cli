package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var goVersionPin = regexp.MustCompile(`go-version:\s*'([^']+)'`)

// govulncheck reports the standard library of whatever toolchain the runner
// resolves. A floating pin like '1.25' silently keeps a cached patch release
// and starts failing the moment a stdlib advisory lands, so require an exact
// patch version in every workflow.
func TestWorkflowsPinExactGoPatchVersion(t *testing.T) {
	files, err := filepath.Glob("../.github/workflows/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no workflow files found")
	}
	exact := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		matches := goVersionPin.FindAllStringSubmatch(string(body), -1)
		if len(matches) == 0 {
			continue
		}
		for _, m := range matches {
			if !exact.MatchString(m[1]) {
				t.Errorf("%s: go-version %q is not an exact patch version", filepath.Base(f), m[1])
			}
		}
	}
}
