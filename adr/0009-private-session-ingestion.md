# ADR 0009: Private Session Ingestion

- Status: Accepted
- Date: 2026-10-10
- Supersedes: None
- Refines: ADR 0001, ADR 0003, ADR 0004, ADR 0007
- Maintainer approval: issue #218 and its approved implementation plan

## Decision

`ingest_session` accepts only exact user-authored events in the authenticated
private binding. External framework, application, user, and session identifiers
are provenance, never authorization. Remember continues to require client
Relationship proposals under ADR 0007.

Session intake atomically preserves immutable events and processing intent before
provider work. A changed event rejects the complete batch before staging. An
unchanged-request retry retains its original key, payload, and validated extraction
checkpoints. A different key cannot claim an unfinished event.

Dense-Mem segments long events and performs bounded, closed-schema extraction.
Window proposals are combined and their entity references reconciled before one
authoritative assessment. Only exact current-event excerpts become submitted
evidence. Earlier session context is read-only and cannot become new evidence.
Providers propose; the existing server assessment and lifecycle policy decide.

All required extraction, assessment, and embedding work completes before the
semantic transaction. Accepted evidence, lifecycle/history, relationships, search
documents/vectors, provenance links, and the terminal receipt commit together.
A required failure commits no new semantic knowledge from the request. Exact raw
intake remains durable for unchanged-request recovery and private erasure.

Processing uses the existing bounded synchronous deadline, without polling or
background readiness. The feature is opt-in through `SESSION_INGEST_ENABLED`.
Rollback disables intake and retains compatible schema/data. New private tables
participate in generation fencing, erasure, retention, and legal holds; deploy the
schema with the matching erasure manifest and stop older incompatible instances.

## Risk

- Separate window assessments could create duplicate entities. Reconcile draft
  references and assess one request-wide proposal set, including homonym tests.
- A late failure could leave partial searchable knowledge. Keep provider work
  outside transactions and commit the complete semantic result and receipt once.
- A stale retry could revive erased data. Reauthorize the private generation and
  all referenced evidence at commit and erase raw intake/checkpoints with the space.

## Verification

Issue #218 records the approved historical 1k waiver before implementation. Its
replacement is a frozen 32-case session-quality cohort run three times with real
providers, twelve Remember base/candidate controls, exact source/boundary and
isolation checks, latency/token reporting, real PostgreSQL atomicity and erasure
tests, registered production-entry E2E, and final-head full CI/E2E. The issue stays
open until code, wiki publication, and exact merged-main verification agree.
