package tests

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// The installers build from the clone they sit in, so they must agree with
// go.mod on the module path and must not hardcode a toolchain version that
// silently drifts from the go directive.
func TestInstallScriptsMatchGoMod(t *testing.T) {
	goMod, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	module := regexp.MustCompile(`(?m)^module (\S+)$`).FindStringSubmatch(string(goMod))
	if module == nil {
		t.Fatal("no module directive in go.mod")
	}

	for _, script := range []string{"../install.sh", "../install.ps1"} {
		body, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, module[1]) {
			t.Errorf("%s does not reference the module path %q", script, module[1])
		}
		if !strings.Contains(text, "go.mod") {
			t.Errorf("%s does not read the required Go version from go.mod", script)
		}
		if m := regexp.MustCompile(`Go 1\.\d+`).FindString(text); m != "" {
			t.Errorf("%s hardcodes a toolchain version (%s); derive it from go.mod", script, m)
		}
	}
}

func TestInstallShellScriptIsExecutable(t *testing.T) {
	info, err := os.Stat("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("install.sh is not executable (mode %v)", info.Mode().Perm())
	}
}

func TestInstallShellScriptHelp(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if out, err := exec.Command("bash", "-n", "../install.sh").CombinedOutput(); err != nil {
		t.Fatalf("install.sh has a syntax error: %v\n%s", err, out)
	}

	out, err := exec.Command("bash", "../install.sh", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh --help failed: %v\n%s", err, out)
	}
	help := string(out)
	for _, want := range []string{"--bin-dir", "--system", "--no-integrate", "AGENT_BRAIN_BIN_DIR"} {
		if !strings.Contains(help, want) {
			t.Errorf("install.sh --help does not document %s:\n%s", want, help)
		}
	}
	// The help text was once sliced out of the script with sed, which leaked
	// shell code into the output whenever the header moved.
	if strings.Contains(help, "set -") {
		t.Errorf("install.sh --help leaks script source:\n%s", help)
	}

	out, err = exec.Command("bash", "../install.sh", "--nope").CombinedOutput()
	if err == nil {
		t.Errorf("install.sh accepted an unknown option:\n%s", out)
	}
}

// README.md is where a fresh clone starts; the installer is useless if it is
// not mentioned there.
func TestReadmeDocumentsInstaller(t *testing.T) {
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"install.sh", "install.ps1"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README.md does not mention %s", want)
		}
	}
}
