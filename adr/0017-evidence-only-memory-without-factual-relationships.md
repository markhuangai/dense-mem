# ADR 0017: Evidence-Only Memory Without Factual Relationships

- Status: Accepted
- Date: 2026-11-09
- Supersedes: ADR 0007's client Relationship proposal and citation-coverage requirements; ADR 0008's canonical graph source kinds and Relationship-backed community projections; ADR 0009's factual Relationship extraction and commit requirements; ADR 0010's Relationship handles and graph-specific history preservation; ADR 0015's Relationship reranking; ADR 0016's entity-class and predicate-concept semantics when their canonical graph sources are removed
- Refines: ADR 0001, ADR 0002, ADR 0003, ADR 0004, ADR 0005, ADR 0006, ADR 0011, ADR 0013, ADR 0014
- Maintainer approval: [issue #681 decision](https://github.com/markhuangai/dense-mem/issues/681#issuecomment-6751188056)

## Decision

Proceed with C: clients submit exact memory Evidence and its provenance to an
explicit authenticated destination. Remove the factual subject-predicate-object
Relationship model completely, including client proposals, server extraction,
acceptance and support machinery, graph reads, public handles, and dependent
runtime and storage. Remove Entity, typed Value, and registered-predicate
machinery whose purpose is that factual graph.

Ontology-lite organizes Evidence directly through topics, Evidence topic
membership, and Evidence equivalence. Recall must expose useful related-topic
hints and bounded follow-up Evidence retrieval without Relationships. Topic
membership and organizational hierarchy remain derived metadata; they cannot
establish factual support or generate new submitted Evidence. Preserve current
team-shared eligibility, private-memory exclusion, freshness checks, ownership,
manager overrides, and generation fences.

The maintainer also chose deletion of obsolete factual graph data and
graph-specific history from PostgreSQL. A permanent graph archive or historical
graph reader is not part of the target. Preserve original Evidence, exact
occurrences, source revisions, provenance, Evidence lifecycle and conflicts,
privacy obligations, and unrelated security and audit history. Removing the
fact model does not authorize rewriting original Evidence into extracted facts.

Issue #681's historical A/C campaign measured equal Recall@10 and complete-support
retrieval on its frozen cohort. B's quality comparison remained inconclusive.
Choosing C favors the simpler product model; it does not establish that factual
Relationships cannot help any workload. The production refactor requires fresh
quality, discovery, isolation, migration, and performance verification.

## Transition and recovery

Existing graph readers, writers, schema, and contracts remain during planning
and only until a separately approved refactor is implemented and cut over.
Do not expand those paths or treat them as the completed target. The refactor
plan must enumerate removals, the breaking contract and client update, treatment
of historical operation keys and mixed receipts, the migration's irreversible
boundary, and recovery from a verified pre-cutover backup. Deployed migrations
remain immutable. This decision record does not execute a database migration
or authorize deployment.

### One-time ontology reset at the pending cutover

On 2026-11-13, after a production-copy staging run spent more than 50 minutes
in selective ontology dependency closure, the maintainer explicitly chose to
reset all derived organization during migration `20261110010000` and rebuild it
from retained Evidence. This narrowly supersedes ADR 0008's stable organization
identity, immutable organization history, and override preservation at that
cutover. It does not change those guarantees for ordinary v2.8 operation.

The pending migration atomically clears the catalog, publications, records,
dependencies, assessments, drafts, source queue, and dirty markers through an
explicit table inventory without cascading deletion. Existing topics, IDs,
manual assignments, overrides, and organization history are not preserved.
Preserve original Evidence and its fingerprints, provenance, lifecycle,
conflicts, legal holds, unrelated security/audit history, saved maintenance
settings and pause state, and maintenance usage accounting. Unexpected foreign
keys or applicable holds block the transaction. Keep the existing stopped-service
and verified-backup recovery boundary.

Reset discovery cursors and leases so normal maintenance rediscovers existing
eligible Evidence in active team-shared spaces and current generations; clients
need not resubmit it. Private and unavailable sources remain excluded. Rebuild
requires enabled, unpaused maintenance and model capacity, runs after startup,
and can produce different organization. It is not part of migration completion.
Already-applied cutovers skip this migration and retain newly built organization.
The maintainer authorized this exact amendment of the pending migration; the
migration-history guard binds its previous and replacement file hashes. Other
deployed migrations remain immutable, and this does not authorize production
deployment or restore.

## Risk

- Graph removal can sever Evidence lineage or erase source records through
  dependencies. Use an explicit deletion inventory, preserve Evidence identities
  and hashes, and prove the migration against populated PostgreSQL fixtures.
- Evidence retrieval can stay stable while related discovery disappears. Require
  actual topic hints and follow-up retrieval from a database with no fact graph,
  including retraction, stale-membership, and isolation cases.
- Old receipts can misrepresent deleted graph state or a retry can duplicate
  accepted Evidence. Define strict contract retirement and preserve scoped
  operation-key/hash identity without retaining a graph replay implementation.
