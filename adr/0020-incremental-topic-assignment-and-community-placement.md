# ADR 0020 — Incremental Topic Assignment and Community Placement

- Status: Accepted
- Maintainer approval: [issue #824](https://github.com/markhuangai/dense-mem/issues/824)
- Supersedes: ADR 0008's single Topic assignment and durable override precedence, ADR 0017's background-only organization and override preservation, and ADR 0018's active ontology names and override behavior. The historical migration and replay contracts remain part of those records.

## Decision

For eligible team-shared Evidence, Remember publishes direct Topic memberships
in the transaction that commits Evidence, search state and the replay receipt.
A validated result can be uncategorized. One item can belong to several
Topics. Scoped Evidence and Topic IDs uniquely identify each active
membership. Optional Decisions assess supplied candidates before structured
generation handles uncertainty and missing Topics. A provider failure remains a
typed failure.

The existing scheduler processes incremental Topic assignment and Community
placement. Community placement stores a parent or standalone decision for a
Topic. Direct Topic filters select the union of their members. Pausing
maintenance stops background admission while foreground assignment and Topic
reads remain available. Disabling the feature defers assignment and ordinary
Evidence remains searchable.

Current PostgreSQL tables and settings use their feature names. Startup
verifies the physical migration before serving. Historical operation keys,
hashes, published IDs, Evidence and provenance retain their original identity.
Historical overrides remain available to history readers. The catalog editor
publishes ordinary definitions and parent edits. Recall and reranking each own
captured outcomes and use the existing feedback capture policy.

Recall can expand candidates through exact lexical pivots and current direct
memberships. It reauthorizes all hits and can apply optional Decision
reranking. Factual Relationships remain retired. Only the paired benchmark
can qualify expanded ranking as the default.

## Verification

Issue #824 requires populated and fresh PostgreSQL upgrades, exact replay,
paused and disabled write cases, authorization and concurrency, and complete
readiness for all five saved evaluation corpora before paired scoring.
