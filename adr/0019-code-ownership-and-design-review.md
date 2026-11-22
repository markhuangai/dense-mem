# ADR 0019: Code Ownership and Design Review

- Status: Accepted
- Date: 2026-11-22
- Supersedes: ADR 0003, ADR 0004, ADR 0005, and ADR 0006

## Context

The checked architecture inventory kept Go and browser units, visibility, and
worker registrations in sync with source. It checked repository import edges,
but it could miss policy in the wrong function, an external driver imported by
a service, or a standard mechanism implemented twice. Maintaining the
inventory and its custom discovery tools added work to each structural change.

## Decision

- Give each authoritative rule, default, validation, state transition, and
  error classification one cohesive owner. Keep use-case policy in the
  owning application capability and shared pure policy in the narrowest
  suitable domain package. Reuse it through the narrowest dependency-safe API
  or consumer-owned port. Split responsibilities with independent reasons to
  change when they force edits or tests to move together.
- Keep dependencies directed from transport through application services to
  domain and ports. Transports check request shape and translate results.
  Services orchestrate policy. PostgreSQL, Redis, and provider adapters
  implement ports and own infrastructure behavior, including SQL,
  transaction mechanics, and translation of driver errors. Keep concrete
  adapters private. PostgreSQL adapters may use domain, ports, and shared
  PostgreSQL infrastructure, which stays private to those adapters and
  composition. Composition wires private implementations. Other capabilities
  consume explicit public contracts. Keep one live implementation of each
  capability and remove forwarding facades once their callers have moved.
- Choose the simplest implementation that preserves supported behavior,
  accepted invariants, and failure handling. Consider deletion and reuse of
  the existing owner, standard library, and installed packages before adding
  code. Where they leave a gap, verify the API, compatibility, maintenance,
  and trade-offs of a maintained dependency. Give new interfaces, wrappers,
  configuration, state, workers, and other custom machinery a concrete need.
  Similar syntax can serve different policy. Keep unrelated logic with its
  owners and use the smallest remedy that resolves a verified defect. Replan
  when that remedy requires changing an accepted contract.
- Retire the capability ownership inventory, its worker registrations, and
  the custom dependency and discovery checker. Review ownership, dependency
  direction, and reuse in the existing correctness-and-design AI PR review.
  Assess changed code through real callers, decisions, and consumers. A
  design finding identifies the changed code, a reachable path, a concrete
  cost or failure, and a simpler compatible option. Keep existing review
  dispositions based on verified impact.

## Verification

Trace the changed rule and error paths to their owners and named callers. Use
the smallest real tests of supported success and adverse behavior, including
the affected integration boundaries. Existing CI continues to exercise builds,
security, PostgreSQL, browser and worker behavior. A dependency can compile and
escape contextual review. Behavioral tests establish the guarantees they
exercise, including isolation, transactions, cancellation and shutdown.
