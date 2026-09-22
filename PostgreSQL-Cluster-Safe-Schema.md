# PostgreSQL Cluster-Safe Schema

The current runtime uses one writable PostgreSQL primary. The schema carries
explicit tenant keys and transaction boundaries so isolation is enforced in
the database as well as in application authorization.

This page describes current constraints and query mechanics. It does not
describe an enabled sharded or replica-read deployment.

## Outcome And Boundary

Startup inspects PostgreSQL recovery/read-only state and distributed-extension
indicators. It rejects standby/read-only connections, Citus/pg_shard indicators,
and separate replica-read configuration. A deployment must route the server to
the writable primary.

PostgreSQL remains authoritative for semantic, workflow, security, and audit
state. pgvector executes inside that database. Redis is not a tenant
authorization boundary.

## One-Team Transaction Invariant

Normal semantic operations enter storage with a fixed authenticated team and
owner context. Their writes do not span multiple teams. Control and scheduler
operations use explicit, narrow system/team ports and bounded operations.

Tenant-owned foreign keys include the team and, where required, the owner or
memory space. This prevents an otherwise valid UUID from binding data to an
unrelated tenant or owner.

## Tenant-Owned Key Contract

Canonical tables use composite tenant keys such as
`(team_id, relationship_id)`. Evidence occurrence references also carry owner
identity where the database needs to enforce it. Space-owned references pair
`team_id` with `space_id`.

An Entity's normalized name is a lookup attribute, not a unique real-world
identity. Canonical Relationship identity additionally includes its author,
endpoints, predicate, polarity, scope, and applicable temporal identity.

Global control/reference data has its own access rules. It is not an excuse to
omit scope from semantic rows. Exact keys and indexes live in the migrations;
illustrative names are not replacement DDL.

## Join And Repository Contract

A tenant join retains tenant equality:

```sql
SELECT relationship.relationship_id, subject.entity_id
FROM relationship_records AS relationship
JOIN entity_records AS subject
  ON subject.team_id = relationship.team_id
 AND subject.entity_id = relationship.subject_entity_id
WHERE relationship.team_id = $1
  AND relationship.relationship_id = $2;
```

Actual reads also apply the operation's memory-space, generation, lifecycle,
support, and time predicates. Search adapters scope candidate IDs before
returning them; hydration reauthorizes canonical rows. Bound parameters carry
user values.

Current vector index SQL can contain a server-validated contract UUID and
validated index expression so PostgreSQL can match a partial index. Callers
cannot supply those SQL fragments.

## RLS And Worker Context

Repository transactions set `app.tx_mode`, `app.current_team_id`,
`app.current_profile_id`, and allowed-space context with transaction-local
settings. Pool reuse must not retain an earlier request's identity.

Tenant tables use RLS and explicit scope predicates. Same-team read visibility
does not permit cross-owner writes. System and migration modes are separate
from request-selected inputs and are used only through their authorized
application boundaries.

Workers claim and finish work through their owning capability ports. A system
scheduler may enumerate teams, but it cannot convert a request's arbitrary
team argument into system authority.

## Space Keys And Generation Fences

Private spaces have an active generation. Erasure seals a space and advances
the generation; writes prepared against an earlier generation fail rather than
resurrecting deleted content.

Reads filter active authorized generations. Cross-space Entity reuse,
duplicate reuse, and support links are invalid. Private-bound recall searches
shared and permitted private branches independently.

Startup checks the compiled private-erasure manifest against database
space-owning/reference tables and deletion ordering. A missing classification
blocks startup. Details belong in
[Data Model And Storage](Data-Model-And-Storage#private-memory-lifecycle-and-erasure).

## Search And Embedding Shape

Search documents carry source/version/hash identity plus embedding contract and
dimensions. Vector dimensions must match the contract. Index generations
declare strategy, expression, operator class, and compatible physical index.

A search result is an authorized candidate, not canonical proof. Final
hydration checks current or requested temporal support/lifecycle state.
Index construction and approximation limits are described in
[ANN, HNSW, And Halfvec](ANN-HNSW-And-Halfvec).

## Durable Queue And Scheduler Shape

Dream, community, conflict review, and private erasure retain durable run or
operation state in PostgreSQL. Claims carry lease/version/generation fences;
a worker cannot commit after another claimant or erasure invalidates its fence.

Remember and correction execute required server-owned work in the originating
call. Maintenance search reconciliation is a separate bounded operation.
Redis signals can coordinate work but cannot replace its durable record.

## Migrations And Validation

The server applies ordered migrations under a PostgreSQL session lock before
serving. Release directories are migration inputs, not competing runtime
authorities. Deployed migrations remain immutable.

A schema change needs lock/rewrite analysis, RLS and constraint review, real
PostgreSQL tests, and a defined recovery or irreversible boundary. Verify
concurrent startup and the exact supported deployment topology.

## Risks And Mitigations

| Failure mode | Mitigation |
| --- | --- |
| A UUID from another team satisfies an ID-only reference | Composite tenant/owner/space foreign keys. |
| Pooled connections retain a prior actor | Explicit transactions with local authorization settings. |
| Search filters only after broad IDs escape storage | Scope candidate queries and reauthorize hydration. |
| A stale worker writes into erased private memory | Active-generation checks and fenced completion. |
| A schema feature is mistaken for distributed runtime support | Enforce the single-primary startup gate and document supported topology. |
