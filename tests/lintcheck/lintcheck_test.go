package lintcheck

import (
	"os/exec"
	"strings"
	"testing"
)

// T028: the depguard no-network rule must fail the lint run when a net import
// exists outside tests. violation.go (build-tagged) is the fixture.
func TestNoNetworkGateBites(t *testing.T) {
	bin, err := exec.LookPath("golangci-lint")
	if err != nil {
		t.Skip("golangci-lint not installed")
	}
	out, err := exec.Command(bin, "run", "--build-tags", "lintcheck", "./...").CombinedOutput()
	if err == nil {
		t.Fatalf("lint passed but violation.go imports net/http — the gate does not bite:\n%s", out)
	}
	if !strings.Contains(string(out), "depguard") {
		t.Fatalf("lint failed but not via depguard:\n%s", out)
	}
}
