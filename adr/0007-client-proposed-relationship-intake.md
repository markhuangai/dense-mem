# ADR 0007: Client-Proposed Relationship Intake

- Status: Accepted
- Date: 2026-10-03
- Supersedes: ADR 0001's matching-retry guarantee only for historical Remember requests that fail the new citation requirements
- Refines: ADR 0001, ADR 0003, ADR 0004
- Maintainer approval: issue #526 and the approved implementation plan

## Decision

Contract `dense-mem.v2.6.6` requires a nonempty `relationships` array for
Remember, and every submitted evidence item must appear in at least one
proposal's `evidence_indices`. Remember owns this coverage rule; the registry
and Dream confirmation reuse it. Missing proposals and uncited evidence return
bounded, actionable validation errors before intake or provider work.

The client proposes Relationships. Dense-Mem still owns security, exact
grounding, normalization, authorization, lifecycle, and durable acceptance.
Coverage does not guarantee complete extraction or semantic acceptance. Safe
evidence and supported Relationships retain partial acceptance; unsupported
proposals receive `not_stored` results with their refs, reasons, messages, and
remediation. Unsafe evidence still rejects the entire batch.

The cutover is strict: historical evidence-only or incompletely cited requests
fail current validation even when retried with their original keys. Durable
history remains intact. Requests satisfying the new rules preserve the existing
same-key replay/conflict semantics and supported v2.6.2/v2.6.3 terminal results.

Hourly evidence-discovery Dream generation is retired. Graph inference keeps its
existing schedule, bounds, and evidence-supported paths. Historical discovery
hypotheses, derivations, runs, diagnostics, ownership restrictions, and
confirmation through Remember remain supported. New generation writers reject
the retired lane. The retirement migration cancels unfinished discovery runs
and abandons outstanding reservations without deleting historical records.

## Risk

- Unsupported proposals may leave safe evidence outside default recall. Return
  explicit per-proposal outcomes and require clients to inspect them.
- Removing discovery machinery may remove historical authorization or graph
  behavior. Retain those readers and policies and verify real PostgreSQL and
  production-entry regressions.
- Old instances could continue discovery during migration. Apply the migration
  only with old application instances stopped; cancellation is irreversible.

## Verification

Verify schema/call agreement, citation coverage, bounded detailed errors, partial
acceptance, strict historical rejection, supported replay, graph inference, no
hourly generation, historical reading/confirmation, and team/profile isolation.
Use real PostgreSQL for retirement and history preservation. Issue #526 records
the explicit deterministic 1k waiver and mandatory replacement checks before
implementation. Full final-head CI and production-image E2E remain required.
