# ADR 0010: Ontology Organization and Legacy Retirement

- Status: Accepted
- Date: 2026-10-12
- Supersedes: ADR 0007's graph Dream and client historical confirmation requirements; ADR 0008's transitional Dream preservation, legacy community fallback, and community master switch
- Refines: ADR 0001, ADR 0002, ADR 0003, ADR 0004, ADR 0008
- Maintainer approval: issue #530 and the directly approved implementation plan

ADR 0014 supersedes global token accounting as an ontology spending/admission
gate. Usage reporting and historical accounting remain preserved; ADR 0014
defines the transition for deployed budget behavior.

## Decision

Contract `dense-mem.v2.7.0` removes client Dream tools, hypothesis recall,
hypothesis feedback, generation, confirmation, and whole-graph community
clustering. Ordinary clients receive eligible evidence, Relationship handles,
and current ontology topic community views. General recall-quality feedback
remains optional and unprompted. Remember retains existing request hashes and
terminal replay versions v2.6.8, v2.6.7, v2.6.6, v2.6.3, and v2.6.2.

The existing operator-controlled ontology switch is the only activation control.
Preserve every stored value and default missing values to false. Do not infer
enablement from legacy settings. Enabling admits work on the existing minute
tick under existing pause, cadence, concurrency, and per-attempt usage reporting.
Disabling stops new admissions and drains dispatched work. All active teams'
shared memory participates, regardless of retired per-team Dream settings.
Private memory is excluded. No activation marker or separate cutover command
is introduced. Disabled ontology preserves ordinary evidence retrieval and
never revives legacy clustering or legacy community snapshot reads.

Apply the retirement migration with old instances stopped. It cancels unfinished
legacy runs, abandons outstanding reservations, releases leases, disables old
settings, and appends audit outcomes in one transaction. Completed runs,
hypotheses, derivations, accepted knowledge, and audit history remain intact.
Read-only operator inspection and diagnostic retention remain supported.
General feedback writes cannot overwrite historical hypothesis feedback.
Retention and privacy erasure still include the historical records.

Rollback may disable ontology while retaining its additive data and history.
Migration Down does not resurrect cancelled runs or restart retired generators.

## Risk

- A seeded false value is indistinguishable from some deliberate choices;
  preserve it and require normal operator enablement to start processing.
- Old instances can write during upgrade; stop them before migration and verify
  the matching runtime contains no legacy generation or feedback writer.
- Removal can break history or replay; verify real historical fixtures, existing
  request hashes, supported terminal versions, and scoped operator readers.
- Disabling can expose old snapshots; keep topic-only reads and verify ordinary
  evidence fallback with zero legacy generation calls.

## Verification

Issue #530 records the approved historical deterministic 1k waiver before code.
The preserved evidence-only seed remains unchanged. Replacement evidence retains
the frozen organization/source/judgment hashes, runs three fresh configured-model
maintenance repetitions and actual Recall/community/manager cohorts, and requires
zero false consolidation, full source/fact retention, no Bad@K increase, lower
duplicate repetition, and the existing paired Recall p95 gate. Real PostgreSQL
proves retirement atomicity, setting preservation, history, RLS, erasure, replay,
and active-team enrollment. Registered production-entry scenarios and successful
final-head full CI/production-image E2E remain mandatory.
