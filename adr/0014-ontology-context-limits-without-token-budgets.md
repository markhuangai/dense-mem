# ADR 0014: Ontology Context Limits Without Token Budgets

- Status: Accepted
- Date: 2026-10-23
- Supersedes: ADR 0008 and ADR 0010's aggregate ontology token admission; ADR 0012's maintenance allowances; ADR 0013's token reservations as a spending/admission requirement
- Refines: ADR 0001, ADR 0002, ADR 0003, ADR 0004
- Maintainer approval: direct instruction on 2026-10-23 to remove overall ontology token budgets, focus on context windows, and update the ADRs; tracked in issue #617

## Decision

Ontology processing has no cumulative token-spending budget at deployment, team,
window or run scope. Do not replace global budgets with per-team quotas, optional
spending ceilings, refill buckets or automatic allowance grants. Correct
processing, convergence and reliable completion take priority over spending
controls. Corpus size and previously consumed tokens do not stop eligible work.

Token limits serve individual model requests. The complete request, including
instructions, schema, source/context data and regeneration history, must leave
room for the response within the configured model's context window and provider
request limits. Reuse the existing request-sizing and bounded batch owners.
Insufficient context produces the existing explicit oversized/bounded outcome;
it cannot silently truncate required evidence or weaken complete-response
validation. These limits describe what fits in a call, not how much work a team
may complete.

Keep bounded complete-response regeneration, deadlines and cancellation, existing
provider throttling/backoff, global/team concurrency, fair rotation and operator
enable/pause/disable controls. Preserve source exclusion, lease/generation fences,
current-input validation, atomic publication and canonical Knowledge isolation.
No replacement rate-control framework is required by this decision.

Reported usage, unavailable usage and terminal outcomes remain observable.
Unavailable usage is not zero consumption. Usage reporting and historical
accounting do not gate future work according to an aggregate allowance. A
terminal failure with unknown usage does not remain in-flight solely because a
spending reservation was retained; genuinely active work still owns its
coordination capacity until safely finalized or recovered.

## Transition

Issue #617 owns removal of budget enforcement, allowance controls and budget-only
deferral/requeue paths through their existing owners. Released code still has
these paths. That exception exists only until #617 completes the forward
transition and removes them; do not expand the budget subsystem in the meantime.
The standalone allowance-deadlock remedy in #591 is superseded by this removal.
Any supported transition retaining that writer must still address its defect.

Preserve immutable windows, grants, attempts, usage and audit history. Define
saved-configuration and active-work compatibility, recover genuinely unfinished
budget-deferred work without retrying semantic failures, and fence incompatible
writers. Do not rewrite deployed migrations, erase conservative historical
charges or silently reinterpret old windows. Existing cadence and concurrency
activation semantics need not change solely to remove spending limits.

Issue #605 owns evaluation orchestration. For the implemented target, readiness
requires actual discovery, pending work, in-flight attempts, search and projection
convergence; it does not require allowance funding or zero historical spending
reservations. Preserve failed/ambiguous outcomes and unknown usage in reports.
Already-pinned evaluations retain their recorded protocol unless the maintainer
explicitly changes it. This ADR is not a runtime migration or evaluation waiver.

## Risk

- A regeneration history can overflow the context window after the first call.
  Account for complete messages, schema and response headroom on every bounded
  attempt, and reject oversized work without partial semantic acceptance.
- Revalidation bugs can consume more provider work without a spending stop.
  Fix repeated work at its owner, retain bounded calls/concurrency and pause
  controls, and measure useful progress alongside usage and failures.
- Budget retirement can strand deferred sources or release an active call's slot.
  Verify forward recovery, durable terminal identity and concurrency fences while
  preserving historical receipts, unknown usage and canonical fingerprints.

## Verification and Recovery

The documentation change records the approved target. Implementation requires
real request-sizing/validation tests and PostgreSQL migration, restart, recovery,
concurrency, pause/disable and A/B/C-team isolation cases. Prove continued progress
beyond former aggregate caps, genuine context-limit rejection, terminal unknown
usage handling, and no duplicate provider dispatch or semantic publication.

Run registered positive/adverse production-entry E2E, repository checks and final
CI/E2E. The deterministic 1k comparison remains required unless #617 records an
explicit maintainer-approved waiver and replacement verification. Other issues'
waivers do not transfer. Measure progress and elapsed time; do not assume a gain.

Recovery may pause or disable maintenance and roll forward while preserving
history. Do not restore incompatible writers over the transitioned state or
erase records to make an older budget-based binary appear compatible.
