# ADR 0008: Versioned Ontology Foundation

- Status: Accepted
- Date: 2026-10-04
- Supersedes: None
- Refines: ADR 0003, ADR 0004, ADR 0005, ADR 0006
- Maintainer approval: issue #241 and its approved implementation plan

## Decision

Ontology is derived organization metadata over canonical Knowledge records.
Entity classes, predicate concepts, and topics have stable IDs and immutable
revisions. Classes supplement existing EntityKind values. Assignments reference
existing Entities, registered predicates, evidence, or Relationships. Evidence
and Relationship equivalence groups remain separate from topic membership.
Every member retains its original handle, owner, provenance, and lifecycle.
Ontology cannot create factual support, merge Entities, or rewrite Knowledge.

The public internal contract owns closed input validation, bounds, compatibility,
hierarchy, persistent override precedence, seeding policy, and versioned
fingerprints. The private PostgreSQL adapter implements scoped source reads,
atomic publication, indexed dependencies, immutable history, and pagination.
Only the active team-shared space and generation are eligible. Private sources
and historical generations cannot be organized into the shared catalog.
Ontology tables use `shared_space_id`, enforced by foreign keys, shared-space
checks, and forced RLS; they do not enter the private-content erasure catalog.

Publications require expected catalog and record revisions plus an operation
key and request hash. Matching retries reuse the durable result. Changed
requests conflict. The catalog lock serializes publishers; source fingerprints
are checked during publication and current reads. Changed sources or applicable
definitions/overrides make derived records stale. Unrelated definition edits
leave unaffected records usable. Manager overrides use stable source handles
and cannot be removed or contradicted by automatic publication. Overrides
cannot make unavailable or incompatible sources eligible.

Rollback publishes a new revision after checking intervening changes and source
eligibility. It never deletes history. The additive migration has no canonical
backfill; explicit seeding pages coarse classes and eligible predicate references.
Down refuses populated ontology history; production recovery rolls forward.

## Transition

#241 supplies dormant internal operations and fixtures. No provider, startup
processing, scheduler, public transport, control UI, or Recall consumer is wired
to this capability. Later program tickets own those integrations and their
evaluation gates. Existing Recall SemanticGroupKey behavior remains effective.
ADR 0007's graph Dream and historical Dream preservation requirements remain
effective until explicitly superseded by approved #530 work.

## Risk

- Derived groups could become factual authority. Keep all canonical writes in
  Knowledge and compare canonical rows before and after ontology operations.
- Private or stale support could enter shared groups. Force RLS on every
  ontology table and recheck shared-space, generation, and support eligibility.
- Automatic work could erase corrections. Persist manager overrides against
  stable handles and reject conflicting complete publications atomically.
- Rollback could revive invalid sources or erase history. Revalidate restored
  dependencies and retain immutable previous revisions.

## Verification

Use real non-superuser PostgreSQL cases for migration, forced RLS, A/B/C
isolation, revision races, replay, stale sources, overrides, rollback, and
canonical preservation. Keep deterministic organization judgments, source locks,
and compact baseline scoring for repetition, fact coverage, and source retention.
The foundation-only historical 1k waiver is recorded in #241; active retrieval
and later tickets retain their evaluation requirements. Existing Recall
regressions, local production E2E, full CI, and production-image E2E still apply.
