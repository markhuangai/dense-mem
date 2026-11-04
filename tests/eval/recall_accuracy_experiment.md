# Recall accuracy reporting

Use the [frozen adverse cohort](recall_accuracy_v1.md), generator, source lock,
judgments and compact baseline without changing their 132 sources or 24 cases.
The registered PostgreSQL harness reports `text`, `vector`, `explicit` and
`ontology` branch membership, raw scores and final ranks. Constant vectors
establish retrieval logic only.

Automatic ontology membership supplies discovery. Evidence and Relationship
ordering uses existing full-query text and exact-cosine scores; explicit Entity
expansion retains its own weight. Eligible unscored discoveries follow scored
results at zero score.

Use the maintained [paired evaluation procedure](README.md#paired-prepared-runtime-evaluation)
for an approved live comparison. Native readiness, complete mappings, explicit
negative judgments and per-case regressions remain required. A public suite
without explicit negatives cannot replace adverse-cohort safety evidence.
Live decision/reranker experiments retain the access limits in ADR 0015.

## Reporting retained paired runs

Prepare a private manifest with absolute paths to the three resolved pair
configurations and a new output directory outside retained inputs and runtimes:

```json
{
  "schema_version": "dense-mem.eval.recall_accuracy_report.v1",
  "pair_configs": ["/absolute/path/repetition-1.json", "/absolute/path/repetition-2.json", "/absolute/path/repetition-3.json"],
  "out_dir": "/absolute/path/new-report-directory"
}
```

```bash
go run ./cmd/eval-runner --mode recall-accuracy-report \
  --experiment-manifest "$accuracy_experiment_manifest"
```

The report reads and verifies native capture/stage receipts, rejects incomplete
or mismatched accounting, and reconciles aggregates with per-case scores using
the existing scorer. It does not modify paired artifacts or contact a provider.
The JSON retains per-case/per-axis quality, rank-one results, mean/p50/p95/p99
latency, missing latency, population variability, native failures and usage
availability. Percentiles use nearest rank; zero or omitted millisecond latency
is unavailable. Unknown usage is not zero cost. A failed quality gate is written
honestly and makes the CLI exit nonzero.
