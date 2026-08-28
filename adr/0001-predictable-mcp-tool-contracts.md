# ADR 0001: Predictable MCP Tool Contracts

- Status: Accepted
- Date: 2026-08-28

## Context

Dense-Mem is commonly called by language-model hosts that must decide what to
do from one MCP result. A successful acknowledgement that leaves required work
to an asynchronous worker makes the real outcome easy to miss: assessment,
embedding, policy, or commit may fail after the caller has left, and a less
capable client may not poll the right status tool or interpret every state.

Model-backed processing cannot promise byte-identical answers. Dense-Mem can
still make its contracts, server-owned decisions, bounds, and outcomes
predictable.

## Decision

Every supported MCP tool must have a bounded and predictable contract:

- `tools/list` and `tools/call` use the same versioned registry, strict schema,
  feature gates, and visibility rules.
- Inputs, provider calls, repair attempts, execution time, result size, and
  pagination are explicitly bounded where applicable.
- Deterministic validation and server policy own authorization, durable state,
  lifecycle, status, support, idempotency, and error classification.
- Required server-owned work finishes in the originating call. The call returns
  a complete terminal result or a typed, bounded, actionable error; it does not
  return success that requires blind status polling to discover the real result.
- A mutation defines replay behavior. A matching idempotent retry reuses the
  authoritative result, while a different request using the same key conflicts.
- Provider variability is allowed only inside a closed response schema with
  complete validation, bounded regeneration, and zero partial acceptance.
- An explicit intermediate result is allowed when progress requires new caller
  input, such as selecting a correction candidate. It is not allowed merely to
  wait for more server-owned processing.

Predictable does not mean that a read returns the same data after authoritative
state changes, or that model-proposed content is byte-for-byte reproducible. It
means the client can rely on the tool's schema, bounds, state semantics, and
next action without guessing about hidden required work.

The asynchronous `remember` receipt and `get_submission_status` tool are a
temporary compatibility path while Remember moves to synchronous terminal
processing. During that transition, changes may preserve the existing path but
must not introduce a new asynchronous public tool or widen the polling
contract. Completing the transition removes status polling from the supported
contract.

## Consequences

Clients receive required failures in the call that caused them and can use
typed remediation without orchestrating a hidden worker lifecycle. Mutating
calls may take longer, so they need explicit deadlines, cancellation, and
idempotent retry behavior. Provider success alone is never enough to report
success; deterministic policy and authoritative commit must also finish.

## Risk

- A synchronous provider path can exceed a client's deadline. Bound every phase,
  honor earlier cancellation, and commit no canonical state after cancellation.
- Reviewers could report the known polling path as a new defect. Treat the
  bounded transition as accepted and report only introduced or worsened
  violations.
- "Predictable" could be misread as accepting malformed provider output to
  stabilize results. Fail closed on invalid complete responses; never coerce,
  splice, or silently fall back.

## Verification

For every changed MCP behavior, verify the catalog and call schemas, bounded
success and error results, cancellation, idempotent replay, and removal of
hidden required work. Model-backed writes also require invalid-response and
provider-failure cases proving zero partial semantic commit.
