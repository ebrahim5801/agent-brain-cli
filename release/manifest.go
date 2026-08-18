// Package release defines the CLI release manifest shared by the server (which
// serves it) and the client self-updater (which consumes it). It carries no
// telemetry, so it lives outside the wire package and its redaction guard.
package release

// Manifest describes the latest published CLI release. It sits at
// $AGENT_BRAIN_RELEASE_DIR/manifest.json on the server and is served verbatim by
// GET /v1/cli/latest.
type Manifest struct {
	Version     string  `json:"version"`
	PublishedAt string  `json:"published_at"`
	Assets      []Asset `json:"assets"`
}

// Asset is one downloadable archive for a specific os/arch. Filename matches the
// GoReleaser archive name ({project}_{os}_{arch}.tar.gz, .zip on windows) and is
// the whitelist the download endpoint validates against.
type Asset struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// AssetFor returns the asset matching goos/goarch, or false when none exists.
func (m Manifest) AssetFor(goos, goarch string) (Asset, bool) {
	for _, a := range m.Assets {
		if a.OS == goos && a.Arch == goarch {
			return a, true
		}
	}
	return Asset{}, false
}

// HasFilename reports whether filename is one of the manifest's assets. The
// download endpoint uses this as a whitelist so path traversal is impossible by
// construction (no path cleaning, only exact match).
func (m Manifest) HasFilename(filename string) bool {
	for _, a := range m.Assets {
		if a.Filename == filename {
			return true
		}
	}
	return false
}
