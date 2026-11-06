# ADR 0016: Explicit Ontology Hierarchy Semantics

- Status: Accepted
- Date: 2026-11-06
- Supersedes: None
- Refines: ADR 0008
- Maintainer approval: issue #709 and its directly approved implementation plan

## Decision

An ontology definition may declare `parent_relation: "subclass_of"` for an
`entity_class`, or `parent_relation: "subproperty_of"` for a `predicate_concept`.
Both require `parent_id`. The parent must be an active definition of the same
kind; entity classes must retain the same base EntityKind. Unknown relations,
incompatible kinds, cycles, and chains exceeding 32 parent links are rejected.
An empty or absent relation retains the existing organizational meaning.

This metadata lives in existing immutable revision JSON. Empty relations are
omitted so legacy JSON, request hashes, record fingerprints, and the
`ontology-source-v1` version remain compatible. Declaring or changing semantics
changes the definition fingerprint and invalidates relevant dependents through
existing dependencies. History, replay, rollback, editor round trips, and
automatic reuse or refresh retain declared semantics. Automatic assessment
cannot replace an accepted semantic parent with a provider proposal.

The existing editor preserves metadata without adding semantic controls.
Clearing a semantic definition's required parent blocks the edit. This decision
does not add Recall expansion, inference, RDF/OWL, model schema changes, or
canonical Knowledge mutations. Existing ownership, generation, source
eligibility, override, publication, and audit rules remain authoritative.

Publication owners pass bounded observations to the composition root. Policy
does not perform logging IO. The context-aware logger emits DEBUG detail, WARN
rejections, ERROR storage failures, and INFO committed publications only after
the outer transaction and audit succeed. Public error classifications remain
unchanged. Trusted actor, correlation, operation, and available maintenance job
identities accompany observations. Complete protected payloads use the existing
stdout capture path independently of telemetry.

## Risk and recovery

- Older strict readers reject revisions containing the new field. Stop all
  incompatible application instances before saving semantic drafts or
  publications. After semantic revisions exist, retain compatible readers and
  recover forward; do not restore an older binary over them, erase history, or
  rewrite accepted revisions to remove metadata.
- Unrelated edits or automatic refresh could erase meaning. Preserve accepted
  semantics explicitly and verify both relation values through reparenting,
  stale reuse, history, replay, and rollback.
- A late audit or commit failure could produce misleading success logs. Emit
  outcomes at the surrounding transaction boundary and verify both failures
  against real PostgreSQL with unchanged history and no committed event.

## Verification

Issue #709 requires fixed legacy hash fixtures, contract and browser regressions,
the ontology-owned PostgreSQL semantic hierarchy cases, management and
organization production-entry scenarios, and final-head review/full CI/E2E.
Its fresh frozen 1k comparison reconciles terminal outcomes while retaining
native incompleteness, failures, ambiguity, and unavailable usage. Reconciled
failure accounting does not convert incomplete coverage into successful work.

The production TRACE default and filtering remain unchanged. Only the disposable
management scenario enables DEBUG explicitly. Log-only acceptance reconstructs
cycle, incompatible class-parent, and stale-version failures from the complete
Compose operation window before teardown, validating identities, chunk counts,
ordering, hashes, and credential canaries. Required captures must be complete.
The documented credential-protection limits remain in force; deliberate
exhaustion is a separate regression and cannot satisfy a required capture case.
