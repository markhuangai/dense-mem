# ADR 0021: Relational Topics and Offline Upgrades

- Status: Accepted
- Approval: [#835](https://github.com/markhuangai/dense-mem/issues/835)
- Supersedes the active storage and manager publication clauses of ADR 0008,
  the pre-v2.8.0 startup upgrade clauses of ADR 0011, the catalog lock
  requirement of ADR 0013, the Topic payload and permanent layout fence
  clauses of ADR 0018, and the separate current Community parent of ADR 0020.

## Decision

PostgreSQL stores current Topics, direct Evidence memberships and Evidence
equivalences in relational tables scoped by team, shared space and generation.
Topic definition edits compare per-record versions. Assignment commits lock the
affected Evidence and verify their Topic revisions. Hierarchy changes hold a
scoped lock while checking for cycles. Automatic placement checks the child and
parent versions before changing the effective parent.

At cutover, select the newest settled placement whose child and parent versions
are current. A current standalone placement clears the effective parent. Use
the declared parent when there is no such placement. Keep historical
declarations and placement outcomes with their original scope and version.

Manager Save at `POST /ui/api/topics/edits` accepts the bounded edit list,
operation key, reason and per-record expected versions. PostgreSQL commits
edits, history, audit and replay together. A stale version returns 409.
Migrated drafts remain readable by their owners. The legacy mutation routes
authenticate and authorize before replaying a matching committed request.
Reusing a key with different content returns 409. New legacy commands return 410.

## Upgrade and recovery

Stop all application services, verify quiescence and take a verified backup
before running migrations. Goose supports fresh installs and upgrades from
released v2.8.0 onward, including interrupted in-range migrations. Databases
with data below that floor must first upgrade through v2.8.0. Preserve the
deployed migrations and in-range data executors.

Run the bounded and resumable copy under the migration lock and a temporary
database write fence. Reconcile source and destination row identities, counts
and fingerprints before removing the fence and obsolete storage settings.
Recover from the verified backup before reopening writes. Use forward repair
once writes reopen. Keep private-copy erasure, legal holds, lineage and
immutable Evidence and replay receipts.

## Risk

- A stale placement could reintroduce a retired or moved parent. Test stale,
  standalone and crossed hierarchy edits with effective endpoint versions.
- An interrupted copy could leave history missing or unblock an old writer.
  Test resume and fence enforcement on populated databases and compare
  identities and fingerprints before cutover.
- An erasure could retain content derived from a migrated private copy.
  Exercise migrated owners and a distinct unrelated owner against real RLS,
  legal hold and audit data.
