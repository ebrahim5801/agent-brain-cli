# Quickstart

From zero to seeing your AI usage in about two minutes. This is the end-user
guide — if you want to develop agent-brain itself, see
[CONTRIBUTING.md](../CONTRIBUTING.md) instead.

## 1. Install

Download the archive for your platform from the
[releases page](https://github.com/ebrahim5801/agent-brain-cli/releases), or
install from source (requires Go 1.25+):

```sh
go install github.com/ebrahim5801/agent-brain-cli/cmd/agent-brain@latest
agent-brain install
```

`install` detects every supported AI assistant on your machine — Claude Code,
Cursor, GitHub Copilot CLI, Gemini CLI — and integrates each one. Your existing
settings are preserved (a timestamped backup is written first), no file inside
any repository is touched, and re-running it is a safe no-op.

To integrate specific assistants only:

```sh
agent-brain install --assistant claude-code --assistant cursor
```

## 2. Verify

```sh
agent-brain status
```

You get one line per assistant showing detected/integrated state, plus the
database location and the last captured event. Run any AI session, check again,
and watch the last-event timestamp move.

## 3. See your stats

There is no step 3 setup — every session is captured automatically from now on,
attributed to its project with zero configuration.

```sh
agent-brain stats                 # this week, all projects
agent-brain stats --daily         # day-by-day
agent-brain stats --project name  # one project
agent-brain stats --json          # machine-readable
```

That is the complete free experience: local, offline, no account. Everything
below is optional.

## 4. Optional: the cloud dashboard

Three deliberate steps — nothing is transmitted before you complete all three:

```sh
agent-brain login                  # once per machine; approve in the browser
agent-brain link <project-key>     # once per project, inside its directory
# the first link asks for telemetry consent
```

Create the project on the web dashboard first to get its key. After linking, a
background service syncs usage numbers every minute (queues offline, never
double-counts). The dashboard shows trends, per-project costs, and — on Pro —
spending forecasts.

Only usage numbers ever sync: timestamps, durations, token counts, model IDs,
activity counts. Your prompts, code, file paths, and repo names stay on your
machine — the sync protocol has no fields for them.

To stop: `agent-brain unlink` (one project), `agent-brain consent withdraw`
(all transmission), `agent-brain logout` (the machine).

## 5. Optional: memory (Pro)

```sh
agent-brain memory enable
```

From then on, each session ends with the assistant distilling durable context
(decisions, conventions, task state) into your local store, and each new session
starts with that memory injected before any code is read — resuming a
conversation instead of starting cold. Inspect it any time with
`agent-brain memory list` / `show` / `edit` / `delete`.

Personal memory never leaves your machine. On a Team plan, memory can be shared
with your team under a separate, per-project consent.

## Uninstall

```sh
agent-brain uninstall                # removes integrations, keeps your data
agent-brain uninstall --purge-data   # removes everything
```
