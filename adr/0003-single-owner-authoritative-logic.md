# ADR 0003: Single Owner for Authoritative Logic

- Status: Accepted
- Date: 2026-08-28

## Context

When the same rule is implemented in multiple packages, the copies can diverge
while still compiling and passing isolated tests. Broad packages can also mix
unrelated responsibilities, so one policy change forces edits across transport,
application, provider, storage, and composition code.

Blind deduplication is not the answer. Similar syntax may represent different
boundary policy, and a generic shared package can create worse coupling than a
small local conversion.

## Decision

Every authoritative rule, default, validation policy, decision table, state
transition, and error classification has one cohesive owner:

- Capability-specific application policy stays with the capability that owns
  the behavior.
- Pure policy shared by multiple capabilities belongs in the lowest cohesive
  domain package permitted by the dependency direction.
- Consumers reuse the owner through the narrowest dependency-safe API or
  consumer-owned port. They do not copy the decision or add a second default.
- Transports bind, validate transport shape, and translate results; they do not
  reimplement application or domain policy.
- PostgreSQL, Redis, and provider adapters implement ports and infrastructure
  mechanics; they do not decide domain policy.
- A unit that combines responsibilities with independent change triggers and
  concrete edit or test coupling must be split at that responsibility boundary.

Do not extract code merely because it looks similar. Keep logic separate when
its semantics, authority, or change trigger differs. Do not create a generic
`utils`, `common`, or parallel framework to host unrelated reuse. Prefer an
existing owner, a narrow private extraction, or deletion of the duplicate.

## Consequences

A supported policy change normally edits one owner plus affected callers and
tests. Package placement follows meaning and dependency direction rather than
file size. Review must distinguish duplicated authority from harmless
boundary-specific representation.

## Risk

- Centralization can create a shared god package. Require one cohesive
  capability or domain owner and reject unrelated utility accumulation.
- An extraction can reverse the dependency direction or broaden an API. Keep
  the owner at the narrowest permitted boundary and use consumer-owned ports.
- Reviewers can mistake similar syntax for duplicate policy. Prove that the
  sites implement the same authoritative decision and change for the same
  reason before requiring reuse.

## Verification

Trace each changed rule from inputs and writers through validation, policy,
persistence or serialization, and every consumer. Confirm one authoritative
owner, dependency-safe reuse, and tests at that owner plus the affected
integration boundaries. Continue running the deterministic architecture
ownership and forbidden-edge checks.
