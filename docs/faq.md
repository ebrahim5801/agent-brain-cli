# Frequently asked questions

Written for launch: the questions people actually ask before installing a tool
that watches their AI assistant. Answers link to the docs that go deeper.

## What is agent-brain?

A local collector that records every AI-coding-assistant session on your
machine — time, tokens, models, projects — plus an optional cloud with
dashboards, spending forecasts, and shared team memory. The local tool is free
and needs no account. See the [README](../README.md) for the full story.

## Do you read my code or my prompts?

No, and not as a policy promise — as a construction constraint. What syncs (only
after you opt in) is numbers: timestamps, durations, token counts, model IDs,
and activity counts. The wire protocol has no fields that could carry prompts,
code, file contents, file paths, or repository names, and a guard test fails the
build if such a field is ever added.

## Then how does "memory" work without reading my code?

Memory is text the *assistant itself* distills at the end of a session —
decisions made, conventions followed, what's half-finished. It is stored
locally and served back to the assistant at the start of the next session.
Personal memory has no network transport at all. Team memory is the one case
where memory text leaves a machine, and it requires its own explicit
per-project consent, separate from telemetry consent, with client-side secret
redaction and a personal-only flag for entries that should never be shared.

## How is this different from the usage page my AI vendor gives me?

Three ways. It's cross-assistant: one view over Claude Code, Cursor, Copilot
CLI, and Gemini CLI, not four vendor pages. It's per-project: sessions are
attributed automatically, so you see which project burns the tokens. And it
feeds the memory layer, which no single vendor will build neutrally across its
competitors.

## Does it slow my assistant down?

The capture path writes to a local SQLite database and has no network
capability at all (a lint gate fails the build if any is introduced), so
recording never waits on a server. Cloud sync runs in a separate background
service, off the assistant's path.

## What about the project I can't share anything from (NDA, client work)?

Several layers, pick the one you need. Projects you never link never transmit
anything. `agent-brain memory disable` turns memory off for one project
(`--all` for everywhere). On team projects, `agent-brain memory pause` stops
contributing while you keep receiving, and individual entries can be marked
personal-only.

## Are the token numbers accurate?

They are honest, which matters more: where an assistant reports token usage
(Claude Code, Gemini CLI) you get full counts; where it doesn't (Cursor reports
only the model), agent-brain records the count as absent rather than estimating.
Costs shown in dashboards are computed from real counts only.

## What happens if I stop paying?

Nothing is destroyed — lapsing pauses, never deletes. If Pro lapses, memory
pauses with all data kept and reactivates on re-upgrade. An expired self-hosted
license degrades the instance to read-only. Your local database is yours
regardless and is never touched by any server-side change.

## What happens to team memory when someone leaves the team?

Entries they authored for the team stay (the team relied on them) but are
pseudonymized under the team survivorship policy. Their personal
memory and local database are untouched; removed members can no longer access
the team pool.

## Someone got hold of our project key. How bad is that?

Not bad: a project key is not a password. Linking succeeds only for an account
that has been invited to the team, so a key alone grants nothing. Keys can be
rotated and revoked per project, and every key lifecycle event lands in the
audit log.

## Can I self-host it?

The CLI in this repository is entirely local: it needs no server at all, and
works fully offline. Cloud sync is optional.

Enterprise orgs that do want cloud features can additionally run the memory
pipeline — the only pipeline that carries content — on their own infrastructure
with a signed offline license, while analytics stays on the vendor cloud. That
backend is a separate, closed-source product; contact the maintainer for
details.

## Can I get my data out? Can I delete it?

Yes to both, self-serve. Account data export (DSAR) downloads your telemetry
and memory as two separated sections; orgs get a full export; self-hosted
memory exports/imports in both directions. Account deletion is end-to-end, with
the survivorship policy above as the only documented exception. Your local
SQLite database is under your control the entire time.

## Which AI assistants are supported?

Claude Code, Cursor, GitHub Copilot CLI, and Gemini CLI — each with full session
capture, memory injection, and end-of-session distillation. One
`agent-brain install` covers all of them; see the
[README](../README.md).

## Where is my data stored geographically?

Enterprise orgs choose an immutable home region (EU/US) at creation; all
primary data stays in that region's database, and moving is an explicit
owner-initiated migration. The only third-party flows are billing metadata to
Stripe, org-configured Slack/webhook deliveries, and invitation/alert email —
never memory content or telemetry values.
