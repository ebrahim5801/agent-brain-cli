# Changelog

## 0.5.0 - 2026-10-14

### Added

- Explicit Memory V2 activation with seven additive SQLite/PostgreSQL tables; existing memory rows remain unchanged.
- Session-scoped capture manifests, durable idempotent receipts, checkpoints and crash/retry recovery.
- Bounded local original-user/artifact evidence checks, reasoning metadata and preserved lifecycle history.
- BM25 relevance/current-decision retrieval, untrusted-data rendering and V2-compatible rollback.
- Synthetic retrieval, workflow and contradiction evaluation with PostgreSQL regression coverage.

### Compatibility

- Upgrade is opt-in after backup; align all hook/MCP/daemon builds and restart assistants. Never downgrade schema-16 readers to a pre-V2 build.
- Preserve configurable capture thresholds and the Claude subagent checkpoint fix. Native live model quality remains unqualified.

## 0.4.0 - 2026-10-14

First release of the agent-brain CLI from this repository, and the first with
public prebuilt binaries. The client was previously developed in a private
monorepo alongside the hosted backend; this repository contains the client
only, extracted with its full functionality intact. The update server already
offered 0.3.0, so this release is numbered above it: a lower number would make
every install replace itself with 0.3.0 at its next update check.

### Added

- One-line installers that need no Go toolchain:
  `curl -fsSL https://agentbrain.wecansync.com/install.sh | sh` on macOS and
  Linux, and `irm https://agentbrain.wecansync.com/install.ps1 | iex` in
  Windows PowerShell. They download the prebuilt binary for the machine's OS
  and CPU, verify it against the release's `checksums.txt`, install it, and
  offer to run `agent-brain install`. Options: `AGENT_BRAIN_VERSION`,
  `AGENT_BRAIN_BIN_DIR`, `AGENT_BRAIN_NO_INTEGRATE=1`, `AGENT_BRAIN_YES=1`.
- A public documentation and install site under `site/`, served at
  https://agentbrain.wecansync.com.
- `go run ./packaging/release -version X.Y.Z` builds reproducible archives for
  Linux, macOS and Windows on amd64 and arm64, with `checksums.txt` and the
  release manifest, laid out as the site's docroot. See
  [docs/releasing.md](docs/releasing.md).
- `agent-brain memory capture` shows and sets when agent-brain asks the
  assistant to save memory: `--every N`, `--checkpoint N`, `--reset`.
  `agent-brain status` shows the settings.

### Changed

- The end-of-reply memory save now fires after 10 tool calls of work since the
  last save, instead of after almost every reply that used a tool. Tool calls
  keep accumulating across replies until the threshold is reached. The
  mid-task checkpoint stays at every 25 tool calls, and the save before
  context compaction is unchanged. `--every 1` restores the old behaviour.
- The memory tools' own calls (`mcp__agent-brain-memory__*`) no longer count
  towards either trigger.
- License: MIT (was Business Source License 1.1).

### Fixed

- Claude Code subagents no longer receive the mid-task memory checkpoint.
  Their tool calls arrive on the parent session's hooks with an `agent_id`;
  agent-brain counted them toward the parent's thresholds and answered with
  the checkpoint, which landed inside the subagent and cost it an extra
  memory-saving round. They are now recorded as `subagent_tool_use` events,
  count toward neither trigger, and are never answered.
