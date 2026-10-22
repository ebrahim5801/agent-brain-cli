# Changelog

## 0.5.11 - 2026-10-22

### Added

- `agent-brain memory export --format obsidian --out FOLDER` writes one note per memory, with properties, `agent-brain/<kind>` tags and links for replacements and related entries, plus an index note. Open the folder as an Obsidian vault to browse memories as a graph. It writes only into a new or empty folder, or one an earlier Obsidian export made, refuses a symlinked folder, and never overwrites a note it didn't write.

### Changed

- A new local (SQLite) store starts in the Memory V2 format, which session capture and summaries need; on the older format, saving a session summary failed. `AGENT_BRAIN_MEMORY_V2_DEFAULT=off` keeps the older format for new stores.
- `agent-brain memory enable` upgrades an existing local store to Memory V2. It and `agent-brain memory upgrade-v2 --apply` first copy a SQLite store to `<store>.pre-v2-<time>.bak` next to it (a consistent snapshot, readable only by you). A shared PostgreSQL store is never upgraded automatically, since teammates' older binaries may use it: `enable` says how to upgrade it, and `agent-brain storage use postgres` asks you to upgrade the PostgreSQL store first when this machine's store already uses Memory V2.
- `agent-brain update`, `memory enable`, `upgrade-v2 --apply` and `storage use` restart a running background sync service, which otherwise kept running the replaced binary or stale schema until the next login.

## 0.5.10 - 2026-10-21

### Added

- `agent-brain memory export --format json` and `agent-brain memory import FILE` move a project's memories to another machine or to a colleague working on the same project. The import shows a summary and asks before writing (`--dry-run` shows the summary only, `--yes` imports without asking). It checks the file came from the same project (same git remote; `--into-current-project` for a project without one) and skips entries already present, so importing twice adds nothing. The file holds nothing tied to the exporting machine: no sessions, transcripts, evidence or paths. Imported entries start unverified, and an import never retires an entry that was already on the machine. Entries shared with a team are left out, since team sync brings them. Without memory V2 on the importing machine, rationales, scoped entries, completed tasks and links are not kept, and the import says so.

## 0.5.9 - 2026-10-21

### Security

- Built with Go 1.26.10, which fixes GO-2026-6809: crypto/x509 now distrusts compromised intermediate certificates. agent-brain checks certificates when it updates itself and when it talks to the cloud server. Every earlier release was built with Go 1.26.9. CI now uses Go 1.26.10 too.

## 0.5.8 - 2026-10-21

### Added

- `agent-brain memory export` writes the project's memories as Markdown, one section per kind. `--out FILE` writes a file only you can read, and `--all` adds superseded and deleted entries.
- Every MCP tool declares whether it only reads (`readOnlyHint`). None is marked destructive: lifecycle changes are appended, never deleted.

### Changed

- Answering a memory request with "nothing to save" takes one call: `memory_capture_complete` with the batch ID and `no_durable_changes`, without `memory_capture_begin`. Only the session the request was issued to can do this.
- The periodic memory request needs at least 5 tool calls since the last request, and its 5-minute wait doubles after each consecutive "nothing to save" answer, up to an hour. A save resets it.
- When a session replaces an automatic task note it saved itself with a newer task note, in the same scope, the replacement is accepted at once instead of waiting for review. Other kinds of entries still need review, because session handles are visible in history and so cannot prove which session is asking.
- The session-start pack no longer injects an automatic entry whose replacement is waiting for review (the replacement is injected instead), or a task note last written more than 30 days ago unless it is critical. Both stay searchable.
- The pack is shorter: it leaves out the unverified default, project-wide or legacy scope, revision, unknown git state and the completeness note. The header says entries are unverified unless labeled. `memory_search` and `memory_list` still show every label.
- The pack holds whole entries only. An entry too long for the budget is skipped, so smaller ones below it still fit; only when nothing fits whole does the top entry go in as an excerpt.

### Fixed

- `memory_save` accepts a `memory_source_reference` result unchanged as evidence. Its `status` field was rejected before. The tool description now says what an evidence item looks like.

## 0.5.7 - 2026-10-20

### Changed

- Personal memory is free and needs no account. `agent-brain memory enable` no longer requires `agent-brain login` or a Pro plan: it asks for the one-time consent and turns memory on. Memories stay in the local database and never leave the machine, as before. Team memory, sharing and cloud sync still need a signed-in account and, for team memory, membership of a paying team's project.
- The memory tools' error messages no longer mention Pro or a paused entitlement. When memory is off, they say to run `agent-brain memory enable`. On a team project whose team memory is off, they also point to personal memory.
- `agent-brain status` shows personal memory as `off` or `active`. It no longer contacts the server to check a plan before showing it.

## 0.5.6 - 2026-10-16

### Fixed

- A Claude Code session got a new memory handle each time its shell changed into a subfolder of the repository, and the MCP server, which checks handles against the folder Claude Code started in, rejected those handles with "session attribution does not match project or checkout". Hooks now take the session's folder from `CLAUDE_PROJECT_DIR`, the folder Claude Code started in, and session handles are matched by the root of their git checkout, so a session keeps one handle. Handles that 0.5.5 issued in a subfolder are accepted again, and a session that only has such a handle keeps using it. Checkout-scoped memories and artifact evidence still use the exact folder.
- The Stop hook could block every reply with a new capture request when the agent could not act on the previous one. It now issues no new request of the same kind (stop, or checkpoint) while an earlier one was delivered and never begun. Beginning or deferring that request re-arms capture.
- `memory_capture_defer` accepts a request that was never begun, so an agent can always acknowledge one it cannot carry out.
- `memory_capture_status` without a `batch_id` returned its batches as a JSON array, which Claude Code's MCP client rejects ("expected record, received array"). It now returns `{"batches": [...]}`.
- `usage_stats` adds up every local row of a session, so a session that an earlier version split by folder is reported in full.
- Redaction missed credential keys with a prefix, such as `"memory_token": "..."` or `refresh_token=...`. They are now redacted before memories are stored or synced.

## 0.5.5 - 2026-10-15

### Fixed

- Windows: agent-brain's Claude Code hooks never ran. Claude Code runs hook commands in Git Bash on Windows, which drops the backslashes from the `C:\...\agent-brain.exe` path agent-brain wrote, so every hook failed with "command not found" and no usage, sessions or memories were recorded. Hook commands now use forward slashes on Windows and quote a path a shell would split. `agent-brain status` reports hooks in the old form, and `agent-brain install` rewrites them. macOS and Linux hooks are written as before.
- The store could fail to open with "database is locked" when several hooks started at the same moment, on every platform. agent-brain now switches the database to WAL mode once, with retries, instead of on every connection.
- Windows: rewriting `config.json` or an assistant's settings file retries for up to two seconds when another process has the file open, instead of failing with "Access is denied".

### Added

- CI runs the tests on Windows and installs a release with `install.ps1`, then runs every Claude Code hook through Git Bash.

## 0.5.4 - 2026-10-15

### Added

- A build can update from a release site instead of the cloud server. `-X github.com/ebrahim5801/agent-brain-cli/internal/config.DefaultUpdateURL=https://...` at build time, or `AGENT_BRAIN_UPDATE_URL` at run time, makes `agent-brain update` and the background updater read `releases/latest.json` and download `releases/<version>/<file>` from that site, as `packaging/release` lays it out. Downloads are still checked against the manifest's SHA-256. Only `https://` sites count, and a redirect away from https is refused. Sign-in, sync and memory keep using the cloud server. Source builds and the GoReleaser builds leave it empty and update from the cloud server as before.
- `packaging/release` stamps its builds with `-update-url`, by default https://agentbrain.wecansync.com, the site it builds.

### Changed

- `agent-brain update` names the site it checks.

## 0.5.3 - 2026-10-15

### Fixed

- `memory_list` with a `priority` or `kind` filter on a project that has no memories answers "no memories for this project", as an unfiltered list does, instead of "no memories on this page (total 0)".

## 0.5.2 - 2026-10-15

### Changed

- `agent-brain memory list` and the `memory_list` MCP tool show 50 entries per page by default, newest first, with a line saying how to get the next page. Use `--limit N` and `--offset N` (MCP: `limit`, `offset`, plus `priority` and `kind` filters); `--limit 0` lists everything.

## 0.5.1 - 2026-10-15

### Added

- A read-only `usage_stats` MCP tool reports this machine's token usage: one session (with its subagent runs), today, and this week. It reads only the local store and works without memory, so editor extensions can show usage without starting the CLI.

### Changed

- The session-start question about linking a project to agent-brain cloud is off by default; turn it on with `agent-brain config link-offer on`.

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
