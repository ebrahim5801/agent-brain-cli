# Memory quality evaluation

Fixtures are synthetic: 150 retrieval queries (50 held out by subject family),
40 multi-session/source-format workflows and 32 structured contradiction cases.
The tests measure retrieval and persistence/verification protocols, not a model's
general extraction accuracy. Scale is opt-in: 10,000 records, seed 42, 30 samples.

Run SQLite tests with `GOTOOLCHAIN=go1.26.9 go test ./tests/memoryeval`.
For PostgreSQL, set `AGENT_BRAIN_TEST_PG_DSN` to a **disposable** database and
`AGENT_BRAIN_REQUIRE_PG=1`; the harness uses per-test schemas. Never use a real
corpus, production DSN or copied authentication/transcripts. CI provides a scratch
PostgreSQL service. Set `AGENT_BRAIN_EVAL_SCALE=1` to include scale measurements.

`AGENT_BRAIN_EVAL_OUT` selects an output directory for JSON quality reports.
Source formats are bounded synthetic local JSONL. Unsupported/provenance-invalid
messages remain unverified; the contradiction benchmark covers explicit subject
and lexical cases, not arbitrary semantic paraphrases. For before/after comparison,
freeze a compatible baseline and run identical fixtures/labels on both builds.
