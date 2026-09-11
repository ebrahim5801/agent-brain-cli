package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var (
	goVersionPin = regexp.MustCompile(`go-version:\s*'([^']+)'`)
	toolRef      = regexp.MustCompile(`go (?:run|install) (\S+)@(\S+)`)
)

func workflows(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../.github/workflows/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no workflow files found")
	}
	return files
}

// govulncheck reports the standard library of whatever toolchain the runner
// resolves. A floating pin like '1.25' silently keeps a cached patch release
// and starts failing the moment a stdlib advisory lands, so require an exact
// patch version in every workflow.
func TestWorkflowsPinExactGoPatchVersion(t *testing.T) {
	exact := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	for _, f := range workflows(t) {
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

// Tools the workflows fetch must name an exact version too. `govulncheck@latest`
// broke the private mirror's pipeline outright the day x/vuln v1.8.0 raised its
// go directive past the pinned toolchain; here the same drift is quieter and
// worse, since the runner will happily download a different Go to satisfy it and
// scan a standard library this project never ships.
func TestWorkflowsPinExactToolVersions(t *testing.T) {
	exact := regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	for _, f := range workflows(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range toolRef.FindAllStringSubmatch(string(body), -1) {
			if !exact.MatchString(m[2]) {
				t.Errorf("%s: %s is fetched as @%s; pin an exact version", filepath.Base(f), m[1], m[2])
			}
		}
	}
}
