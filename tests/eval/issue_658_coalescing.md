# Ontology marker coalescing (#658)

Definition discovery combines a contiguous oldest group of at most 20 markers.
Each marker retains its sequence and cursor; non-definition and unavailable
markers stop the group. SQL bounds each marker's candidates to the remaining
page limit plus one, then selects at most 100 distinct scoped source identities
in the database's existing order. Different sources always retain separate live
freshness checks, including sources sharing an assessment receipt.

## Measurement semantics

The internal discovery result separates attempted counts from committed counts.
A failed transaction reports zero committed counts, retains attempted work, and
sets `committed=false`. Duration uses a monotonic timer around the complete
discovery call, including transaction completion. Preparation duration covers
the complete claimed preparation turn and includes discovery; these durations
must not be added together as exclusive phases.

`initial_sources` counts successful initial corpus refreshes separately.
Marker expansion reports enumerated per-marker matches, including lookahead;
distinct successfully refreshed source identities; actual freshness invocations;
distinct scoped receipt/operation identities; pending source writes (`requeues`);
and markers actually deleted. Count-only identity sets never reuse freshness
results. Logs contain no source text, receipt bodies, or credentials.

## Paired protocol

`TestOntologyMaintenanceRevalidationDiscoveryMeasurement/coalescing-markers`
adds the versioned `coalescing-markers-v3` protocol to the existing measurement.
Both workloads use 100 sources, five current real receipts of 20 sources each, and 20
stable markers queued upfront. Overlapping markers each match all sources;
disjoint markers each match five sources through their unique ordinal terms.
Setup checks each marker's effective database selection, so receipt-reference or
dependency expansion cannot silently turn the disjoint workload into overlap.
The existing current receipts and their immutable history remain intact.

Run five pairs per workload, alternating sequential/combined execution order.
Each sample has a fresh PostgreSQL database and the same deterministic HTTP
provider. Source-creation notifications are drained before constructing the real
assessment receipts. Setup explicitly runs PostgreSQL `ANALYZE` after receipts
and after definition setup, so both modes start with current planner statistics.
Setup is excluded; no additional warm-up drain is performed. The
test-only sequential discovery/expansion drivers are frozen from base
`09d16398005f0a8ba3ca0fe6c31e08c4341eca9f` and reuse the same source-selection and
freshness owners. No production legacy mode or performance toggle is added.

Each sample logs all discovery-call durations including commits, page counts,
SQL counts/time, actual freshness calls, source-file hashes, run/image identity,
canonical hashes, receipt preservation, and additional provider calls. Incomplete
samples retain `failed=true`; no failed pair can satisfy the five-pair gate.
Overlap requires 2,000 sequential versus 100 combined freshness calls and a lower
combined median. Disjoint work requires 100 calls in both modes and reports its
measured change, including regressions. These are isolated discovery comparisons,
not whole-release or historical-corpus throughput estimates.

## Validation and evidence

New PostgreSQL cases are registered with the ontology capability. The existing
measurement remains on dedicated shard 1; ontology correctness runs on shards
0 and 2, and recall prechecks on shard 5. The `ontology_organization` and
`ontology_management` scenarios cover the remaining ontology/recall cases and
positive/adverse production behavior. Require exact-source Development E2E
results and run-owned image cleanup, then final-head full CI, CodeQL and
production-image E2E.

Three predeclared local `TestOntologyMaintenanceCohort` runs use the existing
frozen inputs/judgments, real configured provider, fresh databases, and an
explicit 500,000-token context. Retain every result in separate ignored run
directories. Record the configured/provider identity and any unknown backing
revision; require full source/fact retention and zero false consolidation.

The issue-specific intermediate 1k waiver is recorded in #658. The mandatory
final release comparison remains owned by #660.

## Recorded validation

The [compact evidence](baselines/issue_658_coalescing.json) records independent
plan conformance before tests, passing local ontology tests, formatting/static
checks, integration-profile compilation, and deployment configuration checks.
All three live repetitions passed all nine frozen cases: full source/fact
retention, zero false consolidation, and zero repeated slots. The configured
model was `dense-mem`; its backing revision is unknown.

Three earlier attempts stopped during configuration loading before any model
exchange because the provider environment lacked PostgreSQL connection fields.
Their failed reports are retained. A synthetic config-only DSN satisfied the
loader; the cohort continued to use its own real disposable PostgreSQL fixtures.
Provider settings, frozen judgments, and the context window were unchanged.

The first [Development E2E run](https://github.com/markhuangai/dense-mem/actions/runs/40624465209)
passed both ontology production scenarios and PostgreSQL shards 0 and 5. Shard 2
found two fixture assumptions that omitted existing predicate/definition seeding;
the corrected tests passed locally against PostgreSQL. Shard 1 reached its
27-minute deadline after five overlap pairs and four disjoint pairs. Its
`coalescing-markers-v1` evidence is retained as incomplete, not an acceptance pass.
The run-owned image was deleted by the corresponding cleanup workflow.

A local `coalescing-markers-v2` diagnostic run shortened source setup but exposed
automatic-statistics refresh during longer sequential samples. It was interrupted
and retained. Version 3 explicitly initializes statistics for both modes without
changing the older `discovery-receipts-v1` fixture or its assertions.

All 20 local version-3 samples passed. The median summed discovery time was
38.33s sequential versus 2.05s combined for overlap (94.65% lower), and 1.970s
versus 1.965s for disjoint work (0.25% lower). Overlap used 2,000 versus 100
freshness checks; disjoint work used 100 in both modes. Every sample preserved
canonical fingerprints, queue state, and receipts with no additional provider
calls. Other local project runtimes remained active; these measurements require
the planned isolated remote confirmation and establish no end-to-end speedup.

This file records the prepublication evidence checkpoint. Final exact-head
Development E2E, complete measurement, CI, CodeQL and production-image E2E
receipts belong to the pull request closing #658; these historical attempts do
not substitute for those gates.

## CI fixture follow-up

The first PR validation passed all 33 E2E scenarios and six PostgreSQL shards
but missed the strict coverage threshold. Registering the existing committed
diagnostics regression raised native complete coverage to 36,845/40,932 in
[run 40643016026](https://github.com/markhuangai/dense-mem/actions/runs/40643016026).
That run passed 32 scenarios and five PostgreSQL shards; telemetry parity failed
and shard 2 exhausted its unchanged 27-minute budget after 111 tests passed.

A real Grafana/Prometheus probe reproduced a startup boundary where a live
embedding card sees a second scrape that the earlier, whole-second series query
does not. The telemetry fixture now requires sampled embedding activity within
its existing readiness loop. Every parity assertion remains. The failed run's
raw scrape samples were unavailable, so its exact timing cannot be reconstructed.

The two new large coalescing tests reuse prepared-source setup before constructing
real receipts. All assertions and fresh databases remain. One local setup
comparison passed in 240.751s before and 48.155s after; this is test setup evidence,
not a service performance claim. The frozen measurement protocols are unchanged.
Both failures are retained and require new-head CI/E2E verification.
