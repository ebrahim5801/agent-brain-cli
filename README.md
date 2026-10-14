# agent-brain

A local-first collector, memory store, and MCP server for AI coding assistants.

agent-brain runs on your machine. It records how you actually use Claude Code,
Cursor, GitHub Copilot CLI, Gemini CLI, OpenCode, and Codex CLI — sessions,
token usage, cost — and gives your assistants a persistent, project-scoped
memory so they stop re-deriving what they already learned last week.

Everything is stored in a local SQLite database you own. Nothing leaves your
machine unless you explicitly link a project and grant consent.

## Install

One command installs the prebuilt binary — no Go needed:

```sh
curl -fsSL https://agentbrain.wecansync.com/install.sh | sh
```

On Windows, in PowerShell:

```powershell
irm https://agentbrain.wecansync.com/install.ps1 | iex
```

The installer downloads the binary for your OS and CPU (Linux, macOS, or
Windows on amd64/arm64), checks it against the release's `checksums.txt`,
installs it, and offers to run `agent-brain install` for your assistants. Set
`AGENT_BRAIN_VERSION=0.4.0` to pin a version, `AGENT_BRAIN_BIN_DIR=DIR` to
choose the folder, `AGENT_BRAIN_NO_INTEGRATE=1` to install the binary only, or
`AGENT_BRAIN_YES=1` to integrate every detected assistant without asking. The
full guide is at <https://agentbrain.wecansync.com/docs/>.

`git` is optional but recommended — without it, memory entries are stored
without git context.

### From source

Clone the repository and run the source installer. It checks the system
requirements (Go 1.25 or newer, checked against `go.mod`), builds the binary,
installs it, and offers to wire up your assistants.

```sh
git clone https://github.com/ebrahim5801/agent-brain-cli.git
cd agent-brain-cli
./install.sh
```

On Windows, run the PowerShell twin instead:

```powershell
powershell -ExecutionPolicy Bypass -File .\install.ps1
```

The binary goes to `~/.local/bin` (`%LOCALAPPDATA%\Programs\agent-brain` on
Windows). Useful flags:

| Flag | Effect |
| --- | --- |
| `--system` | install to `/usr/local/bin` (uses `sudo`); Linux and macOS only |
| `--bin-dir DIR` | install somewhere else (`-BinDir DIR` in PowerShell) |
| `--yes` | no prompts; integrate assistants automatically (`-Yes`) |
| `--no-integrate` | install the binary only (`-NoIntegrate`) |

`AGENT_BRAIN_BIN_DIR` overrides the install directory for both scripts.

Prefer to do it yourself?

```sh
go install github.com/ebrahim5801/agent-brain-cli/cmd/agent-brain@latest
agent-brain install
```

`agent-brain install` detects every supported assistant on your machine and
integrates each one. Existing settings are preserved (a timestamped backup is
written first), no file inside any repository is touched, and re-running it is
a safe no-op.

To integrate specific assistants only:

```sh
agent-brain install --assistant claude-code --assistant cursor
```

Codex CLI needs one extra step: it will not run a hook until you approve it.
After `agent-brain install`, open `codex`, run `/hooks`, and trust the
agent-brain entries. Until you do, Codex records nothing and reports no error —
`agent-brain status` carries the same reminder.

## Usage

```sh
agent-brain stats              # token usage and cost, by day, project, model
agent-brain stats --sessions   # per-session breakdown
agent-brain memory list        # what your assistants have remembered
agent-brain memory show <id>   # inspect one entry
agent-brain status             # integration health and collection activity
```

See [docs/quickstart.md](docs/quickstart.md) for the two-minute walkthrough and
[docs/faq.md](docs/faq.md) for common questions.

## Memory

Memory is the reason this exists. Assistants forget everything between
sessions, so they re-read the same files and re-ask the same questions. agent-brain
gives them an MCP server with two tools — `memory_search` and `memory_save` —
plus a session-start hook that injects the most relevant entries automatically.

Entries are typed (`decision`, `convention`, `task_state`, `fact`), carry the
git state they were captured at, and are ranked so stale ones are flagged rather
than silently trusted.

agent-brain asks the assistant to save memory after 10 tool calls of work since
the last save (not after every reply), at a mid-task checkpoint every 25 tool
calls, and before the context is compacted. The memory tools' own calls are not
counted. `agent-brain memory capture` shows the settings; `--every N` and
`--checkpoint N` change them (0 turns that trigger off, `--every 1` saves after
every reply that used a tool), and `--reset` restores the defaults.

## Privacy

The privacy boundary is enforced structurally, not by policy. The [`wire/`](wire/)
package defines every struct that can be serialized to the network, and a guard
test fails the build if a field for a local path, git remote, project name,
hostname, or message text ever appears there. A second guard test fails the
build if memory content reaches a transport package.

Data retention for every local table is documented in
[docs/compliance/retention.md](docs/compliance/retention.md), and a test fails
the build if a migration adds a table that isn't documented.

## Optional cloud sync

The CLI works fully offline and always will. Optionally, it can sync usage
statistics and share memory with a team via a hosted backend. That backend is a
separate, closed-source product; this repository contains only the client.

## Documentation

| Document | What it covers |
| --- | --- |
| [docs/quickstart.md](docs/quickstart.md) | Two-minute walkthrough: install, verify, first stats |
| [docs/faq.md](docs/faq.md) | What is collected, what never leaves your machine, common questions |
| [docs/compliance/retention.md](docs/compliance/retention.md) | Every local table and how long its rows are kept |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Development setup, test suite, guard tests, PR expectations |
| [SECURITY.md](SECURITY.md) | How to report a vulnerability |
| [CHANGELOG.md](CHANGELOG.md) | Release history |
| [LICENSE](LICENSE) / [NOTICE](NOTICE) | MIT License terms, name, and attribution |
| [docs/releasing.md](docs/releasing.md) | Building a release and publishing it to agentbrain.wecansync.com |

## Building

```sh
go build ./...
go test ./...
```

`install.sh` builds the same way, adding `-trimpath` and the version stamp, and
copies the result into your bin directory.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT. See [LICENSE](LICENSE). The agent-brain name is covered by
[NOTICE](NOTICE).
