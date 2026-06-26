# Dense-Mem Evaluation Tests

This directory contains local-only RAG evaluation assets and run artifacts.
The benchmark path is intentionally separate from production data: a seed pack
is imported into a disposable local Dense-Mem team, baseline recall is run
through the same tool surface used by clients, and deterministic retrieval
scores are written under `tests/eval/runs/`.

## Current Layout

```text
tests/eval/
  README.md
  seeds/
    .gitignore
    local_pr_v1/
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
    pr.jsonl
  runs/
    .gitignore
  cache/
    embeddings/
      .gitignore
  snapshots/
    .gitignore
```

`local_pr_v1` is a fixed deterministic synthetic PR-scale seed: 200 cases and
2,000 corpus rows across the planned retrieval slices. Treat the JSONL files as
the source of truth. Do not regenerate them during a baseline or candidate run,
because accuracy comparisons only mean anything when both runs use the exact
same cases, answers, qrels, and hard negatives.

`local_train_100k_v1` is a large local-only materialized fixture pack for
training or load experiments. It lives beside other seeds so fixed fixture data
has one location, but it is intentionally ignored by git because it is hundreds
of MB. Before using it for a reported run, validate its manifest and keep the
run artifacts with the seed hash.

## Commands

Validate the committed seed and suite:

```bash
go run ./cmd/eval-runner \
  --mode validate \
  --seed tests/eval/seeds/local_pr_v1/seed_manifest.json \
  --suite tests/eval/suites/pr.jsonl
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
  --out tests/eval/runs/$(date -u +%Y%m%dT%H%M%SZ)_baseline
```

The runner enables `EVALUATION_MODE_ENABLED`, imports corpus rows through
`remember`, exports fragment mappings through `eval_list_knowledge_refs`, runs
cases through `eval_run_recall_case`, scores with deterministic retrieval
metrics, and writes:

```text
run_config.json
seed_manifest.json
suite.jsonl
knowledge_mapping.json
recall_traces.jsonl
retrieval_scores.jsonl
summary.json
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
```

`source_doc_id` labels are remapped to Dense-Mem fragment IDs after import or
export. Unmapped required refs are reported in `unmapped_source_refs` and still
penalize the denominator, so mapping problems cannot silently inflate scores.

## Remaining Work

Embeddings and materialized Postgres/Neo4j snapshots are cache artifacts. They
should be reused when the seed hash, schema hash, embedding provider, model, and
dimensions match; the JSONL seed remains the source of truth. Snapshot restore
and embedding-cache reuse are not implemented in the runner yet.

The release seed still needs curated public-dataset streams from the design plan:
BEIR/MTEB/MS MARCO, HotpotQA/KILT, FEVER, RAGBench/RAGTruth, plus reviewed
Dense-Mem project-memory fixtures and adversarial cases.

For fine-tuning, do not train on the same cases used for acceptance gates.
Materialize or download training data separately, then keep `local_pr_v1` and
future release suites as held-out evaluation sets. A 100k synthetic seed is
useful for local load, reranker plumbing, and initial training-data format work,
but it is too repetitive to be the only real fine-tuning corpus.
