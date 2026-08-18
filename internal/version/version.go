// Package version exposes the binary's build version to every package that
// needs it (CLI banner, MCP/daemon heartbeat, self-update). It lives in its own
// package so both internal/cli and internal/update can read it without an import
// cycle. GoReleaser injects the real value via ldflags
// (-X github.com/ebrahim5801/agent-brain-cli/internal/version.Version=vX.Y.Z);
// untagged builds keep "dev" and refuse to self-update.
package version

var Version = "dev"

// IsDev reports whether this is an untagged development build.
func IsDev() bool {
	return Version == "dev" || Version == ""
}
