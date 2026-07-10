# Evaluation

Dense-Mem's local evaluation harness measures recall and context assembly
against deterministic seed data. It is intentionally separate from production
memory: seed packs are imported into a disposable local team, recalled through
the same tool surface clients use, and scored from exported refs.

## Seed Packs

Evaluation assets live in the main repository under `tests/eval/`.

| Seed | Purpose |
| --- | --- |
| `local_eval_1k_v2` | Committed local acceptance seed with 1,000 cases, 4,000 corpus rows, 3,000 hard negatives, and 1,000 qrels. |
| `local_eval_relational_1k_v1` | Committed relational recall seed with 1,000 cases, 4,000 corpus rows, 2,000 hard negatives, 1,000 qrels, and 100 expected dream hypotheses. |
| `local_train_100k_v1` | Ignored local-only training or load fixture. |
| `local_train_relational_100k_v1` | Ignored local-only relational training or load fixture. |

The relational seed exercises overlapping entity relationships, time windows,
aliases, source authority, negation, retractions, and close-neighbor
distractors. It also scores expected dream hypotheses separately from validated
memory results.

Regenerate the committed acceptance seed only when intentionally changing the
seed:

```bash
go run ./cmd/eval-seedgen \
  --preset local_eval_1k_v2 \
  --out tests/eval/seeds/local_eval_1k_v2 \
  --suite tests/eval/suites/local_eval_1k_v2.jsonl
```

Generate the relational held-out seed:

```bash
go run ./cmd/eval-seedgen --preset local_eval_relational_1k_v1
```

Generate the local-only relational training fixture:

```bash
go run ./cmd/eval-seedgen --preset local_train_relational_100k_v1
```

## Runner Commands

Validate the default committed seed:

```bash
go run ./cmd/eval-runner --mode validate
```

Validate the committed relational seed:

```bash
go run ./cmd/eval-runner \
  --mode validate \
  --seed tests/eval/seeds/local_eval_relational_1k_v1/seed_manifest.json \
  --suite tests/eval/suites/local_eval_relational_1k_v1.jsonl
```

Run a live local baseline against an already configured local instance:

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

Live imports call `remember` for fragment-only rows and `import_memories` for
rows with typed claims. Fragment-only rows poll `get_memory_placement`, so a
4,000-row seed can require thousands of tool requests before recall cases run.
For disposable compose eval runs, raise `RATE_LIMIT_PER_MINUTE` before starting
the server.

## Evaluation Mode

The runner enables `EVALUATION_MODE_ENABLED`, imports seed rows, optionally runs
the eval-only dream cycle, exports knowledge refs, runs recall cases, and writes
artifacts under `tests/eval/runs/`.

Expected output files include:

```text
run_config.json
seed_manifest.json
suite.jsonl
knowledge_mapping.json
recall_traces.jsonl
retrieval_scores.jsonl
summary.json
gate_result.json
comparison.json
```

Evaluation exports and recall traces are on by default whenever evaluation mode
is enabled. Do not use production teams or production memory for primary RAG
evaluation.

## Scoring

Retrieval scoring is deterministic:

```text
ranked_refs + qrels.required_refs + qrels.bad_refs
  -> Recall@K
  -> MRR
  -> nDCG@K
  -> Bad@K
  -> required_rank1_rate
  -> bad_rank1_rate
```

The runner also scores assembled context refs when traces include
`context_refs`, supporting evidence when traces include `context_evidence_refs`,
and dream hypotheses when traces include `dream_refs`.

See [Recall Ranking](Recall-Ranking#reading-evaluation-traces) for ranking trace
fields and [Evaluation Semantics And Gates](Recall-Ranking#evaluation-semantics-and-gates)
for cross-tier source mapping and fragment-only evidence fallback behavior.

Dream metrics are separate:

```text
dream_refs + qrels.required_dream_refs + qrels.bad_dream_refs
  -> average_dream_recall_at_k
  -> average_dream_mrr
  -> average_dream_ndcg_at_k
  -> average_dream_bad_at_k
  -> dream_required_rank1_rate
  -> dream_bad_rank1_rate
```

Dream metrics do not change ordinary ranked, context, or evidence metrics.
Expected dream qrels are mapped through `expected_dreams.jsonl` by matching the
dream source-ref set exported from `eval_list_knowledge_refs`.

## Eval-Only Tools

Evaluation tools are visible only when evaluation mode is enabled and require a
read-write API key. They fail closed when runtime config is unavailable.

| Tool | Purpose |
| --- | --- |
| `eval_get_manifest` | Return the evaluation-mode manifest, runtime limits, and available capabilities. |
| `eval_list_knowledge_refs` | Page through team-scoped fragments, claims, facts, communities, dreams, or graph edges. |
| `eval_get_knowledge_item` | Fetch one exported evaluation item. |
| `eval_run_recall_case` | Run one recall/context evaluation case through current Dense-Mem logic. |
| `eval_run_dream_cycle` | Run the dream phase during evaluation so expected dream hypotheses can be exported and scored. |
| `eval_score_retrieval_case` | Score one set of ranked refs against required and bad refs. |
| `eval_list_recall_feedback_events` | Export stored recall-feedback events for offline analysis. |
| `eval_get_recall_feedback_event` | Fetch one stored recall-feedback event. |

`eval_run_dream_cycle` accepts optional `seed_dreams` in evaluation mode. Seeded
dreams are deterministic hypotheses with source refs; they are capped by
`max_outputs` and remain hypotheses, not facts.
