#!/bin/sh
# agent-brain installer for macOS and Linux.
#
#   curl -fsSL https://agentbrain.wecansync.com/install.sh | sh
#
# Downloads the prebuilt binary for this OS and CPU, checks it against the
# release's checksums.txt, installs it, then offers to run `agent-brain
# install` to integrate your coding assistants. No Go toolchain is needed.
#
# Environment:
#   AGENT_BRAIN_VERSION=0.5.0     install this version instead of the latest
#   AGENT_BRAIN_BIN_DIR=DIR       install directory (default ~/.local/bin)
#   AGENT_BRAIN_NO_INTEGRATE=1    install the binary only
#   AGENT_BRAIN_YES=1             integrate every detected assistant without asking
#   AGENT_BRAIN_BASE_URL=URL      download from a mirror (default below)

set -eu

BASE_URL="${AGENT_BRAIN_BASE_URL:-https://agentbrain.wecansync.com}"
BASE_URL="${BASE_URL%/}"
BIN_DIR="${AGENT_BRAIN_BIN_DIR:-$HOME/.local/bin}"
VERSION="${AGENT_BRAIN_VERSION:-}"

red=""; green=""; yellow=""; bold=""; reset=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	red=$(printf '\033[31m'); green=$(printf '\033[32m')
	yellow=$(printf '\033[33m'); bold=$(printf '\033[1m'); reset=$(printf '\033[0m')
fi
info() { printf '%s\n' "$*"; }
ok() { printf '%s✓%s %s\n' "$green" "$reset" "$*"; }
warn() { printf '%s!%s %s\n' "$yellow" "$reset" "$*" >&2; }
die() { printf '%sx%s %s\n' "$red" "$reset" "$*" >&2; exit 1; }

# The whole script is one function called on the last line, so a download cut
# short by the network never runs half an installer.
main() {
	printf '%sInstalling agent-brain%s\n' "$bold" "$reset"

	case "$(uname -s)" in
		Linux) os="linux" ;;
		Darwin) os="darwin" ;;
		MINGW*|MSYS*|CYGWIN*) die "on Windows, run in PowerShell: irm $BASE_URL/install.ps1 | iex" ;;
		*) die "unsupported operating system: $(uname -s)" ;;
	esac
	case "$(uname -m)" in
		x86_64|amd64) arch="amd64" ;;
		arm64|aarch64) arch="arm64" ;;
		*) die "unsupported CPU: $(uname -m) (amd64 and arm64 are supported)" ;;
	esac
	ok "$os/$arch"

	# HTTPS only, redirects included, unless the mirror itself is plain HTTP
	# (a local test server).
	case "$BASE_URL" in
		http://*) proto="=http,https" ;;
		*) proto="=https" ;;
	esac
	if command -v curl >/dev/null 2>&1; then
		fetch() { curl -fsSL --proto "$proto" --proto-redir "$proto" --tlsv1.2 --retry 2 -o "$2" "$1"; }
	elif command -v wget >/dev/null 2>&1; then
		fetch() { wget -q -O "$2" "$1"; }
	else
		die "curl or wget is required"
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		sha256() { sha256sum "$1" | cut -d' ' -f1; }
	elif command -v shasum >/dev/null 2>&1; then
		sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
	else
		die "sha256sum or shasum is required to verify the download"
	fi
	command -v tar >/dev/null 2>&1 || die "tar is required"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	if [ -z "$VERSION" ]; then
		fetch "$BASE_URL/releases/latest.json" "$tmp/latest.json" ||
			die "could not read $BASE_URL/releases/latest.json"
		VERSION=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -n1)
	fi
	VERSION="${VERSION#v}"
	printf '%s\n' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$' ||
		die "not a release version: '$VERSION'"

	archive="agent-brain_${os}_${arch}.tar.gz"
	release="$BASE_URL/releases/$VERSION"
	info "Downloading agent-brain $VERSION..."
	fetch "$release/$archive" "$tmp/$archive" || die "could not download $release/$archive"
	fetch "$release/checksums.txt" "$tmp/checksums.txt" || die "could not download $release/checksums.txt"

	expected=$(awk -v f="$archive" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/checksums.txt")
	[ -n "$expected" ] || die "checksums.txt has no entry for $archive"
	actual=$(sha256 "$tmp/$archive")
	[ "$actual" = "$expected" ] || die "checksum mismatch for $archive (expected $expected, got $actual)"
	ok "checksum verified"

	mkdir -p "$tmp/x"
	tar -xzf "$tmp/$archive" -C "$tmp/x" agent-brain || die "the archive has no agent-brain binary"
	[ -f "$tmp/x/agent-brain" ] && [ ! -L "$tmp/x/agent-brain" ] || die "the archive has no agent-brain binary"
	chmod 0755 "$tmp/x/agent-brain"
	"$tmp/x/agent-brain" --version >/dev/null 2>&1 || die "the downloaded binary does not run on this machine"

	mkdir -p "$BIN_DIR" 2>/dev/null || die "cannot create $BIN_DIR (set AGENT_BRAIN_BIN_DIR to a folder you can write)"
	[ -w "$BIN_DIR" ] || die "$BIN_DIR is not writable (set AGENT_BRAIN_BIN_DIR to a folder you can write)"
	# Copy next to the target, then rename, so a running agent-brain is never
	# left with a half-written file.
	cp "$tmp/x/agent-brain" "$BIN_DIR/.agent-brain.new.$$"
	mv -f "$BIN_DIR/.agent-brain.new.$$" "$BIN_DIR/agent-brain"
	ok "installed $BIN_DIR/agent-brain ($("$BIN_DIR/agent-brain" --version 2>/dev/null | head -n1))"

	case ":$PATH:" in
		*":$BIN_DIR:"*) ;;
		*)
			warn "$BIN_DIR is not on your PATH. Add this line to your shell profile:"
			warn "    export PATH=\"$BIN_DIR:\$PATH\""
			;;
	esac

	printf '\n'
	if [ "${AGENT_BRAIN_NO_INTEGRATE:-}" = "1" ]; then
		info "Skipped assistant integration. Run 'agent-brain install' when you are ready."
	elif [ "${AGENT_BRAIN_YES:-}" = "1" ]; then
		# With stdin on a pipe, `agent-brain install` integrates every detected
		# assistant without asking. (Not </dev/null: that is a character device,
		# which agent-brain treats as a terminal.)
		: | "$BIN_DIR/agent-brain" install
	elif (: </dev/tty) 2>/dev/null; then
		info "'agent-brain install' finds the AI assistants on this machine and asks"
		info "before integrating each one. Existing settings are backed up first and"
		info "no file inside any repository is touched."
		printf 'Run it now? [Y/n] '
		read -r answer </dev/tty || answer=""
		case "$answer" in
			""|y|Y|yes|YES) "$BIN_DIR/agent-brain" install </dev/tty ;;
			*) info "Skipped. Run 'agent-brain install' when you are ready." ;;
		esac
	else
		info "No terminal, so assistant integration was skipped."
		info "Run 'agent-brain install' to integrate your assistants."
	fi

	printf '\n%sDone.%s Next: agent-brain status\n' "$green" "$reset"
}

main "$@"
