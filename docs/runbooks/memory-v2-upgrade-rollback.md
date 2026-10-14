# Memory V2 upgrade and rollback

1. Stop assistant memory MCP processes and the Agent Brain daemon.
2. Back up the configured store and restore-test into another disposable database.
   For SQLite use `.backup`/the online backup API; copying an active WAL main file
   alone is unsafe. For PostgreSQL use a private `pg_dump --format=custom` backup.
3. Install and align V2-compatible hook/MCP/daemon binaries. Retain a compatible
   rollback binary. Existing `dev` versions may identify different executable hashes.
4. Preview `agent-brain memory upgrade-v2` (or add `--preview-backend postgres`).
   Only after reviewing the backup and DDL, run
   `agent-brain memory upgrade-v2 --apply` on the configured target store.
5. Restart every process and fresh assistant session. Approve changed Codex hooks
   through `/hooks`; never bypass trust. Inspect CLI/MCP memory status and compare
   store fingerprints. Verify a smoke capture only in an isolated scratch store.

The explicit transaction adds schema 16 and seven side tables; no historical
memory update/backfill occurs. Normal startup never silently applies this upgrade.
Schema-15 stores remain readable and support legacy saves, but reject V2-only save
metadata/operation IDs before writing. Cached older readers require restart.

## Safe rollback

Stop processes and use the retained V2-compatible binary with both variables
applied to **each** hook/MCP/daemon environment:

```sh
export AGENT_BRAIN_MEMORY_V2_AUTOMATION=off
export AGENT_BRAIN_MEMORY_V2_RANKING=off
```

Restart. Schema 16 and historical effective-state filtering remain active;
accepted lifecycle transitions remain effective. Flags do not reverse history.
Explicit restore/reopen appends a reversal when applicable. Do not drop tables,
decrement the marker or run a pre-V2 reader against schema 16: it may present a
superseded original row as current. A pre-upgrade backup restore is a separate
recovery operation requiring reconciliation of every subsequent write.

## Isolation and storage changes

Hosts may sanitize the environment passed to an MCP child. Set the child's env
explicitly to scratch `AGENT_BRAIN_DATA_DIR`, `AGENT_BRAIN_CONFIG_DIR`, an empty
`AGENT_BRAIN_PG_DSN` and SQLite-only scratch config. Also pass:

```text
mcp --require-data-dir /absolute/scratch/data --require-config-dir /absolute/scratch/config
```

These guards fail before opening a store. Never put real transcripts/authentication
material in reports or use a production database for synthetic tests.

A V2 SQLite transfer requires an explicitly upgraded PostgreSQL target before
`storage use postgres`; it does not silently upgrade the target. Back up both
stores, coordinate downtime, and verify side-table count/content checks before
cutover. Failed transfer does not authorize deleting the source.
