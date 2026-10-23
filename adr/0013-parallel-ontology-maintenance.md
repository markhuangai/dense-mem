# ADR 0013: Parallel Ontology Maintenance

- Status: Accepted
- Date: 2026-10-21
- Supersedes: ADR 0008's automatic rejection solely for an unrelated catalog revision change
- Refines: ADR 0003, ADR 0004, ADR 0008, ADR 0010, ADR 0012
- Maintainer approval: issue #593 and its directly approved implementation plan

ADR 0014 supersedes token reservations as a spending/admission requirement.
Concurrency, fair rotation, source exclusion, publication fencing and historical
accounting preservation remain in force under its bounded transition.

## Decision

Operators configure a deployment per-team concurrency default of one and bounded
team overrides through the existing control portal and app-config API. Limits
are 1–8, bounded by global capacity, and frozen with the next window's policy.
Historical window policies remain immutable and missing values mean one.
Managers cannot select execution policy. Pause and disable stop new admission
and drain dispatched work while preserving attempt usage and accounting history.

The existing team lease serializes bounded discovery and batch preparation.
Live batch source identities, including full grouping closures, are excluded
from other batches. Admission atomically transfers the preparation slot to
the batch lease before provider work. Preparation, batch, and projection work
share team and global capacity under the existing accounting lock and fair
rotation. Expired workers cannot publish or overwrite newer source revisions.

Automatic organization publication retains the catalog lock, complete response
validation, source fingerprints, relevant context and dependency checks, record
revision checks, manager overrides, atomic publication, and immutable receipts.
Only after those assessed inputs remain current may it use the locked current
catalog revision for publication. Manager publication still requires its exact
expected revision. Relevant changes reject the complete automatic result.
No canonical Knowledge state is written by ontology maintenance.

## Risk

- Overlapping claims could duplicate work; serialize preparation and transfer
  ownership atomically, excluding complete live batch closures.
- Stale results could overwrite corrections; verify all assessed inputs under
  the catalog lock and retain record-version and override rejection.
- Shared capacity could be exceeded; count preparation, batch and projection
  leases together and enforce atomic admission within both capacity limits.

## Verification and Recovery

Issue #593 records the explicit pre-implementation evaluation waiver and its
replacement real PostgreSQL, production-entry E2E, permanent CI and final-head
full CI/production-image E2E requirements. It does not waive other issues' gates.
Tests prove parallel execution, limits, source exclusion, fair rotation,
publication races, lease recovery, window activation, allowance races, draining,
forced RLS, and canonical preservation.

Pause, drain and stop old workers before upgrade or rollback. The new migration
changes only the operational admission index and retains durable history.
Down refuses active work; recovery may disable maintenance and roll forward.
