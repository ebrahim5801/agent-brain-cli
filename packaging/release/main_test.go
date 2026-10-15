package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/release"
)

var testEntries = []entry{
	{name: "agent-brain", mode: 0o755, data: []byte("binary")},
	{name: "LICENSE", mode: 0o644, data: []byte("license")},
	{name: "README.md", mode: 0o644, data: []byte("readme")},
}

var testTime = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func TestArchivesAreReproducible(t *testing.T) {
	for name, write := range map[string]func(io.Writer, []entry, time.Time) error{
		"tar.gz": writeTarGz,
		"zip":    writeZip,
	} {
		var a, b bytes.Buffer
		if err := write(&a, testEntries, testTime); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := write(&b, testEntries, testTime); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(a.Bytes(), b.Bytes()) {
			t.Errorf("%s: two builds of the same input differ", name)
		}
	}
}

// The installers extract the binary by its exact top-level name, so it must
// not sit inside a folder.
func TestTarGzLayout(t *testing.T) {
	var buf bytes.Buffer
	if err := writeTarGz(&buf, testEntries, testTime); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var i int
	for ; ; i++ {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		want := testEntries[i]
		if hdr.Name != want.name || hdr.Mode != want.mode || !hdr.ModTime.Equal(testTime) || hdr.Uid != 0 || hdr.Gid != 0 {
			t.Errorf("entry %d = %s mode %o time %v uid %d; want %s mode %o", i, hdr.Name, hdr.Mode, hdr.ModTime, hdr.Uid, want.name, want.mode)
		}
		data, _ := io.ReadAll(tr)
		if !bytes.Equal(data, want.data) {
			t.Errorf("entry %s has the wrong content", hdr.Name)
		}
	}
	if i != len(testEntries) {
		t.Errorf("archive has %d entries, want %d", i, len(testEntries))
	}
}

func TestZipLayout(t *testing.T) {
	var buf bytes.Buffer
	if err := writeZip(&buf, testEntries, testTime); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(testEntries) {
		t.Fatalf("archive has %d entries, want %d", len(zr.File), len(testEntries))
	}
	for i, f := range zr.File {
		if f.Name != testEntries[i].name {
			t.Errorf("entry %d = %s, want %s", i, f.Name, testEntries[i].name)
		}
	}
}

func TestChecksumsFormat(t *testing.T) {
	got := string(checksums([]release.Asset{
		{Filename: "agent-brain_linux_amd64.tar.gz", SHA256: "aa"},
		{Filename: "agent-brain_windows_amd64.zip", SHA256: "bb"},
	}))
	want := "aa  agent-brain_linux_amd64.tar.gz\nbb  agent-brain_windows_amd64.zip\n"
	if got != want {
		t.Errorf("checksums = %q, want %q", got, want)
	}
}

func TestCheckOutRefusesUnrelatedFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkOut(dir); err == nil {
		t.Fatal("checkOut accepted a folder that is not a release build")
	}
	file := filepath.Join(dir, "notes.txt")
	if err := checkOut(file); err == nil {
		t.Fatal("checkOut accepted a file")
	}
}

func TestCheckOutAcceptsMissingEmptyAndEarlierBuild(t *testing.T) {
	root := t.TempDir()
	if err := checkOut(filepath.Join(root, "missing")); err != nil {
		t.Errorf("missing folder: %v", err)
	}
	if err := checkOut(root); err != nil {
		t.Errorf("empty folder: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "releases", "latest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkOut(root); err != nil {
		t.Errorf("earlier build: %v", err)
	}
}

// A build that failed before the manifest was written must not leave a folder
// the next build refuses to replace: the build stages elsewhere, so out is
// untouched until the final move.
func TestFailedBuildLeavesOutUntouched(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	repo := t.TempDir()
	files := map[string]string{
		"go.mod":                  "module example.com/broken\n\ngo 1.25\n",
		"cmd/agent-brain/main.go": "package main\n\nfunc main() { this does not compile }\n",
		"site/index.html":         "<!doctype html>\n",
		"LICENSE":                 "license\n",
		"NOTICE":                  "notice\n",
		"README.md":               "readme\n",
		"CHANGELOG.md":            "changelog\n",
	}
	for name, body := range files {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "broken")
	t.Chdir(repo)

	out := filepath.Join(repo, "dist", "site")
	if err := run("0.1.0", out, false, "https://releases.example"); err == nil {
		t.Fatal("run succeeded although the binary does not compile")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a failed build created %s (err %v)", out, err)
	}
	entries, err := os.ReadDir(filepath.Join(repo, "dist"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("a failed build left %s in dist/", e.Name())
	}
}

func TestVersionPattern(t *testing.T) {
	for v, ok := range map[string]bool{
		"0.1.0": true, "1.2.3-rc.1": true,
		"v0.1.0": false, "0.1": false, "0.1.0/../x": false, "": false,
	} {
		if versionRE.MatchString(v) != ok {
			t.Errorf("versionRE.MatchString(%q) = %v, want %v", v, !ok, ok)
		}
	}
}

func TestRunRejectsANonHTTPSUpdateURL(t *testing.T) {
	for _, bad := range []string{
		"http://releases.example", "releases.example", "https://releases.example/x y",
		"https://trusted.example\t-X\tgithub.com/ebrahim5801/agent-brain-cli/internal/config.DefaultUpdateURL=https://evil.example",
		"https://user@releases.example", "https://releases.example/?a=b", "https://",
	} {
		err := run("0.1.0", t.TempDir(), true, bad)
		if err == nil || !strings.Contains(err.Error(), "-update-url") {
			t.Errorf("run with -update-url %q: err = %v, want an -update-url error", bad, err)
		}
	}
}

func TestCheckUpdateURLAcceptsPlainHTTPS(t *testing.T) {
	for _, good := range []string{"", "https://agentbrain.wecansync.com", "https://releases.example/agent-brain"} {
		if err := checkUpdateURL(good); err != nil {
			t.Errorf("checkUpdateURL(%q) = %v", good, err)
		}
	}
}
