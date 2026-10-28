# Discovery receipt investigation (#633)

This protocol measures the existing definition-marker discovery path. It does
not introduce receipt reuse, change freshness or locking policy, or establish
historical-corpus throughput. Any optimization requires a separate approved plan.
The maintainer approved the investigation and its issue-specific deterministic
1k waiver in [the approval record](https://github.com/markhuangai/dense-mem/issues/633#issuecomment-6432071854).

## Workloads and observation

`TestOntologyMaintenanceRevalidationDiscoveryMeasurement` creates three fresh
PostgreSQL fixtures for each workload, using the real non-superuser adapter and
deterministic HTTP assessment provider. Every workload has 100 evidence sources.

| Workload | Distinct receipts | Sources per receipt | Terminal source states |
|---|---:|---|---|
| unique | 100 | 100 receipts of size 1 | 100 organized |
| overlap-64 | 64 | 52 of size 1, 1 of size 2, 8 of size 3, and sizes 5, 8, 9 | 75 organized, 10 ambiguous, 15 failed |
| shared-5 | 5 | 5 receipts of size 20 | 100 organized |

Receipts are created by the actual organization service. Queue setup uses their
durable results and the existing outcome policy; this is not a batch-completion
test. The harness drains setup discovery, then re-enqueues a definition marker
against unchanged inputs before each page. It does not rewrite receipts or the
canonical corpus. This controlled replay measures the cost of current checks.

Each repetition has two warm-up pages, one with observation disabled and one
enabled, followed by five measured pairs with alternating execution order.
Every pair runs one page in each mode. Both modes use the same SQL counter;
only the enabled mode observes freshness entry and completion. Disabled-mode
freshness observations are unavailable, not zero calls. SQL work and source
outcomes must match; timing has no pass/fail threshold.

`ontology_discovery_protocol`, `ontology_discovery_measurement` and
`ontology_discovery_coverage` JSON log records retain workload and source-file
hashes, run/image identity, page latency including commit, actual source refresh
and receipt call counts, full receipt source/dependency widths, current/stale/error
outcomes, repeated validation time, SQL count/time, marker/cursor progress,
native source counts, provider calls, reported usage and unavailable usage.
GORM SQL timing excludes row decoding and transaction begin/commit; the complete
validator and page timers include their respective work. Repeated validation
time is measured cost, not a prediction of achievable savings.

The overlap shape reproduces the retained census's multiplicities and terminal
states, not its original texts or full receipt/dependency payloads. Receipt
widths and source hashes make that limitation explicit. The protocol does not
claim full-corpus convergence, retrieval-quality equivalence, or runtime speedup.

## Concurrency and preservation

The `TestOntologyMaintenanceRevalidationDiscovery` integration family pauses a
real page at a completed freshness check, then starts a real concurrent writer.
It records whether relevant definition publication, new vocabulary, source or
definition-support withdrawal, manager override, or generation change commits
between checks or waits on a PostgreSQL lock. Unused vocabulary exercises the
unchanged-classification case. Subsequent discovery must process the resulting
markers, and retired generations must remain inaccessible.

Separate cases exercise cancellation within validation, after a completed source,
and while waiting for a row lock. They verify rollback and later progress. The
A/B/C case includes different shared-memory owners, private sources, a foreign
team, rejected foreign turns, and marker pagination. Every applicable case
checks immutable receipt history and canonical fingerprints; intentional
canonical writer changes are separated from discovery's own effects.

## Validation and retained evidence

All new database cases are registered in the ontology precheck inventory.
Shard 0 runs the large-backlog and vocabulary/source-fence cases; shard 1 runs
the complete measurement; shard 2 runs the remaining ontology cases. Shards 3–5
retain all other registered capabilities. Development E2E runs all six shards plus
`ontology_organization,ontology_management` on the exact pushed source SHA. All six
shards must finish within 30 minutes; the existing 30-minute job timeouts and
27-minute execution bounds remain unchanged. The allocation and bound were
[directly approved](https://github.com/markhuangai/dense-mem/issues/633#issuecomment-6440871680).
The existing allocator weights community's case count fourfold in the six-shard
route because its fanout cohort took about 598 seconds in
[the measured validation run](https://github.com/markhuangai/dense-mem/actions/runs/40498086627).
This is a placement estimate; successful all-six validation remains mandatory.
The expensive measurement is omitted only from duplicate
coverage execution; the other `Revalidation.*` correctness cases retain observer
coverage, and the complete measurement remains mandatory in PostgreSQL E2E.
Keep the run URL, trusted workflow revision, source SHA, production image digest,
required-case results and run-owned image cleanup receipt with the report.
The source SHA is supplied by that verified workflow receipt; the test logs bind
the actual measured source files by SHA-256. The CI redactor preserves validated
boolean/integer values of the five public telemetry/verifier/embedding settings
used by bootstrap. Credentials, explicit patterns, unknown settings and malformed
values remain masked, including credentials equal to a public value. This scoped
amendment was [directly approved](https://github.com/markhuangai/dense-mem/issues/633#issuecomment-6439754910).

The compact evidence is in [the baseline](baselines/issue_633_discovery.json).
Raw logs and generated outputs stay in ignored or task-owned temporary paths.
The final-head full repository CI, CodeQL, production-image E2E and review gates
remain required. The PR leaves #633 open for the optimization decision.

## Reproduction and aggregation

Use a repository-admin account and the trusted workflow on `main`, with a full
pushed same-repository source SHA. The recorded baseline source is
`484be6dd8762294e177d31059436ea2260d050cf`.

```bash
gh workflow run development-e2e.yml --repo markhuangai/dense-mem --ref main \
  -f source_ref=484be6dd8762294e177d31059436ea2260d050cf \
  -f postgres_shards=all \
  -f scenarios=ontology_organization,ontology_management
```

Retain the new run ID and download its six PostgreSQL job logs to an ignored
evaluation run directory. Decode the outer Go test JSON records, then concatenate
`Output` chunks by `(Package, Test)` through each newline before parsing the
`ontology_discovery_*` records: Go splits long output into 1,024-character chunks.
Require one protocol, 90 measurement, 18 coverage, seven concurrency, three
cancellation, one isolation and one lock-cancellation record. Verify the protocol's
workload/source hashes, run ID and image digest against the tested source and
workflow receipt. Require all 654 registered cases to pass exactly once and
verify the originating run's exact image deletion.

For each workload and observation mode, aggregate its 15 measured pages across
three repetitions. Report median latency and nearest-rank p95, selecting sorted
sample `ceil(0.95*n)`. Compute observation's paired percentage difference as the
median of `(enabled_page_ms / disabled_page_ms - 1) * 100`, matching repetition
and sample. Preserve per-repetition medians and actual receipt-width histograms.
Match before/after source counts, provider calls/usage and canonical hashes;
match SQL counts in each enabled/disabled pair. Disabled freshness observations
remain unavailable. These rules permit regeneration from the logged records
without the historical corpus or a live model provider.

## Measured result and safety conclusion

[Run 40504929103](https://github.com/markhuangai/dense-mem/actions/runs/40504929103)
passed all 654 registered cases, both ontology scenarios and all required record
checks. The slowest shard and total six-shard span were 1,484 seconds (24m44s).
[Cleanup 40508526950](https://github.com/markhuangai/dense-mem/actions/runs/40508526950)
deleted the exact published image; its digest and deleted version are in the
baseline.

All enabled pages selected/refreshed 100 sources and started/completed 100
current receipt checks, with zero stale/error outcomes. The median costs were:

| Workload | Committed page ms | Validation ms | Repeated validation ms | Receipt / total SQL statements |
|---|---:|---:|---:|---:|
| unique | 701.978 | 616.083 | 0 | 1,100 / 1,410 |
| overlap-64 | 613.308 | 531.801 | 216.290 | 1,025 / 1,335 |
| shared-5 | 1,325.053 | 1,141.060 | 1,082.146 | 1,200 / 1,510 |

Receipt/dependency widths differ between workloads; their histograms are retained
in the baseline. Repeated validation occupies a median 35.27% and 81.63% of the
enabled overlap/shared pages, but those times include live work that cannot be
skipped safely. Provider calls and reported usage remained unchanged throughout
each repetition; all 100 distinct source states, receipts and canonical hashes
were preserved.

Paired observation differences were +0.20%, -0.06% and -0.26% for unique, overlap
and shared workloads. Their signs reflect measurement noise and do not establish
speedup or zero overhead. Both modes retain SQL-counter overhead. The overlap
p95 was 5,288.089 ms enabled and 6,121.421 ms disabled; with 15 samples, this
nearest-rank p95 is the maximum and is not a stable tail estimate.

The synchronized writers establish the reuse boundary:

| Writer | Observed during the paused page | Result |
|---|---|---|
| Relevant definition publication | Waited behind an existing lock | Both checks remained current before commit |
| Relevant vocabulary, source/support withdrawal, manager override | Committed between receipt checks | The second check of the same receipt became stale |
| Generation change | Committed during the page | Visible PostgreSQL rejection, rollback, and old-generation exclusion |
| Unused vocabulary | Committed between checks | Both checks remained current |

Do not memoize receipt freshness across this write page. Its `WithTeamTx` path
allows later statements to see newly committed inputs; receipt identity does not
freeze current source eligibility, definitions/support, overrides, vocabulary or
generation. Preserve per-source refreshes, cursor updates, requeues, cancellation
and lock behavior.

Immutable receipt decoding, closed-schema validation, and receipt-derived
handle/dependency preparation are candidates for sharing within a page when
bound to team, space, generation and complete receipt identity. Their separate
saving was not measured: the validator timer starts after receipt lookup and
decoding. Profile that pure work before proposing an optimization, and require a
separately approved plan. No runtime optimization or historical-corpus speedup
claim follows from this investigation.
