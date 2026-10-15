package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ebrahim5801/agent-brain-cli/release"
)

// fakeBinaryScript is a shell script that stands in for the real binary so
// sanityCheck's `--version` succeeds. Non-windows only.
const fakeBinaryScript = "#!/bin/sh\necho \"agent-brain version test\"\n"

func makeTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// releaseServer serves a manifest and one archive under the given filename.
func releaseServer(t *testing.T, filename string, archive []byte, sha string) *httptest.Server {
	t.Helper()
	m := release.Manifest{
		Version: "1.4.0",
		Assets: []release.Asset{
			{OS: runtime.GOOS, Arch: runtime.GOARCH, Filename: filename, SHA256: sha, Size: int64(len(archive))},
		},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/cli/latest"):
			_ = json.NewEncoder(w).Encode(m)
		case r.URL.Path == "/v1/cli/download/"+filename:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestCheckReportsNewer(t *testing.T) {
	archive := makeTarGz(t, "agent-brain", []byte("x"))
	srv := releaseServer(t, "agent-brain_x.tar.gz", archive, sha256hex(archive))
	defer srv.Close()

	res, err := Check(context.Background(), srv.Client(), Source{Base: srv.URL}, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Newer || res.Latest != "1.4.0" || !res.HasAsset {
		t.Fatalf("res = %+v", res)
	}

	same, err := Check(context.Background(), srv.Client(), Source{Base: srv.URL}, "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if same.Newer {
		t.Fatalf("1.4.0 vs 1.4.0 should not be newer")
	}

	dev, err := Check(context.Background(), srv.Client(), Source{Base: srv.URL}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Newer {
		t.Fatalf("dev build must not report newer")
	}
}

func TestApplyEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary is a POSIX shell script")
	}
	content := []byte(fakeBinaryScript)
	filename := "agent-brain_test.tar.gz"
	archive := makeTarGz(t, binaryName(), content)
	srv := releaseServer(t, filename, archive, sha256hex(archive))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "agent-brain")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	swapExecutable(t, target)

	asset := release.Asset{OS: runtime.GOOS, Arch: runtime.GOARCH, Filename: filename, SHA256: sha256hex(archive)}
	got, err := Apply(context.Background(), srv.Client(), Source{Base: srv.URL}, Result{Latest: "1.4.0", Asset: asset})
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("target = %q, want %q", got, target)
	}
	replaced, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(replaced) != fakeBinaryScript {
		t.Fatalf("target not replaced: %q", string(replaced))
	}
	// No stray temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestApplyZipArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary is a POSIX shell script")
	}
	content := []byte(fakeBinaryScript)
	filename := "agent-brain_test.zip"
	archive := makeZip(t, binaryName(), content)
	srv := releaseServer(t, filename, archive, sha256hex(archive))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "agent-brain")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	swapExecutable(t, target)

	asset := release.Asset{OS: runtime.GOOS, Arch: runtime.GOARCH, Filename: filename, SHA256: sha256hex(archive)}
	if _, err := Apply(context.Background(), srv.Client(), Source{Base: srv.URL}, Result{Latest: "1.4.0", Asset: asset}); err != nil {
		t.Fatal(err)
	}
	replaced, _ := os.ReadFile(target)
	if string(replaced) != fakeBinaryScript {
		t.Fatalf("zip target not replaced: %q", string(replaced))
	}
}

func TestApplyChecksumMismatch(t *testing.T) {
	archive := makeTarGz(t, binaryName(), []byte(fakeBinaryScript))
	filename := "agent-brain_test.tar.gz"
	srv := releaseServer(t, filename, archive, "deadbeef")
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "agent-brain")
	_ = os.WriteFile(target, []byte("OLD"), 0o755)
	swapExecutable(t, target)

	asset := release.Asset{OS: runtime.GOOS, Arch: runtime.GOARCH, Filename: filename, SHA256: "deadbeef"}
	_, err := Apply(context.Background(), srv.Client(), Source{Base: srv.URL}, Result{Latest: "1.4.0", Asset: asset})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	// The original binary must be untouched.
	if b, _ := os.ReadFile(target); string(b) != "OLD" {
		t.Fatalf("target modified after mismatch: %q", string(b))
	}
}

func TestApplySymlinkResolved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary is a POSIX shell script")
	}
	content := []byte(fakeBinaryScript)
	filename := "agent-brain_test.tar.gz"
	archive := makeTarGz(t, binaryName(), content)
	srv := releaseServer(t, filename, archive, sha256hex(archive))
	defer srv.Close()

	dir := t.TempDir()
	realTarget := filepath.Join(dir, "agent-brain-real")
	if err := os.WriteFile(realTarget, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "agent-brain")
	if err := os.Symlink(realTarget, link); err != nil {
		t.Fatal(err)
	}
	swapExecutable(t, link)

	asset := release.Asset{OS: runtime.GOOS, Arch: runtime.GOARCH, Filename: filename, SHA256: sha256hex(archive)}
	got, err := Apply(context.Background(), srv.Client(), Source{Base: srv.URL}, Result{Latest: "1.4.0", Asset: asset})
	if err != nil {
		t.Fatal(err)
	}
	// The real file behind the symlink is replaced; the symlink still points at it.
	if got != realTarget {
		t.Fatalf("target = %q, want real target %q", got, realTarget)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was clobbered")
	}
	if b, _ := os.ReadFile(realTarget); string(b) != fakeBinaryScript {
		t.Fatalf("real target not replaced: %q", string(b))
	}
}

func TestApplyEACCESManualInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission model differs on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	content := []byte(fakeBinaryScript)
	filename := "agent-brain_test.tar.gz"
	archive := makeTarGz(t, binaryName(), content)
	srv := releaseServer(t, filename, archive, sha256hex(archive))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "agent-brain")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Make the directory read+execute but not writable: rename into it fails EACCES.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	swapExecutable(t, target)

	asset := release.Asset{OS: runtime.GOOS, Arch: runtime.GOARCH, Filename: filename, SHA256: sha256hex(archive)}
	_, err := Apply(context.Background(), srv.Client(), Source{Base: srv.URL}, Result{Latest: "1.4.0", Asset: asset})
	var manual *ErrManualInstall
	if err == nil {
		t.Fatal("expected ErrManualInstall")
	}
	if !errors.As(err, &manual) {
		t.Fatalf("err = %v, want *ErrManualInstall", err)
	}
	if !strings.Contains(manual.Hint, "sudo install") {
		t.Fatalf("hint = %q", manual.Hint)
	}
}

// swapExecutable points Apply's target resolution at path for the duration of
// the test.
func swapExecutable(t *testing.T, path string) {
	t.Helper()
	prev := osExecutable
	osExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { osExecutable = prev })
}

// staticSite serves a release the way packaging/release lays out a docroot:
// releases/latest.json and releases/<version>/<file>. It records the paths
// asked for so a test can prove the cloud endpoints were never used.
func staticSite(t *testing.T, filename string, archive []byte, sha string, seen *[]string) *httptest.Server {
	t.Helper()
	m := release.Manifest{
		Version: "1.4.0",
		Assets: []release.Asset{
			{OS: runtime.GOOS, Arch: runtime.GOARCH, Filename: filename, SHA256: sha, Size: int64(len(archive))},
		},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.URL.Path)
		switch r.URL.Path {
		case "/releases/latest.json":
			_ = json.NewEncoder(w).Encode(m)
		case "/releases/1.4.0/" + filename:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestStaticSourceCheckAndApply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary is a POSIX shell script")
	}
	filename := "agent-brain_test.tar.gz"
	archive := makeTarGz(t, binaryName(), []byte(fakeBinaryScript))
	var seen []string
	srv := staticSite(t, filename, archive, sha256hex(archive), &seen)
	defer srv.Close()
	src := Source{Base: srv.URL, Static: true}

	res, err := Check(context.Background(), srv.Client(), src, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Newer || res.Latest != "1.4.0" || !res.HasAsset {
		t.Fatalf("res = %+v", res)
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "agent-brain")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	swapExecutable(t, target)
	if _, err := Apply(context.Background(), srv.Client(), src, res); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != fakeBinaryScript {
		t.Fatalf("target not replaced: %q", got)
	}
	for _, path := range seen {
		if strings.HasPrefix(path, "/v1/") {
			t.Fatalf("static source asked the cloud endpoint %s", path)
		}
	}
}

func TestStaticSourceRejectsTamperedArchive(t *testing.T) {
	filename := "agent-brain_test.tar.gz"
	archive := makeTarGz(t, binaryName(), []byte(fakeBinaryScript))
	var seen []string
	srv := staticSite(t, filename, archive, sha256hex([]byte("something else")), &seen)
	defer srv.Close()
	src := Source{Base: srv.URL, Static: true}

	res, err := Check(context.Background(), srv.Client(), src, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "agent-brain")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	swapExecutable(t, target)
	if _, err := Apply(context.Background(), srv.Client(), src, res); err == nil {
		t.Fatal("a checksum mismatch must fail the update")
	}
	if got, _ := os.ReadFile(target); string(got) != "OLD" {
		t.Fatalf("target changed after a failed update: %q", got)
	}
}

func TestSourceURLs(t *testing.T) {
	static := Source{Base: "https://releases.example", Static: true}
	if got := static.latestURL(); got != "https://releases.example/releases/latest.json" {
		t.Fatalf("static latest = %s", got)
	}
	if got := static.downloadURL("0.5.4", "agent-brain_linux_amd64.tar.gz"); got != "https://releases.example/releases/0.5.4/agent-brain_linux_amd64.tar.gz" {
		t.Fatalf("static download = %s", got)
	}
	if got := static.downloadURL("../x", "a/b.zip"); strings.Contains(got, "../") || strings.Contains(got, "a/b") {
		t.Fatalf("static download must escape path parts: %s", got)
	}
	cloud := Source{Base: "https://cloud.example"}
	if got := cloud.downloadURL("0.5.4", "agent-brain_linux_amd64.tar.gz"); got != "https://cloud.example/v1/cli/download/agent-brain_linux_amd64.tar.gz" {
		t.Fatalf("cloud download = %s", got)
	}
}
