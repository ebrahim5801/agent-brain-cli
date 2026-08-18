# Data retention matrix (client)

**Owner**: repository maintainer

Every table the agent-brain CLI creates on your machine appears below with its
contents, retention rule, owner, and legal basis. A guard test
(`internal/store/retention_guard_test.go`) parses the migration SQL and fails the
build if any table is missing a row here. `schema_migrations` (bookkeeping) is
the sole exemption.

Legal-basis categories: **contract** (needed to provide the service),
**legitimate-interest** (product operation, minimized), **legal-obligation**
(tax/accounting), **consent** (explicitly granted, revocable).

## Client (SQLite, on the user's machine)

The client database belongs to the user and is never held by the vendor. It is
never touched by account or org deletion (FR-024).

| Table | Contents | Retention rule | Owner | Legal basis |
|---|---|---|---|---|
| `sessions` | Local session records awaiting/after sync | Retained locally until the user clears it | user | contract |
| `events` | Local per-session event counters | Retained locally until the user clears it | user | contract |
| `projects` | Local project link state | Retained locally until unlinked | user | contract |
| `memories` | Local personal memory store | Retained locally until the user deletes it | user | consent |
| `memory_usage_events` | Local retrieval/citation counters per session and memory entry | Retained locally until the user clears it; syncs identifiers/counts only, never memory content | user | legitimate-interest |
| `session_model_usage` | Local per-session token totals split by model | Retained locally until the user clears it; syncs model names/counts only, never content | user | legitimate-interest |
| `team_memories` | Local read-through cache of the team pool | Disposable cache; repopulated from the server | user | consent |
| `memory_team_supersedes` | Local supersede-link cache | Disposable cache; rebuilt on sync | user | consent |
| `team_sync_state` | Local team-memory sync cursor/endpoint | Retained locally to drive incremental sync | user | contract |
| `diagnostics` | Local diagnostic counters | Retained locally until the user clears it | user | legitimate-interest |
