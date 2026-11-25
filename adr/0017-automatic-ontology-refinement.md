# ADR 0017: Automatic Ontology Refinement

- Status: Accepted
- Date: 2026-11-08
- Supersedes: ADR 0016's prohibition on explicit automatic updates to unpinned semantic parent links only
- Refines: ADR 0008, ADR 0013, ADR 0014, ADR 0016
- Maintainer approval: issue #711 and its directly approved implementation plan

## Decision

For entity and predicate sources, automatic classifications are preferences.
Manager classification overrides remain locks. Selection precedence remains
manager override, eligible current assignment, then coarse vocabulary match.
Topics and equivalence retain their existing policy. The bounded organization
vocabulary supplies alternatives without adding a worker, budget, or Recall path.

Organization policy `ontology-organization-v2` uses provider schema
`ontology_organization_v2`. A complete response contains `hierarchy_updates`.
An explicit update may change only the parent ID and relation of a supplied,
current, unpinned entity class or predicate concept that is preferred or selected
by a compatible non-ambiguous assessed source. A bounded reason must justify the
change from supplied context. Pins, kind and EntityKind compatibility, source
eligibility, references, cycles, depth, leases, revisions, and manager overrides
remain authoritative. Missing explicit updates preserve all accepted parent
meaning, including legacy organizational links. A person's name does not justify
inferring a role. Unsupported refinement reuses the preference or records
ambiguity while retaining an eligible assignment.

The entire response is validated before one atomic publication. Definition
ancestry and applicable pins enter the assessed context and freshness identity.
Required ancestry that cannot fit the existing bounds produces an explicit
bounded outcome. In-batch dependencies refresh with changed definitions.
Historical receipts remain unchanged and readable; v2 identities prevent v1
assessment reuse. Unchanged successful v2 work reuses its receipt without another
provider call. Provider failures remain failures. Canonical Knowledge is unchanged.

## Risk and recovery

- A pin or correction during provider work could be overwritten. Snapshot pins
  and overrides and recheck assessed state under the publication lock; reject the
  complete result on a relevant change.
- An unrelated hierarchy change could invalidate meaning. Restrict targets to
  the explicit allowlist and relevant sources, preserve every other field, and
  validate the combined graph and assignment dependencies atomically.
- Requeueing could repeat work or retry failures. The one-time Goose migration
  refreshes only eligible organized entity/predicate queue rows in active teams'
  current shared generations, advancing revisions and retaining receipts and
  assignments. Other statuses and historical generations remain unchanged.

Pause maintenance, drain work, and stop all old workers before migration. The
queue UPDATE locks eligible rows and policy DDL locks teams; lock acquisition is
limited to five seconds. There is no canonical rewrite or history backfill.
Down refuses to undo the refresh. Keep compatible readers and recover forward.

## Verification

Issue #711 requires real PostgreSQL and production-entry proof for refinement,
semantic links, legacy preservation, ambiguity, complete-response rejection,
pin/freshness races, replay, restart, lease recovery, A/B/C isolation, and
canonical preservation. Protected DEBUG assessment and HTTP exchange payloads
carry trusted operation, run, batch, assessment, attempt, and exchange identities.
WARN rejection, INFO durable completion, and ERROR failure observations retain
the existing capture limits and production TRACE default.

The frozen 1k comparison and three organization/matching cohort repetitions per
arm remain required. Numerical quality differences are reported, including all
regressions, without a delta gate. Completeness, provenance, native readiness,
and the existing cohort safety assertions remain required.
