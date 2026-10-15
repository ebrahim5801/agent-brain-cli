// Package update implements the client self-updater: it asks the update
// source (the cloud server, or a static release site) for the latest CLI
// release, compares versions, and atomically replaces the running binary in
// place. The binary path must never change — the assistant's
// MCP/hook settings reference the absolute path (plans/cli-self-update.md).
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

// Source is where releases come from. A cloud source (Static false) is the
// vendor server's /v1/cli/latest and /v1/cli/download/<file> endpoints; a
// static source is a release site laid out as releases/latest.json and
// releases/<version>/<file>, as packaging/release writes it.
type Source struct {
	Base   string
	Static bool
}

func (s Source) latestURL() string {
	if s.Static {
		return s.Base + "/releases/latest.json"
	}
	return fmt.Sprintf("%s/v1/cli/latest?os=%s&arch=%s", s.Base, runtime.GOOS, runtime.GOARCH)
}

func (s Source) downloadURL(version, filename string) string {
	if s.Static {
		return s.Base + "/releases/" + url.PathEscape(version) + "/" + url.PathEscape(filename)
	}
	return s.Base + "/v1/cli/download/" + filename
}

// Check asks the source for the latest release and compares it to current,
// the running version. A "dev" current never reports Newer (an untagged build
// cannot be meaningfully compared) but the manifest is still returned so
// --force can act.
func Check(ctx context.Context, client *http.Client, src Source, current string) (Result, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	client = noDowngrade(client)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.latestURL(), nil)
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

// noDowngrade returns a copy of client that refuses a redirect from https to
// any other scheme. The manifest's SHA-256 only protects an archive if the
// manifest itself came over TLS, so a release source's https must also cover
// wherever it redirects.
func noDowngrade(client *http.Client) *http.Client {
	c := *client
	next := client.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("refusing a redirect from https to %s", req.URL.Redacted())
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &c
}
