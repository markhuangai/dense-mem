# Query-scored ontology Recall measurements

The combined #667/#668 PR implements query-scored ontology discovery and prepares
the later evaluation report. Automatic ontology membership is not a ranking
channel. Evidence and Relationship candidates use their existing full-query text
and exact-cosine scores; explicit entity expansion retains its existing weight.
Eligible unscored discoveries follow scored results at zero score.

The maintainer approved deferring the full `public_6axis_1k_v3` comparison until
#660 merges. Live `AI_DECISION` experiments are also deferred. #668 remains open.
This preparation and the smaller measurements do not establish full-program
accuracy or reranker quality/cost.

## Required #667 measurements

Preserve the [frozen cohort](recall_accuracy_v1.md), its generator, source lock,
judgments and existing baseline. The cohort has 132 sources and 24 cases. The
registered PostgreSQL harness logs bounded `text`, `vector`, `explicit` and
`ontology` branch membership, raw scores and the final ranking scores alongside
the existing source mappings and metrics. Its constant vectors prove retrieval
logic only.

After the independent plan audit and focused checks, pin the fetched base and
clean implementation commits, matching images/native runners and one corrected
scorer. Prepare one fresh live baseline and three fresh candidate runtimes through
the [existing lifecycle](runtime-lifecycle.md). Keep all generated artifacts in
ignored run directories. Retain historical runtimes and captures.

Use the configured `dense-mem` generation model and Cloudflare
`@cf/baai/bge-m3` at 1,024 dimensions. Disable all optional decision tasks. Both
sides use import concurrency and verified admission 4, ontology global/team
capacity 8/8 and a 500,000-token context window. Record effective model identities,
endpoint hashes, input hashes, source/image/runner identities and unavailable
backing revisions before imports.

Predeclare three run identities and one 24-hour total observation deadline. Run
the existing paired monitor once for each prepared pair:

```bash
PAIR_CONFIG="$accuracy_pair_config" \
  bash tests/eval/scripts/run_full_public_rag_eval_until_done.sh
```

The first pair imports a fresh baseline. Later pairs may reuse only its verified
native capture proof and unchanged original artifacts. Each candidate starts
fresh. Retain all attempts; preserve native resume and failure rules. Do not
change keys, alter judgments or repeat a failed measurement to obtain a pass.

Every repetition requires complete mappings/readiness, no aggregate Recall@10
loss or Bad@10 increase against the baseline, and no bad rank-one result in the
generic-predicate cases. Report every per-case regression and variability.

Run the existing `BenchmarkRecallOntology` with 20 warmups, 200 measurements and
five disabled/enabled pairs per workload, then `compare_ontology_recall.py`.
The unchanged maximum median paired p95 regression is 10%. Archive source-bound
logs and compact results; a failed gate blocks #667 acceptance.

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

## Deferred #668 execution

After #660 and the retrieval PR merge, separately approve and pin the retrieval
merge commit and its first parent. Verify the code difference and matching native
runners/images. Use one corrected scorer for both sides. The frozen public input
remains 991 documents and 204 cases, seed hash
`0923aa14109291e5d5cf24fe419f0b6fc229823bf3dd4292e17d45447365e675` and suite hash
`438a9742576ad5fa9a97c8edaa1e6b0550c740a812dfd3cf051cce68e632b17d`.

Use fresh isolated runtimes and the same admission, model and capacity settings
on both sides. Run the paired monitor with a 24-hour observation budget, native
readiness, complete 991-source/204-case accounting and zero unmapped references.
On each drained, paused runtime, later capture a separate ontology-disabled
control and verify canonical/ontology fingerprints around every condition.
Disabling removes both grouping and discovery; it is not an isolated expansion
measurement. These controls are not executed by this PR or the reporting command.

Overall nDCG@10 must improve, Recall@10 and MRR must not decline, and the separate
adverse cohort must introduce no Bad@10 or bad rank-one result before claiming a
validated improvement. Report every case regression, including the residual
case without a matching definition. A report's cohort gate is not full-program
acceptance; retain both the public and adverse-cohort evidence. Unknown model
revisions, independently generated semantic state and host load remain explicit
limitations. The reranker experiment requires separate future approval and is
not automatically enabled when #660 merges.
