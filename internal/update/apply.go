package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/release"
)

// osExecutable resolves the running binary's path; a package var so tests can
// point Apply at a throwaway target instead of the test binary itself.
var osExecutable = os.Executable

// binaryName is the executable inside the release archive.
func binaryName() string {
	if runtime.GOOS == "windows" {
		return "agent-brain.exe"
	}
	return "agent-brain"
}

// ErrManualInstall is returned when the target directory is not writable (e.g.
// /usr/local/bin owned by root). The verified new binary is left at TempPath so
// the user can finish with the printed sudo command; the caller prints Hint.
type ErrManualInstall struct {
	TempPath string
	Target   string
	Hint     string
}

func (e *ErrManualInstall) Error() string { return e.Hint }

// asManualInstall is errors.As specialized for *ErrManualInstall.
func asManualInstall(err error, target **ErrManualInstall) bool {
	return errors.As(err, target)
}

// CanReplaceTarget reports whether the running executable's directory is
// writable, i.e. a background self-update could complete without manual steps.
// The auto-update path checks this before spawning so a root-owned install
// (e.g. /usr/local/bin) degrades to a visible notice instead of failing
// silently every day.
func CanReplaceTarget() bool {
	target, err := osExecutable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".agent-brain-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// Apply downloads the asset for this platform, verifies its sha256 against the
// manifest, extracts the binary, sanity-checks it, and atomically replaces the
// running executable in place. It returns the resolved target path on success.
func Apply(ctx context.Context, client *http.Client, serverURL string, asset release.Asset) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	target, err := osExecutable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	// A symlinked install must replace the real target, not the symlink.
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	targetDir := filepath.Dir(target)

	archivePath, err := download(ctx, client, serverURL, asset, targetDir)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(archivePath) }()

	// Prefer extracting next to the target so the final rename is atomic on the
	// same filesystem. If the target dir is not writable (e.g. /usr/local/bin
	// owned by root), fall back to a system temp and report a manual install.
	isZip := strings.HasSuffix(asset.Filename, ".zip")
	extracted, atomic, err := extractBinary(archivePath, targetDir, isZip)
	if err != nil {
		return "", err
	}
	cleanupExtract := true
	defer func() {
		if cleanupExtract {
			if _, statErr := os.Stat(extracted); statErr == nil {
				_ = os.Remove(extracted)
			}
		}
	}()

	if err := os.Chmod(extracted, 0o755); err != nil {
		return "", fmt.Errorf("chmod new binary: %w", err)
	}
	if err := sanityCheck(ctx, extracted); err != nil {
		return "", err
	}

	if !atomic {
		// The verified binary lives in a temp dir; leave it there for the user.
		cleanupExtract = false
		return "", &ErrManualInstall{
			TempPath: extracted,
			Target:   target,
			Hint:     fmt.Sprintf("cannot write %s (permission denied); finish with:\n  sudo install -m 0755 %s %s", target, extracted, target),
		}
	}

	if err := replace(extracted, target); err != nil {
		var manual *ErrManualInstall
		if asManualInstall(err, &manual) {
			cleanupExtract = false
		}
		return "", err
	}
	cleanupExtract = false
	return target, nil
}

// download fetches the archive to a temp file in dir (same filesystem as the
// target so the later rename is atomic) and verifies its sha256.
func download(ctx context.Context, client *http.Client, serverURL string, asset release.Asset, dir string) (string, error) {
	url := fmt.Sprintf("%s/v1/cli/download/%s", serverURL, asset.Filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s failed (%d)", asset.Filename, resp.StatusCode)
	}

	tmp, err := os.CreateTemp(dir, ".agent-brain-dl-*")
	if err != nil {
		// Fall back to the system temp dir if the target dir isn't writable; the
		// archive is deleted either way, only the extracted binary must be on the
		// target filesystem.
		tmp, err = os.CreateTemp("", ".agent-brain-dl-*")
		if err != nil {
			return "", err
		}
	}
	tmpName := tmp.Name()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, asset.SHA256) {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("checksum mismatch: got %s, want %s", got, asset.SHA256)
	}
	return tmpName, nil
}

// extractBinary pulls the agent-brain binary out of the archive and writes it to
// a temp file (agent-brain.new-*), returning its path and whether it landed in
// dir. It prefers dir (so the final rename is atomic on the same filesystem);
// when dir is not writable it falls back to a system temp dir and reports
// atomic=false so the caller can guide a manual install. isZip selects the
// archive format (the downloaded temp path has lost its extension).
func extractBinary(archivePath, dir string, isZip bool) (path string, atomic bool, err error) {
	out, err := os.CreateTemp(dir, "agent-brain.new-*")
	atomic = true
	if err != nil {
		// Target dir not writable: extract into a system temp instead.
		out, err = os.CreateTemp("", "agent-brain.new-*")
		atomic = false
		if err != nil {
			return "", false, fmt.Errorf("create temp binary: %w", err)
		}
	}
	outName := out.Name()
	fail := func(cause error) (string, bool, error) {
		out.Close()
		_ = os.Remove(outName)
		return "", false, cause
	}

	if isZip {
		if err := copyFromZip(archivePath, out); err != nil {
			return fail(err)
		}
	} else {
		if err := copyFromTarGz(archivePath, out); err != nil {
			return fail(err)
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(outName)
		return "", false, err
	}
	return outName, atomic, nil
}

func copyFromTarGz(archivePath string, out io.Writer) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	want := binaryName()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) == want && hdr.Typeflag == tar.TypeReg {
			if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // archive is sha256-verified before extraction
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("%s not found in archive", want)
}

func copyFromZip(archivePath string, out io.Writer) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	want := binaryName()
	for _, zf := range zr.File {
		if filepath.Base(zf.Name) != want {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		_, err = io.Copy(out, rc) //nolint:gosec // archive is sha256-verified before extraction
		_ = rc.Close()
		return err
	}
	return fmt.Errorf("%s not found in archive", want)
}

// sanityCheck runs the extracted binary with --version so a corrupt or
// wrong-arch download is caught before it replaces the working binary.
func sanityCheck(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("new binary failed --version: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), "agent-brain") {
		return fmt.Errorf("new binary --version output unexpected: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
