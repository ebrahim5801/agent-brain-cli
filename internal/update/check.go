// Package update implements the client self-updater: it asks the cloud server
// for the latest CLI release, compares versions, and atomically replaces the
// running binary in place. The binary path must never change — the assistant's
// MCP/hook settings reference the absolute path (plans/cli-self-update.md).
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/ebrahim5801/agent-brain-cli/release"
)

// Result reports a version check against the server.
type Result struct {
	Current  string
	Latest   string
	Newer    bool          // Latest is strictly newer than Current
	Asset    release.Asset // release asset for this os/arch
	HasAsset bool          // an asset exists for this platform
}

// Check asks the server for the latest release and compares it to current.
// serverURL is the vendor cloud base (cfg.Server()); current is the running
// version. A "dev" current never reports Newer (an untagged build cannot be
// meaningfully compared) but the manifest is still returned so --force can act.
func Check(ctx context.Context, client *http.Client, serverURL, current string) (Result, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	url := fmt.Sprintf("%s/v1/cli/latest?os=%s&arch=%s", serverURL, runtime.GOOS, runtime.GOARCH)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return Result{}, fmt.Errorf("no release is published for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("release check failed (%d)", resp.StatusCode)
	}
	var m release.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return Result{}, fmt.Errorf("decode manifest: %w", err)
	}

	res := Result{Current: current, Latest: m.Version}
	res.Asset, res.HasAsset = m.AssetFor(runtime.GOOS, runtime.GOARCH)
	if current != "dev" && current != "" {
		res.Newer = compareVersions(m.Version, current) > 0
	}
	return res, nil
}
