#!/usr/bin/env bash
#
# agent-brain installer.
#
# Run from a clone of this repository:
#
#   ./install.sh                 install to ~/.local/bin
#   ./install.sh --system        install to /usr/local/bin (uses sudo)
#   ./install.sh --bin-dir DIR   install to DIR
#   ./install.sh --yes           no prompts; integrate assistants automatically
#   ./install.sh --no-integrate  install the binary only
#
# Environment: AGENT_BRAIN_BIN_DIR overrides the install directory.

set -euo pipefail

MODULE_PATH="github.com/ebrahim5801/agent-brain-cli"

REPO_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
BIN_DIR="${AGENT_BRAIN_BIN_DIR:-}"
USE_SYSTEM=0
ASSUME_YES=0
INTEGRATE=1

red=""; green=""; yellow=""; bold=""; reset=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	red=$(printf '\033[31m'); green=$(printf '\033[32m')
	yellow=$(printf '\033[33m'); bold=$(printf '\033[1m'); reset=$(printf '\033[0m')
fi

info() { printf '%s\n' "$*"; }
ok() { printf '%s✓%s %s\n' "$green" "$reset" "$*"; }
warn() { printf '%s!%s %s\n' "$yellow" "$reset" "$*" >&2; }
die() { printf '%s✗%s %s\n' "$red" "$reset" "$*" >&2; exit 1; }

usage() {
	cat <<'EOF'
agent-brain installer.

Run from a clone of this repository:

  ./install.sh                 install to ~/.local/bin
  ./install.sh --system        install to /usr/local/bin (uses sudo)
  ./install.sh --bin-dir DIR   install to DIR
  ./install.sh --yes           no prompts; integrate assistants automatically
  ./install.sh --no-integrate  install the binary only

Environment: AGENT_BRAIN_BIN_DIR overrides the install directory.
EOF
	exit 0
}

while [ $# -gt 0 ]; do
	case "$1" in
		-h|--help) usage ;;
		--system) USE_SYSTEM=1 ;;
		--bin-dir) [ $# -ge 2 ] || die "--bin-dir needs a directory"; BIN_DIR="$2"; shift ;;
		--bin-dir=*) BIN_DIR="${1#--bin-dir=}" ;;
		-y|--yes) ASSUME_YES=1 ;;
		--no-integrate) INTEGRATE=0 ;;
		*) die "unknown option: $1 (try --help)" ;;
	esac
	shift
done

# ---------------------------------------------------------------- requirements

printf '%sChecking system requirements%s\n' "$bold" "$reset"

os=$(uname -s)
case "$os" in
	Linux) os_label="Linux" ;;
	Darwin) os_label="macOS" ;;
	MINGW*|MSYS*|CYGWIN*)
		die "this script is for Linux and macOS. On Windows run install.ps1 in PowerShell." ;;
	*) die "unsupported operating system: $os" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64|amd64) arch_label="amd64" ;;
	arm64|aarch64) arch_label="arm64" ;;
	*) die "unsupported architecture: $arch (amd64 and arm64 are supported)" ;;
esac
ok "$os_label/$arch_label"

[ -f "$REPO_DIR/go.mod" ] || die "go.mod not found in $REPO_DIR — run this script from a clone of the repository"
grep -q "^module $MODULE_PATH\$" "$REPO_DIR/go.mod" || die "$REPO_DIR/go.mod is not the agent-brain module"
[ -d "$REPO_DIR/cmd/agent-brain" ] || die "cmd/agent-brain not found in $REPO_DIR — the clone looks incomplete"
ok "repository at $REPO_DIR"

# The required toolchain is whatever go.mod asks for, so this never drifts.
required_go=$(sed -n 's/^go \([0-9][0-9.]*\).*/\1/p' "$REPO_DIR/go.mod" | head -n1)
[ -n "$required_go" ] || die "could not read the go directive from go.mod"

command -v go >/dev/null 2>&1 || die "Go $required_go or newer is required but 'go' was not found. Install it from https://go.dev/dl/ and re-run."

go_version=$(go env GOVERSION 2>/dev/null || true)
go_version="${go_version#go}"
[ -n "$go_version" ] || die "could not determine the installed Go version"

# Compare dotted versions field by field, treating a missing field as 0.
version_lt() {
	a1=$(printf '%s' "$1" | cut -d. -f1); a2=$(printf '%s' "$1" | cut -d. -f2); a3=$(printf '%s' "$1" | cut -d. -f3)
	b1=$(printf '%s' "$2" | cut -d. -f1); b2=$(printf '%s' "$2" | cut -d. -f2); b3=$(printf '%s' "$2" | cut -d. -f3)
	for pair in "${a1:-0} ${b1:-0}" "${a2:-0} ${b2:-0}" "${a3:-0} ${b3:-0}"; do
		set -- $pair
		[ "$1" -lt "$2" ] && return 0
		[ "$1" -gt "$2" ] && return 1
	done
	return 1
}

if version_lt "$go_version" "$required_go"; then
	die "Go $required_go or newer is required, found $go_version. Upgrade from https://go.dev/dl/ and re-run."
fi
ok "Go $go_version"

if command -v git >/dev/null 2>&1; then
	ok "git $(git --version | awk '{print $3}')"
else
	warn "git not found — memory entries will be stored without git context"
fi

# ------------------------------------------------------------------ target dir

if [ -z "$BIN_DIR" ]; then
	if [ "$USE_SYSTEM" -eq 1 ]; then
		BIN_DIR="/usr/local/bin"
	else
		BIN_DIR="$HOME/.local/bin"
	fi
elif [ "$USE_SYSTEM" -eq 1 ]; then
	die "--system and --bin-dir are mutually exclusive"
fi

SUDO=""
if ! mkdir -p "$BIN_DIR" 2>/dev/null || [ ! -w "$BIN_DIR" ]; then
	if [ "$(id -u)" -eq 0 ]; then
		mkdir -p "$BIN_DIR" || die "cannot create $BIN_DIR"
	elif command -v sudo >/dev/null 2>&1; then
		info "$BIN_DIR is not writable; sudo will be used to install there."
		SUDO="sudo"
		$SUDO mkdir -p "$BIN_DIR" || die "cannot create $BIN_DIR"
	else
		die "$BIN_DIR is not writable and sudo is not available. Re-run with --bin-dir DIR."
	fi
fi
ok "install directory $BIN_DIR"

# ---------------------------------------------------------------------- build

printf '\n%sBuilding agent-brain%s\n' "$bold" "$reset"

build_version="dev"
if command -v git >/dev/null 2>&1 && git -C "$REPO_DIR" rev-parse --git-dir >/dev/null 2>&1; then
	described=$(git -C "$REPO_DIR" describe --tags --dirty 2>/dev/null || true)
	[ -n "$described" ] && build_version="$described"
fi

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

(
	cd "$REPO_DIR"
	CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X $MODULE_PATH/internal/version.Version=$build_version" \
		-o "$tmp_dir/agent-brain" ./cmd/agent-brain
) || die "build failed"
ok "built $build_version"

$SUDO install -m 0755 "$tmp_dir/agent-brain" "$BIN_DIR/agent-brain" ||
	die "could not install to $BIN_DIR"
ok "installed $BIN_DIR/agent-brain"

case ":$PATH:" in
	*":$BIN_DIR:"*) ;;
	*)
		warn "$BIN_DIR is not on your PATH. Add this to your shell profile:"
		warn "    export PATH=\"$BIN_DIR:\$PATH\""
		;;
esac

# ------------------------------------------------------------------ integrate

printf '\n'
if [ "$INTEGRATE" -eq 0 ]; then
	info "Skipping assistant integration. Run '$BIN_DIR/agent-brain install' when you are ready."
elif [ "$ASSUME_YES" -eq 1 ]; then
	"$BIN_DIR/agent-brain" install
elif [ -t 0 ]; then
	info "'agent-brain install' detects the AI assistants on this machine and wires"
	info "each one up. Existing settings are backed up first and no file inside any"
	info "repository is touched."
	printf 'Run it now? [Y/n] '
	read -r answer || answer=""
	case "$answer" in
		""|y|Y|yes|YES) "$BIN_DIR/agent-brain" install ;;
		*) info "Skipped. Run '$BIN_DIR/agent-brain install' when you are ready." ;;
	esac
else
	info "Non-interactive shell; skipping assistant integration."
	info "Run '$BIN_DIR/agent-brain install' to wire up your assistants."
fi

printf '\n%sDone.%s Try: agent-brain status\n' "$green" "$reset"
