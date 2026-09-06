# ADR 0005: Capability Module Boundaries and Ownership Manifests

- Status: Accepted
- Date: 2026-09-06
- Supersedes: None
- Refines: ADR 0003, ADR 0004

## Context

The architecture checker previously kept one complete ownership document. That
document classified roles and dependency direction, but it could not express
module visibility and every capability migration had to edit the same file.
The resulting shared write surface makes independent, incrementally releasable
capability work difficult to review and easy to merge with stale ownership
metadata.

## Decision

The checked architecture is one combined graph assembled from independently
owned capability fragments under `architecture/modules/<capability>.json`.
Each fragment owns the exact Go and browser units, worker anchors, retained
exceptions, and completion records for one capability. The central manifest
owns repository-wide discovery entries, profiles, the role matrix, and the
deterministic fragment inventory. The loader rejects missing, duplicate,
unlisted, malformed, wildcard, and undiscovered entries; a fragment unit
inherits its fragment capability and declares explicit `public` or `private`
visibility.

The role matrix remains dependency-directed from transport to application
service to domain and ports. PostgreSQL storage is split into a private
`postgres_adapter` role and a private `postgres_infrastructure` role. A
PostgreSQL adapter may consume only domain, ports, and the explicitly classified
PostgreSQL infrastructure; application, transport, provider, and unrelated
adapter code cannot consume PostgreSQL infrastructure. Composition may construct
private units. A cross-capability import is otherwise allowed only when its
target is public; same-capability imports remain valid.

Each migration has one live implementation. Compatibility may remain only as a
bounded alias or single-hop forwarding path with named consumers and removal
owner #382. It must not copy policy, create a second authority, add a runtime
flag, or become a permanent dual-write or fallback path. When the named removal
owner lands, the corresponding exception or compatibility entry is deleted.

## Consequences

Capability changes can update their owned fragment without rewriting the full
inventory, while the loader still verifies one complete production and
evaluation graph. Public contracts can be shared deliberately, and private
implementation packages cannot become cross-capability dependencies by
accident. The central manifest and loader remain shared read-only infrastructure
for migration adopters.

## Risk

- A fragment could be omitted or duplicated and silently remove a package from
  enforcement. Require exact inventory reconciliation, fail on unlisted or
  missing fragments, and compare the merged graph with both Go profiles and the
  reachable browser graph.
- Marking an infrastructure unit public could legalize a policy bypass across
  capabilities. Keep concrete adapters and PostgreSQL infrastructure private,
  allow only the narrow adapter-to-infrastructure edge, and retain negative
  application, transport, provider, and adapter tests.
- A compatibility alias could become a second implementation or survive its
  removal owner. Record every consumer and issue owner in the fragment
  exception, and expire the entry when that issue is completed.

## Verification

Run the architecture conformance UAT and checker against the merged manifest.
The tests cover fragment inventory, duplicate/missing/unlisted fragments,
capability inheritance, visibility boundaries, PostgreSQL infrastructure
direction, completion and exception expiry, worker lifecycle obligations, and
the existing production/evaluation/browser/worker discovery paths.
