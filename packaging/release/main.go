// Command release builds the agent-brain release archives for every supported
// platform and lays them out, together with the static site, as the docroot
// served at https://agentbrain.wecansync.com:
//
//	go run ./packaging/release -version 0.1.0
//
// Output (default dist/site):
//
//	index.html, docs/, assets/, install.sh, install.ps1, ...   copied from site/
//	releases/latest.json                                        release manifest
//	releases/<version>/agent-brain_<os>_<arch>.tar.gz|.zip      archives
//	releases/<version>/checksums.txt                            sha256sum format
//	releases/<version>/manifest.json                            same as latest.json
//
// The manifest is release.Manifest, the format the update server serves at
// /v1/cli/latest, so releases/<version>/ can also be copied as-is into the
// server's release directory.
//
// The archives match the GoReleaser config (.goreleaser.yaml) in name, layout
// and build flags, and are reproducible: entries are in a fixed order, owned by
// root, and stamped with the commit time, so the same commit built with the
// same Go version gives byte-identical archives.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ebrahim5801/agent-brain-cli/release"
)

const (
	projectName = "agent-brain"
	versionVar  = "github.com/ebrahim5801/agent-brain-cli/internal/version.Version"
	updateVar   = "github.com/ebrahim5801/agent-brain-cli/internal/config.DefaultUpdateURL"
)

// targets are the platforms the installers and the self-updater support.
var targets = []struct{ OS, Arch string }{
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"linux", "amd64"}, {"linux", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

// extraFiles ship next to the binary in every archive, as GoReleaser does.
var extraFiles = []string{"LICENSE", "NOTICE", "README.md", "CHANGELOG.md"}

var versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

func main() {
	version := flag.String("version", "", "release version, for example 0.1.0 (required)")
	out := flag.String("out", filepath.Join("dist", "site"), "output docroot; replaced if it holds an earlier build")
	allowDirty := flag.Bool("allow-dirty", false, "build even if the git working tree has uncommitted changes")
	updateURL := flag.String("update-url", "https://agentbrain.wecansync.com", "https site the binaries update from (the docroot this builds); empty updates from the cloud server")
	flag.Parse()
	if err := run(strings.TrimPrefix(*version, "v"), *out, *allowDirty, *updateURL); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func run(version, out string, allowDirty bool, updateURL string) error {
	if !versionRE.MatchString(version) {
		return fmt.Errorf("-version must look like 0.1.0, got %q", version)
	}
	if err := checkUpdateURL(updateURL); err != nil {
		return err
	}
	for _, p := range append([]string{"go.mod", filepath.Join("cmd", projectName), "site"}, extraFiles...) {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("run this from the repository root (%s: %w)", p, err)
		}
	}

	if dirty, err := gitOutput("status", "--porcelain"); err != nil {
		return err
	} else if dirty != "" && !allowDirty {
		return errors.New("the working tree has uncommitted changes; commit them or pass -allow-dirty")
	}
	commit, err := gitOutput("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	stamp, err := gitOutput("log", "-1", "--format=%ct")
	if err != nil {
		return err
	}
	secs, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return fmt.Errorf("reading the commit time: %w", err)
	}
	mtime := time.Unix(secs, 0).UTC()

	if err := checkOut(out); err != nil {
		return err
	}
	// Build into a sibling folder and move it into place only on success, so a
	// failed build never leaves a partial docroot behind.
	if err := os.MkdirAll(filepath.Dir(filepath.Clean(out)), 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(filepath.Clean(out)), "."+filepath.Base(filepath.Clean(out))+".build-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o755); err != nil {
		return err
	}
	relDir := filepath.Join(stage, "releases", version)
	if err := os.MkdirAll(relDir, 0o755); err != nil {
		return err
	}
	if err := copySite("site", stage); err != nil {
		return err
	}

	build, err := os.MkdirTemp("", "agent-brain-release-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(build)

	extras := make([]entry, 0, len(extraFiles))
	for _, name := range extraFiles {
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		extras = append(extras, entry{name: name, mode: 0o644, data: data})
	}

	manifest := release.Manifest{Version: version, PublishedAt: mtime.Format(time.RFC3339)}
	var hostBinary string
	for _, t := range targets {
		bin := projectName
		if t.OS == "windows" {
			bin += ".exe"
		}
		binPath := filepath.Join(build, t.OS+"_"+t.Arch, bin)
		fmt.Fprintf(os.Stderr, "building %s/%s\n", t.OS, t.Arch)
		if err := goBuild(t.OS, t.Arch, version, updateURL, binPath); err != nil {
			return err
		}
		if t.OS == runtime.GOOS && t.Arch == runtime.GOARCH {
			hostBinary = binPath
		}
		data, err := os.ReadFile(binPath)
		if err != nil {
			return err
		}
		entries := append([]entry{{name: bin, mode: 0o755, data: data}}, extras...)

		var archive bytes.Buffer
		filename := fmt.Sprintf("%s_%s_%s", projectName, t.OS, t.Arch)
		if t.OS == "windows" {
			filename += ".zip"
			err = writeZip(&archive, entries, mtime)
		} else {
			filename += ".tar.gz"
			err = writeTarGz(&archive, entries, mtime)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", filename, err)
		}
		if err := os.WriteFile(filepath.Join(relDir, filename), archive.Bytes(), 0o644); err != nil {
			return err
		}
		sum := sha256.Sum256(archive.Bytes())
		manifest.Assets = append(manifest.Assets, release.Asset{
			OS: t.OS, Arch: t.Arch, Filename: filename,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(archive.Len()),
		})
	}
	sort.Slice(manifest.Assets, func(i, j int) bool { return manifest.Assets[i].Filename < manifest.Assets[j].Filename })

	if hostBinary != "" {
		if err := smokeTest(hostBinary, version); err != nil {
			return err
		}
	}

	if err := os.WriteFile(filepath.Join(relDir, "checksums.txt"), checksums(manifest.Assets), 0o644); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(relDir, "manifest.json"), data, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "releases", "latest.json"), data, 0o644); err != nil {
		return err
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.Rename(stage, out); err != nil {
		return err
	}
	relDir = filepath.Join(out, "releases", version)

	fmt.Printf("agent-brain %s from commit %s\n", version, commit[:12])
	for _, a := range manifest.Assets {
		fmt.Printf("  %-36s %10d  %s\n", a.Filename, a.Size, a.SHA256)
	}
	fmt.Printf("docroot: %s\n", out)
	fmt.Printf("update-server files: %s/{manifest.json,*.tar.gz,*.zip}\n", relDir)
	return nil
}

func gitOutput(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// checkOut reports whether out may be replaced by the new build: it must be
// missing, empty, or an earlier build (it holds releases/latest.json), so a
// mistyped -out never deletes unrelated files.
func checkOut(out string) error {
	info, err := os.Lstat(out)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists and is not a folder; choose another -out", out)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(out, "releases", "latest.json")); err != nil {
		return fmt.Errorf("%s is not empty and is not an earlier release build; choose another -out", out)
	}
	return nil
}

// copySite copies the static site into the docroot, skipping any releases/
// folder a local test may have left in site/.
func copySite(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == "releases" && d.IsDir() {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0o644)
		default:
			return fmt.Errorf("site/%s is not a regular file; the site must not contain links", filepath.ToSlash(rel))
		}
	})
}

// checkUpdateURL accepts an empty value (update from the cloud server) or a
// plain https URL. The value becomes one -X assignment inside -ldflags, which
// the linker splits on whitespace and quotes, so those are refused outright.
func checkUpdateURL(value string) error {
	if value == "" {
		return nil
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '\'' || r == '"' || r == '\\'
	}) >= 0 {
		return fmt.Errorf("-update-url must not contain spaces, control characters, quotes or backslashes, got %q", value)
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("-update-url must be an https URL without credentials, query or fragment, got %q", value)
	}
	return nil
}

func goBuild(goos, goarch, version, updateURL, output string) error {
	ldflags := "-s -w -X " + versionVar + "=" + version
	if updateURL != "" {
		ldflags += " -X " + updateVar + "=" + strings.TrimRight(updateURL, "/")
	}
	cmd := exec.Command("go", "build",
		"-trimpath", "-buildvcs=false",
		"-ldflags", ldflags,
		"-o", output, "./cmd/"+projectName)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build %s/%s: %w", goos, goarch, err)
	}
	return nil
}

// smokeTest runs the binary built for this machine, as the self-updater does
// before swapping a binary in, and checks the version was stamped.
func smokeTest(binary, version string) error {
	out, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s --version: %w\n%s", binary, err, out)
	}
	if !strings.Contains(string(out), projectName) || !strings.Contains(string(out), version) {
		return fmt.Errorf("%s --version printed %q; expected the name and version %s", binary, strings.TrimSpace(string(out)), version)
	}
	return nil
}

type entry struct {
	name string
	mode int64
	data []byte
}

func writeTarGz(w io.Writer, entries []entry, mtime time.Time) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     e.name,
			Mode:     e.mode,
			Size:     int64(len(e.data)),
			ModTime:  mtime,
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(e.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func writeZip(w io.Writer, entries []entry, mtime time.Time) error {
	zw := zip.NewWriter(w)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: mtime}
		hdr.SetMode(fs.FileMode(e.mode))
		f, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := f.Write(e.data); err != nil {
			return err
		}
	}
	return zw.Close()
}

// checksums renders assets in the sha256sum format GoReleaser uses.
func checksums(assets []release.Asset) []byte {
	var b bytes.Buffer
	for _, a := range assets {
		fmt.Fprintf(&b, "%s  %s\n", a.SHA256, a.Filename)
	}
	return b.Bytes()
}
