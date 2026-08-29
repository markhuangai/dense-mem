# ADR 0004: Prefer the Simplest Sufficient Design (KISS)

- Status: Accepted
- Date: 2026-08-29
- Supersedes: None
- Related: ADR 0003

## Context

A correct change can become harder to reason about when implementation and
review add wrappers, configuration, state, branches, and extension points for
requirements the system does not currently support. Repeated local fixes can
also preserve a complicated design when replacing or deleting that machinery
would satisfy the same contract more safely.

Short code is not automatically simple. A shortcut that hides failures,
duplicates policy, weakens authorization, or skips durable-state and concurrency
requirements moves complexity into production behavior instead of removing it.

## Decision

New and changed implementations use the least complex design that fully
satisfies current supported behavior, accepted architecture, and required
failure handling:

- Prefer deletion, reuse of an existing authoritative owner, or narrow local
  logic before adding a new abstraction or subsystem.
- Every new interface, wrapper, configuration value, durable state, background
  process, generalized extension point, or test framework must serve a concrete
  current requirement that a simpler design cannot meet.
- Do not build for hypothetical reuse, future providers, unsupported states, or
  generalized best practices without an accepted requirement.
- Validate a review finding separately from its proposed remedy. A real defect
  with an overengineered suggestion receives the smallest sufficient verified
  fix, not the suggested machinery.
- When review fixes create second-order findings or reviewer-only machinery,
  simplify or replace the changed implementation before adding more patches.
- If the simplest correct solution changes the accepted contract or architecture,
  return to planning; do not silently expand the pull request.

KISS does not authorize unrelated cleanup or weaken required security,
isolation, typed errors, durable history, transactions, concurrency, public
contracts, or real-logic proof. ADR 0003 continues to govern ownership of
authoritative logic; local simplicity cannot create a second source of truth.

## Consequences

Changes should have fewer independent concepts and smaller failure surfaces.
Review may reject a technically plausible hardening suggestion when it has no
current supported consequence, and may replace accumulated fix machinery with a
simpler in-scope implementation. A complex solution remains acceptable when a
current invariant requires it and the simpler alternatives are shown to fail.

## Risk

- KISS could be used to dismiss a rare but supported security, data-integrity,
  or concurrency path. Require a concrete supported-path analysis before
  classifying the path as unnecessary.
- Local shortcuts could duplicate authoritative policy. Apply ADR 0003 and
  reuse its cohesive owner through the narrowest dependency-safe boundary.
- Simplification could become an unrelated rewrite. Limit it to changed,
  ticket-owned behavior and return to planning before changing the accepted
  architecture or contract.

## Verification

For each non-mechanical design or review fix, compare deletion or reuse, a
narrow local change, and any proposed new machinery. Record why the selected
design is the simplest one that satisfies the current invariant. Verify the
chosen behavior with the smallest real-logic test at the owning boundary, then
run the required integration and end-to-end gates for the affected contract.
