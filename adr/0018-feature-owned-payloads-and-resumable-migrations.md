# ADR 0018: Feature-Owned Payloads and Resumable Migrations

- Status: Accepted
- Date: 2026-11-13
- Supersedes: None
- Refines: ADR 0003, ADR 0004, ADR 0008, ADR 0011, and ADR 0017 (Evidence-only memory)
- Maintainer approval: approved combined implementation plan in [issue #746](https://github.com/markhuangai/dense-mem/issues/746)

## Decision

Keep one PostgreSQL database. Exact intake, Evidence, occurrences, provenance,
ownership, and lifecycle retain their existing tables and authority. Ontology
keeps shared identity, revision metadata, publication receipts, and indexed
dependencies. Feature tables own topic, membership, equivalence, and manager
override revision payloads. Manager draft edits and prepared changes live in
the same feature partitions with their original container and ordinal; a shared
envelope retains the remaining draft fields and missing/null/array shapes.

The ontology adapter reconstructs the existing records and drafts through
invoker-rights views. Public contracts, fingerprints, IDs, revisions, operation
keys, request hashes, and replay behavior do not change. Publication and draft
persistence remain single transactions. Scoped foreign keys, forced RLS,
append-only payloads, and deferred completeness checks protect reconstruction.
Private memory and stale generations remain ineligible for shared organization.

Operational history remains retention-governed. Raw provider diagnostics are
opaque history, not another feature payload authority. Shared receipts retain
stable references. Removing a feature may still require explicit cleanup of
dependent membership, override, or receipt references; this separation limits
the owned data that must change without promising zero data work.

## Migration and recovery

Deployed migrations are immutable. Older databases finish the existing
Evidence-only migration before preparation, data conversion, and cutover.
ADR 0017's separately approved reset governs a still-pending Evidence-only
cutover; this change preserves the organization present after that boundary.
This executor establishes the contract for this and subsequent migrations;
it does not retrofit resumability into historical migrations.

Preparation creates empty feature/checkpoint tables and a durable write fence.
The existing Goose provider registers the data and cutover executors as
transaction-disabled Go migrations. A dedicated connection holds a session
advisory lock while explicit transactions copy at most 100 source rows and
8 MiB per batch, with a 30-second deadline and five-second lock timeout. The
existing overall migration timeout still applies. Checkpoints commit with the
data, carrying executor/inventory/schema identities, phase, keyset cursor,
counts, fingerprints, and completion state. Repeated keys must contain the
same JSON payload. Source fingerprint scans and verification use bounded units.
Retained rows larger than one batch use byte fragments and a primary-key cursor
with a byte offset; each fragment checkpoints its fingerprint, and row counts
advance only when the complete row has been scanned. Prefix verification also
revalidates committed fragments before resuming an incomplete row.
Goose needs a connection for each waiting locker and a separate executor slot;
finite pools temporarily retain that capacity during migration and restore the
configured limit after the last overlapping starter exits.

Normal application writers remain fenced until cutover and must set the
transaction-local storage layout thereafter. Migration transactions are the
explicit maintenance bypass; a database superuser outside application modes
retains its administrative access. Listeners and workers start only after
migration validation. A restart verifies committed source prefixes and payloads
before resuming. Changed sources, incompatible layouts, missing payloads, and
mismatched receipts refuse completion.

After reconstruction and retained-data verification, a short DDL transaction
removes the old mixed body columns and writes the compatible receipt with the
completed state. A restart reconciles this receipt if the process died before
Goose recorded the migration. Recovery resumes or rolls forward with a matching
application. Down refuses; this delivery performs no backup/restore rehearsal.

## Ownership and dependency inventory

| Owner | Durable relations and dependencies |
| --- | --- |
| Canonical memory | Existing intake, Evidence, occurrence, source revision, lineage, legal hold, privacy, and security tables; conversion reads and fingerprints them without rewriting them. |
| Ontology identity | `ontology_catalog_heads`, `ontology_record_heads`, `ontology_record_revisions`, `ontology_publications`, `ontology_source_dependencies`, and `ontology_revision_dependencies`; scoped keys and publication replay remain authoritative. |
| Feature payloads | `ontology_{topic,membership,equivalence,override}_{revision,draft}_payloads`; revision FKs retain team, shared space, generation, ID, and version. Draft FKs additionally retain owner. |
| Draft workflow | `ontology_draft_heads`, `ontology_draft_revisions`, and `ontology_draft_envelopes`; existing head/version guards and deferred head-to-revision FK remain. |
| Coordination and history | Existing maintenance state/leases, markers, assessments, attempts, and operation receipts retain their policy and references. |

The existing catalog/head guards, revision append-only guards, draft guards,
and `dense_mem_ontology_dirty_ontology_record_heads` marker trigger remain.
The new feature tables add `feature_append_only` and
`feature_storage_write_gate`. Cutover adds deferred
`feature_payload_required`, `feature_payload_consistent`, and
`feature_draft_required` checks. Payload union views are read-only and use
`security_invoker`; the record and draft read views reconstruct the existing
adapter representation. The three body columns are dropped with PostgreSQL's
default RESTRICT dependency behavior, never CASCADE. Unexpected dependencies
therefore block cutover rather than being silently removed.

Cutover releases this migration's temporary fence on the eight legacy
`v2_migration_*` control tables after verification, in the completion transaction.
Their existing read-only RLS and guarded retirement contract remain authoritative;
runtime and canonical tables retain the storage-layout fence.

## Risk

- Splitting mixed drafts can lose order or shape. Retain ordinals and envelope
  shapes and compare the complete reconstructed JSON before removing sources.
- A stale writer can invalidate committed checkpoints. Fence writes in the
  database, serialize executors, and require the layout version after cutover.
- A process can die after data commit but before reporting it. Couple checkpoint
  writes with data and distinguish committed, rolled-back, and unconfirmed work.
- Cross-feature reads can expose another owner or slow Recall. Preserve scoped
  FKs and forced RLS, batch reads, and compare fixed-fixture results and timings.

## Verification and operations

Issue #746 records the approved 1k evaluation waiver. Replacement checks cover
fresh/populated conversion, cancellation/restart, concurrent starters, connection
loss, ambiguous bookkeeping, mismatched sources/destinations, exact preservation,
real A/B/C isolation, Recall/topic behavior, and fixed-fixture timing samples.
Development E2E runs registered PostgreSQL regressions and production entry
points; final PR validation retains the full production-image matrix and CI.

At the existing TRACE default, INFO records phase boundaries and committed
checkpoints, WARN reports cancellation or incomplete capture, and ERROR reports
required failures. Migration diagnostics carry safe identities, counts,
fingerprints, transaction outcome, and recovery action without memory text or
credentials. Collect complete server stdout before teardown to diagnose failure
and restart. Operational-log pruning reports committed deletion counts, cutoff,
effective retention, duration, and errors; unavailable settings stop pruning
instead of silently changing retention. Existing hold checks remain effective.
