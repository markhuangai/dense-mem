# ADR 0002: Public-Behavior End-to-End Assurance

- Status: Accepted
- Date: 2026-08-28

## Context

Dense-Mem does not have a separate human acceptance-testing population. Its
supported behavior is therefore accepted through automated evidence. Unit and
package tests are necessary, but they cannot prove that production entry
points, composition, authentication, persistence, provider boundaries, and
serialization work together.

A success-only end-to-end scenario also misses the cases most likely to leave a
client with an unsafe or misleading result.

## Decision

Every new or changed externally observable behavior with a supported local
production-entry harness must have both:

1. a positive Compose or UAT case that proves the intended result; and
2. a distinct adverse case that proves the applicable rejection, permission
   denial, boundary, provider failure, cancellation, recovery, or regression
   behavior.

This rule covers public MCP and HTTP contracts, browser behavior, security and
isolation boundaries, data lifecycle, and provider-integrated behavior. The
case must enter through the supported production surface and use real
dependencies where the behavior depends on them. A deterministic local HTTP
provider fixture may replace an external model service; it must not replace the
server validation or domain policy being tested.

Existing scenarios count when their setup and assertions exercise the changed
path. One scenario may prove more than one behavior, but every behavior must be
mapped to concrete positive and adverse assertions.

End-to-end proof complements rather than replaces narrower evidence:

- RLS, migrations, constraints, transactions, locks, idempotency, `pgvector`,
  concurrency, and cross-profile behavior require real PostgreSQL-backed tests.
- Pure policy and validation use the narrowest real-logic layer.
- Semantic quality, ranking, placement, embeddings, search documents, migration
  placement, or performance changes retain their deterministic evaluation gate.

Documentation-only changes, test-only changes, behavior-preserving refactors,
and internal changes with no affected public invariant do not require a new
end-to-end scenario.

## Consequences

Pull requests that change public behavior carry an explicit scenario map and
stronger release evidence. The suite grows only with supported behavior, not
with every internal branch. Focused scenarios make failures diagnosable, while
the full Compose gate protects composition and cross-feature behavior.

## Risk

- End-to-end cases can become slow or brittle. Use deterministic fixtures,
  bounded waits, production entry points, and assertions on behavior rather
  than timing accidents.
- A nominal positive and negative pair can become vacuous. Require each case to
  fail if the changed behavior is missing or incorrect.
- End-to-end coverage can hide a wrong lower-layer implementation. Keep the
  focused real-logic and PostgreSQL requirements for the boundaries they prove.

## Verification

For each changed public invariant, review the linked issue, production paths,
test registration, fixtures, and assertions. Require a focused positive and
adverse scenario, then run the repository-prescribed focused and full gates.
