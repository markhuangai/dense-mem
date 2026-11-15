# Ontology PostgreSQL measurement regressions

`TestOntologyMaintenanceCoalescingMeasurement` and `TestOntologyMaintenanceRevalidationDiscoveryMeasurement`
run deterministic HTTP providers against real PostgreSQL on dedicated seven-job shards 0 and 1, respectively.
They retain `coalescing-markers-v3` and `discovery-receipts-v1`; neither calls a live model. Paired samples
remain serialized with fresh state. Legacy three- and six-job selectors remain supported; the six-job
measurement selection runs both complete cohorts sequentially during rollout. After plan conformance, request
the complete exact-source Development route:

```bash
gh workflow run development-e2e.yml --repo markhuangai/dense-mem --ref main \
  -f source_ref="$pushed_source_sha" -f postgres_shards=all \
  -f scenarios=ontology_organization,ontology_management
```

Retain all seven job logs in a new ignored run directory. Decode outer Go test JSON, then concatenate `Output`
chunks by `(Package, Test)` through each newline before parsing `ontology_discovery_*` records; Go splits long
output into 1,024-character chunks. Verify run ID, exact tested source and image digest against the workflow
receipt, and source/workload hashes against the tested files. Require the complete current registry exactly
once, selected scenarios, aggregate and owned cleanup.

Retain source SHA, immutable image digest, setup/test durations, deadline headroom, protocol and raw-log
hashes, aggregate results and run-owned cleanup. Target five minutes of
execution headroom within unchanged 27/30-minute limits.

The receipt protocol requires one `protocol`, 90 `measurement`, 18 `coverage`, seven concurrency, three
cancellation, one isolation and one lock-cancellation record. Its unique, overlap-64 and shared-5 workloads
each have 100 sources, three fresh repetitions, two warmup pages and five alternating measured pairs. For each
workload/mode, aggregate all 15 measured pages and retain repetition medians and receipt-width histograms.
Report median and nearest-rank p95 (sorted sample `ceil(0.95*n)`). Pair observation overhead by
repetition/sample using the median of `(enabled_page_ms / disabled_page_ms - 1) * 100`. Disabled freshness
observations are unavailable, not zero calls. Require equal SQL counts per pair and unchanged source outcomes,
provider calls/usage, receipts and canonical hashes. With 15 pages, p95 is the maximum and is not a stable
tail.

The coalescing protocol requires one `coalescing_protocol`, 20 successful `coalescing_measurement` records and
two `coalescing_comparison` records. Each shape has five alternating sequential/combined pairs, a fresh
database per sample, 100 sources, five real 20-source receipts and 20 markers. Setup drains source
notifications and runs ANALYZE after receipts and definition setup; there is no warmup drain. Overlap requires
2,000 sequential versus 100 combined freshness checks and a lower combined median. Disjoint requires 100 in
each mode and reports the measured change, including regressions. Require unchanged queue, canonical state and
receipts and zero added provider calls. Preserve failed or incomplete samples; they cannot satisfy the
five-pair gate.

Discovery duration includes commit. Preparation includes discovery, provider turns may overlap, and GORM
timing excludes decoding/transaction boundaries; do not add these measurements as exclusive phases or infer
release speedup. Concurrent writers can invalidate a later source check of the same receipt. Preserve
per-source freshness, marker/cursor progress, cancellation and rollback.
