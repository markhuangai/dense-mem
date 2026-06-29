# Dense-Mem Evaluation Tests

This directory contains local-only RAG evaluation assets and run artifacts.
The benchmark path is intentionally separate from production data: a seed pack
is imported into a disposable local Dense-Mem team, recall is run through the
same tool surface used by clients, and deterministic retrieval scores are
written under `tests/eval/runs/`.

## Current Layout

```text
tests/eval/
  README.md
  seeds/
    .gitignore
    local_eval_1k_v2/
      seed_manifest.json
      corpus.jsonl
      cases.jsonl
      qrels.jsonl
      answers.jsonl
      hard_negatives.jsonl
      transforms.jsonl
      licenses.md
    local_train_100k_v1/     # local ignored fixture
      seed_manifest.json
      suite.jsonl
      corpus.jsonl
      cases.jsonl
      qrels.jsonl
      answers.jsonl
      hard_negatives.jsonl
      transforms.jsonl
      licenses.md
  suites/
    local_eval_1k_v2.jsonl
  runs/
    .gitignore
  cache/
    embeddings/
      .gitignore
  snapshots/
    .gitignore
```

`local_eval_1k_v2` is the committed local acceptance seed. It contains 1,000
cases, 4,000 corpus rows, 3,000 hard negatives, and 1,000 qrels. The split is
100 sanity cases plus 900 adversarial cases across obsolete corrections,
explicit negation, alias collisions, temporal validity, quoted false claims,
scope collisions, authority conflicts, retractions, unit traps, and conditional
exceptions.

The JSONL files are the source of truth for a run. Regenerate them only when
intentionally changing the seed:

```bash
go run ./cmd/eval-seedgen \
  --preset local_eval_1k_v2 \
  --out tests/eval/seeds/local_eval_1k_v2 \
  --suite tests/eval/suites/local_eval_1k_v2.jsonl
```

`local_train_100k_v1` is a large local-only materialized fixture pack for
training or load experiments. It lives beside other seeds so fixed fixture data
has one location, but it is intentionally ignored by git because it is hundreds
of MB. Before using it for a reported run, validate its manifest and keep the
run artifacts with the seed hash.

## Commands

Validate the default committed seed and suite:

```bash
go run ./cmd/eval-runner --mode validate
```

Validate the local 100k seed and colocated suite:

```bash
go run ./cmd/eval-runner \
  --mode validate \
  --seed tests/eval/seeds/local_train_100k_v1/seed_manifest.json \
  --suite tests/eval/seeds/local_train_100k_v1/suite.jsonl \
  --out tests/eval/runs/local_train_100k_v1_validate
```

Run a local live baseline against an already configured local instance:

```bash
DENSE_MEM_API_KEY=<read-write-key> \
DENSE_MEM_CONTROL_TOKEN=<control-token> \
scripts/eval-local.sh \
  --mode baseline \
  --import-seed \
  --min-recall-at-k 0.80 \
  --min-required-rank1-rate 0.55 \
  --max-average-bad-at-k 0.20 \
  --max-bad-rank1-rate 0.05 \
  --out tests/eval/runs/$(date -u +%Y%m%dT%H%M%SZ)_baseline
```

Live seed imports call `remember` for fragment-only rows and `import_memories`
for rows with typed claims. Fragment-only imports poll `get_memory_placement`
for each corpus row. `local_eval_1k_v2` has 4,000 corpus rows, so one-shot
import needs at least 8,000 tool requests before export and recall cases. For
disposable eval compose runs, raise `RATE_LIMIT_PER_MINUTE` in `.env` before
starting the server, then restart the server.

For ranking/search-only experiments against `local_eval_1k_v2`, reuse the stable
already-imported eval team and omit `--import-seed`. Re-import the 1k seed only
when the stored data shape changes: seed corpus, embeddings,
claim/fact extraction, import metadata, or write-path fields such as
`valid_from` / `recorded_at`.

The runner enables `EVALUATION_MODE_ENABLED`, imports corpus rows through
`remember` or `import_memories`, exports fragment/claim/fact mappings through
`eval_list_knowledge_refs`, runs cases through `eval_run_recall_case`, scores
with deterministic retrieval metrics, and writes:

```text
run_config.json
seed_manifest.json
suite.jsonl
knowledge_mapping.json
recall_traces.jsonl
retrieval_scores.jsonl
summary.json
gate_result.json       # when gate flags are set
comparison.json        # compare mode only
```

Compare a candidate run to a baseline:

```bash
go run ./cmd/eval-runner \
  --mode compare \
  --baseline-run tests/eval/runs/<baseline> \
  --candidate-run tests/eval/runs/<candidate>
```

## Scoring

Retrieval scoring is deterministic and uses:

```text
ranked_refs + qrels.required_refs + qrels.bad_refs
  -> Recall@K
  -> MRR
  -> nDCG@K
  -> Bad@K
  -> required_rank1_rate
  -> bad_rank1_rate
```

When a trace includes `context_refs`, the runner also scores the assembled
context with the same qrels and writes separate `context_*` metrics:

```text
context_refs + qrels.required_refs + qrels.bad_refs
  -> average_context_recall_at_k
  -> average_context_mrr
  -> average_context_ndcg_at_k
  -> average_context_bad_at_k
  -> context_required_rank1_rate
  -> context_bad_rank1_rate
```

The original ranked metrics are unchanged. Use the context metrics when the
question is whether a judged-bad item reached the final assembled context, not
only whether it appeared in the raw ranked retrieval list.

When a trace includes `context_evidence_refs` and the qrel includes
`required_evidence_refs` or `bad_evidence_refs`, the runner scores supporting
fragments separately and writes `evidence_*` metrics:

```text
context_evidence_refs + qrels.required_evidence_refs + qrels.bad_evidence_refs
  -> average_evidence_recall_at_k
  -> average_evidence_mrr
  -> average_evidence_ndcg_at_k
  -> average_evidence_bad_at_k
  -> evidence_required_rank1_rate
  -> evidence_bad_rank1_rate
```

Use evidence metrics when the top-level fact or claim is correct but its
assembled supporting fragments may be stale, wrong, or missing.

`source_doc_id` labels are remapped to Dense-Mem refs after import or export.
The mapping keeps a backward-compatible default fragment ref and also supports
type-aware refs for seeds that use the same `source_doc_id` as a fragment,
claim, and fact. This remapping also applies to evidence qrels. Unmapped
required refs are reported in `unmapped_source_refs` and still penalize the
denominator, so mapping problems cannot silently inflate scores.

Validation also checks manifest counts, qrel source-doc coverage, suite case
coverage, and adversarial cases having explicit `bad_refs`.

## Remaining Work

Embeddings and materialized Postgres/Neo4j snapshots are cache artifacts. They
should be reused when the seed hash, schema hash, embedding provider, model, and
dimensions match; the JSONL seed remains the source of truth. Snapshot restore
and embedding-cache reuse are not implemented in the runner yet.

For fine-tuning, do not train on the same cases used for acceptance gates.
Materialize or download training data separately, and keep committed local eval
seeds and future release suites as held-out evaluation sets. The ignored 100k
synthetic seed is useful for local load, reranker plumbing, and training-data
format work, but it is too repetitive to be the only real fine-tuning corpus.
