# ADR 0012: Operator Maintenance Allowances

- Status: Accepted
- Date: 2026-10-16
- Supersedes: None
- Refines: ADR 0003, ADR 0004, ADR 0008, ADR 0010
- Maintainer approval: issue #240 and the explicit approval of Add allowance now

## Decision

The control portal can add input/output token allowance to its named active
maintenance window. The original policy, dates, usage and outstanding
reservations remain intact. Additions are deployment-wide and do not change
team rotation, source order, model request limits or semantic decisions.
At least one amount must be positive; cumulative additions are limited to
one billion tokens of each kind per window. Additions expire with that window.

The existing maintenance command transaction owns the operation key, expected
window check, allowance counters, work requeue and required audit. Identical
retries return one stored result; changed requests conflict. Grant receipts are
immutable, and reservations and reconciliation use the original allowance plus
the recorded additions under the existing accounting lock.

A grant makes budget-deferred sources eligible for normal automatic maintenance
again while preserving their pending order. A batch completing after a concurrent
grant also retains that opportunity. Another budget refusal parks the source
until a further grant or window. Failed and ambiguous sources are not retried.
Disable, pause and provider-overrun stops remain effective. No grant dispatches
provider work directly or opens another window.

## Risk

- Retried requests could fund a window twice; serialize idempotent commands and
  commit counters, receipt and audit atomically.
- A stale portal could fund a later window; require its expected window ID and
  reject expired or mismatched windows.
- Concurrent completion could strand deferred work or spin; compare the claim's
  allowance snapshot and verify retry parking after the grant is consumed.

## Verification and Recovery

Real PostgreSQL tests cover concurrent grants/admission, immutable history,
audit rollback, late completion, rollover and automatic progress. Strict control
API and browser tests cover the public action. Existing frozen quality/latency,
repository, final-head CI/E2E and paired staging gates remain required.
Down refuses populated grant history; disable maintenance and recover forward.

For PR #571 only, the maintainer approved accepting the measured local Recall
latency failure: grouping +11.481814% and discovery +14.696983% against the 10%
median paired p95 limit. The [approval record](https://github.com/markhuangai/dense-mem/issues/240#issuecomment-6226504744)
preserves the failed measurements and requires passing correctness, final-head
full CI/E2E, and paired staging OFF/ON/OFF acceptance. This exception changes
neither the benchmark nor future performance gates.
