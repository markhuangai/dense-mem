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

After measurements complete, retain compact aggregate evidence in
`tests/eval/baselines/issue_633_discovery.json`; raw logs and generated outputs
stay in ignored or task-owned temporary paths. The final-head full repository
CI, CodeQL, production-image E2E and review gates remain required. The PR leaves
#633 open for the evidence-backed optimization decision.
