# Memory V2

Memory V2 adds explicit session attribution, acknowledged capture and recovery,
local evidence checks, preserved decision history and improved BM25 retrieval.
It does not add embeddings, a vector database or automatic deletion.

## Activation

Installing the new binary does not activate the seven-table schema. Back up the
configured SQLite/PostgreSQL database, test restoring the backup, stop existing
memory processes, and align every hook/MCP/daemon on the same V2-compatible build.
Then preview and explicitly apply:

```sh
agent-brain memory upgrade-v2
agent-brain memory upgrade-v2 --preview-backend postgres
agent-brain memory upgrade-v2 --apply
```

Restart all assistants and the daemon. Codex hook changes require normal `/hooks`
approval. Check `agent-brain memory status` and the MCP `memory_status` tool;
configured assistants must agree on the store fingerprint and schema 16.
See [upgrade and rollback](runbooks/memory-v2-upgrade-rollback.md).

Historical memory rows are neither rewritten nor backfilled. Seven additive
tables store bindings, capture batches, write receipts, new-record details,
evidence, relationships and state events. Accepted transitions affect the
current view while preserving the original record. Unknown evidence does not
silently supersede a decision or complete a task.

## Sessions and capture

Session handles are scoped to project, assistant, native session and checkout.
Missing handles remain unbound; summaries require an explicit validated handle.
Concurrent assistants can share project memories without attributing a save to
the newest session. Checkpoints occur at supported hooks, not an idle timer.

`agent-brain memory capture --every N --checkpoint N` retains the existing public
capture settings; zero disables that trigger. Claude subagent tool activity and
memory-protocol calls do not arm the parent's checkpoint. Capture requests are
not persistence acknowledgments. Declare stable labels with
`memory_capture_begin`, save each using its operation ID and generation, then
call `memory_capture_complete` only after every item persisted. An empty manifest
requires explicit `no_durable_changes`; unavailable context remains deferred.
Identical retries return durable receipts; changed payloads cannot reuse an ID.

## Evidence and retrieval

New important memories can retain rationale, subject, scope, source references,
redacted excerpts, verification status/timestamp and relationships. The local
verifier checks bounded referenced Claude/Codex original user messages or
repository artifacts. It stores no complete transcripts. Model-generated claims,
tool output and known synthetic/child-spawn messages cannot verify user decisions.
Unsupported formats/prose remain unverified. Automatic lifecycle acceptance uses
narrow direct declarations and expected revisions; it is not general semantic
reasoning. Manual source review is bounded to selected IDs.

BM25 candidates pass relevance, effective-state and scope filtering with bounded
importance/freshness boosts. Accepted predecessor terms can retrieve the current
decision; unresolved alternatives remain visible. `memory_search(explain: true)`
shows score components. Git matching is separate from evidence verification.
All retrieved content is quoted untrusted data, including legacy records;
priority does not authorize actions or override the current conversation.

## Limits and evaluation

The synthetic quality suite covers 150 labeled queries, 40 multi-session/source
workflows and 32 constrained contradiction cases on SQLite/PostgreSQL. It measures
storage/retrieval protocols, not universal model extraction accuracy. Native live
annotated extraction and adversarial model behavior remain unqualified. OpenCode
V1's generated plugin has fixture coverage; OpenCode V2 needs separate plugin
adaptation, with existing MCP access as the fallback. Other unsupported source
formats remain unverified. Unsaved thoughts cannot be recovered after a crash.

Run [the quality suite](../tests/memoryeval/README.md) only against disposable data.
