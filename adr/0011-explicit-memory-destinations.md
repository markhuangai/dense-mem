# ADR 0011: Explicit Memory Destinations and Membership-Owned Private Memory

- Status: Accepted
- Date: 2026-10-14
- Supersedes: ADR 0001 and ADR 0007 retry guarantees for historical Remember requests without an explicit target; ADR 0009's credential-binding scope and preservation of checkpoints assessed against mixed private context; ADR 0010's Remember replay guarantee for requests without an explicit target
- Refines: ADR 0002, ADR 0003, ADR 0004, ADR 0008
- Maintainer approval: issue #573 and the directly approved implementation plan

## Decision

Contract `dense-mem.v2.7.0` requires `remember.target_space` to be `private` or
`team_shared`. One destination applies to the whole submission. The calling
client chooses the destination; Dense-Mem resolves its actual team, owner, space,
and generation from authentication. There is no default, provider classification,
or redirection of a denied shared write to private memory. The target participates
in the request hash, so changing it under the same operation key conflicts.

Every API credential owns a separate private space through its API-client team
membership, including credentials issued by an SSO user. SSO sessions and OAuth
tokens use their SSO user/team membership's private space. Private spaces expose
one public kind, `private`, and use `owner_membership_id`; semantic ownership
aliases and immutable authorship remain distinct. Rotation preserves the
credential identity and space. Expiry does not erase history, and no operation
may reactivate a revoked identity.

Authenticated team-read-only callers can read, remember, correct, retract, and
erase their own private memory. Shared mutation requires current shared write
permission and the existing semantic ownership checks. Destination-aware policy
has one owner and is enforced before provider work and at authoritative commit.
MCP discovery and invocation use the same capabilities. `ingest_session` stays
private-only and remains gated by `SESSION_INGEST_ENABLED`.

Credential/session projections expose effective `memory_access` for private
and team-shared read/write. Credential binding inputs are rejected explicitly,
and both portals show these capabilities without a binding selector.

## Historical Data and Compatibility

The prerelease already used the v2.7.0 contract string. Clients must refresh
`tools/list`; that string alone cannot identify the request shape. Historical
Remember requests without a target fail validation even on retries. Preserve
their exact requests, hashes, receipts, evidence, and history without inventing
a target or bypassing idempotency.

Migration runs in a maintenance window with old instances and workers stopped.
A read-only preflight reports a versioned source fingerprint, ownership mappings,
support-copy and handle requirements, table coverage, and blockers without memory
text or credentials. Missing or ambiguous immutable writer attribution blocks
migration. New ordered migrations use bounded resumable processing, preserve
deployed migration history, and prevent serving any incomplete result.

Split legacy private history by original writer. Keep existing isolated space
IDs where possible and leave shared history shared. Copy only the supporting
closure necessary for records whose writers previously shared the same legacy
private space. Such copies retain original authorship, authority, and provenance
and never count as independent corroboration. This exception never permits
copying, reading, or citing a separate private space.

When IDs must change, resolve old evidence and Relationship handles only through
owner/destination/generation-scoped mappings and reauthorize the destination.
Keep irreducibly mixed artifacts sealed and non-queryable. Rebuild scope-dependent
search, graph, and conflict projections; reuse embeddings only when source and
model contracts match. Preserve session intake/receipts, but recompute unfinished
checkpoints assessed against mixed context using their original operation identity.

Supporting copies, mappings, and retained source lineage participate in retention,
legal holds, and erasure. Carry holds forward and fence stale jobs/retries. Runtime
code cannot recover erased content from migration history.

Erasing an original writer's private memory erases every migration copy of that
writer's records, including copies in another destination, and invalidates memories
that depend on the erased support. An applicable hold on any affected copy blocks
physical purge. This governed erasure does not grant the requesting writer normal
read or mutation access to another private space.

## Risk

- A writer or handle mapping can expose foreign private history; fail closed on
  ambiguous attribution and reauthorize mapped records within one destination.
- Supporting copies can inflate support or defeat erasure; preserve provenance
  and include every copy and recoverable source in the privacy lifecycle.
- Private-write capability can grant shared mutation accidentally; require current
  shared write permission plus ownership and verify revocation at commit.
- Partial migration or old workers can restore mixed context; stop old processes,
  resume bounded migration steps, and block startup until reconciliation passes.

## Verification and Recovery

Issue #573 requires real PostgreSQL A/B/C ownership, RLS, transaction, migration,
interruption, history, hold/erasure, and stale-generation tests; positive/adverse
production-entry scenarios; and the deterministic 1k base/candidate comparison.
After implementation and an attempted comparison, the maintainer explicitly
waived the remaining evaluation on 2026-10-14. Both imports stopped on the same
nonretryable historical security rejection; no complete quality scores exist.
The complete repository check, real PostgreSQL regressions, and all ten required
local production-entry scenarios passed. Independent plan conformance preceded
testing; full final-head CI and production-image E2E remain required before human
merge. This waiver does not claim retrieval-quality equivalence.

Before new writes, rollback restores the verified backup and matching binary with
instances stopped. After new writes, recovery rolls forward unless the maintainer
explicitly authorizes the destructive restore boundary. Down cannot discard
history or resurrect legacy bindings. Production migration is separately authorized.
