# agent-brain

A local-first collector, memory store, and MCP server for AI coding assistants.

agent-brain runs on your machine. It records how you actually use Claude Code,
Cursor, GitHub Copilot CLI, Gemini CLI, and OpenCode — sessions, token usage,
cost — and gives your assistants a persistent, project-scoped memory so they
stop re-deriving what they already learned last week.

Everything is stored in a local SQLite database you own. Nothing leaves your
machine unless you explicitly link a project and grant consent.

## Install

Download the release for your platform, or build from source (Go 1.25+):

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

## Building

```sh
go build ./...
go test ./...
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Business Source License 1.1. You may use agent-brain freely, including
commercially inside your own organization. You may not offer it to third
parties as a hosted or managed agent-memory service. On 2030-08-18 it converts
to Apache-2.0. See [LICENSE](LICENSE).
