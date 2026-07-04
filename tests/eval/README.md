# Dense-Mem Public RAG Evaluation

This directory prepares and runs public retrieval evals against a dedicated
local Dense-Mem stack. Public dataset downloads, generated seed data, database
files, and run artifacts are local-only and ignored by git.

## Layout

```text
tests/eval/
  README.md
  docker-compose.eval.yml
  scripts/
    prepare_full_public_rag_eval.py
  data/       # ignored: downloaded and extracted public datasets
  runtime/    # ignored: persistent eval-only Postgres, Neo4j, Redis, Prometheus data
  seeds/      # ignored: generated full Dense-Mem seed packs
  suites/     # ignored: generated full eval suites
  runs/       # ignored: eval run artifacts
```

## Public Axes

| Axis | Default Dataset | Purpose |
| --- | --- | --- |
| `beir_standard` | BEIR `scifact` | Standard ad-hoc retrieval with public qrels. |
| `msmarco_passage` | BEIR `msmarco` | Large web passage retrieval. |
| `hotpotqa_multihop` | BEIR `hotpotqa` | Multi-hop retrieval with supporting-evidence qrels. |

The preparation script consumes BEIR-format `corpus.jsonl`, `queries.jsonl`,
and `qrels/*.tsv` files from:

`https://public.ukp.informatik.tu-darmstadt.de/thakur/BEIR/datasets/{dataset}.zip`

It downloads complete upstream dataset packages, then materializes every corpus
package locally. By default, it materializes a deterministic budgeted seed of
about 5,000 corpus rows across the three axes. Use `--max-corpus-docs 0` only
when intentionally opting into the complete upstream corpora. Evidence text is
capped below the server's per-entry validation limit.

## Prepare Budgeted Eval Data

```bash
python3 tests/eval/scripts/prepare_full_public_rag_eval.py \
  --seed-id public_rag_3axis_5k_v1 \
  --source-seed-id public_rag_3axis_full_v1 \
  --max-corpus-docs 5000
```

`--source-seed-id public_rag_3axis_full_v1` keeps source document ids compatible
with the already-imported partial local corpus, so direct import can skip rows
that were already embedded.

The command writes:

```text
tests/eval/data/beir/
tests/eval/seeds/public_rag_3axis_5k_v1/
tests/eval/suites/public_rag_3axis_5k_v1.jsonl
```

Generated files are intentionally ignored by git.

## Start Persistent Eval Stack

Use the eval compose override so database state is kept under
`tests/eval/runtime/` instead of anonymous Docker volumes:

```bash
docker compose -p densemem_eval_full \
  -f docker-compose.yml \
  -f tests/eval/docker-compose.eval.yml \
  up -d --build
```

Do not use this stack for production or ad-hoc manual memory work. It is a
reusable eval database.

## Import Budgeted Corpus Once

Provision an eval API key in the persistent stack, then import the generated
seed with `import` mode. Public corpora should use the direct eval import path:
it batches embedding requests and writes `SourceFragment` rows directly into
the eval-only Neo4j database while still running recall through the public tool
surface later.

```bash
set -a
. ./.env
set +a

go run ./cmd/eval-runner \
  --mode import \
  --seed tests/eval/seeds/public_rag_3axis_5k_v1/seed_manifest.json \
  --suite tests/eval/suites/public_rag_3axis_5k_v1.jsonl \
  --out tests/eval/runs/import_public_rag_3axis_5k_v1 \
  --import-seed \
  --import-concurrency 4 \
  --direct-import \
  --direct-import-team-id "$(jq -r .team_id tests/eval/runs/eval_full_profile.json)" \
  --direct-import-batch-size 512 \
  --neo4j-uri bolt://127.0.0.1:${NEO4J_BOLT_HOST_PORT:-17687} \
  --neo4j-user "${NEO4J_USER}" \
  --neo4j-database "${NEO4J_DATABASE:-neo4j}" \
  --max-page-size 500
```

`import` mode imports corpus rows, validates qrel mappings, and writes artifacts
without running recall cases. Omit `--direct-import` only when intentionally
validating the `remember` write path on a small corpus.

For clean budgeted databases, the resumable monitor adopts an existing import
PID from the import run directory, restarts direct import if it exits before the
`counts.corpus` target is present in Neo4j, verifies the final fragment count,
and then runs the baseline eval without reimporting:

```bash
tests/eval/scripts/run_full_public_rag_eval_until_done.sh
```

The monitor writes current progress to
`tests/eval/runs/full_eval_monitor/status.json`. It checks progress every
60 seconds by default; set `SLEEP_SECONDS` to override the cadence.

If the eval database already contains more fragments than the current budgeted
seed, run import mode directly and reuse the generated mapping artifact instead
of the monitor's global-count loop.

Re-import only when one of these changes:

- public dataset contents or selected qrels split
- Dense-Mem import behavior
- embedding provider, model, or dimensions
- database schema/migrations
- seed generation logic

## Run Recall Without Reimporting

After the budgeted corpus has been imported into the persistent eval stack, run
baseline/candidate suites without `--import-seed`. Reuse the mapping artifact
from the import run so the runner does not need to export the whole knowledge
map:

```bash
go run ./cmd/eval-runner \
  --mode baseline \
  --seed tests/eval/seeds/public_rag_3axis_5k_v1/seed_manifest.json \
  --suite tests/eval/suites/public_rag_3axis_5k_v1.jsonl \
  --out tests/eval/runs/before_public_rag_3axis_5k_v1 \
  --mapping tests/eval/runs/import_public_rag_3axis_5k_v1/knowledge_mapping.json \
  --max-page-size 500

go run ./cmd/eval-runner \
  --mode candidate \
  --seed tests/eval/seeds/public_rag_3axis_5k_v1/seed_manifest.json \
  --suite tests/eval/suites/public_rag_3axis_5k_v1.jsonl \
  --out tests/eval/runs/after_public_rag_3axis_5k_v1 \
  --mapping tests/eval/runs/import_public_rag_3axis_5k_v1/knowledge_mapping.json \
  --max-page-size 500

go run ./cmd/eval-runner \
  --mode compare \
  --baseline-run tests/eval/runs/before_public_rag_3axis_5k_v1 \
  --candidate-run tests/eval/runs/after_public_rag_3axis_5k_v1 \
  --out tests/eval/runs/compare_public_rag_3axis_5k_v1
```

## Reset Eval State

This deletes the persistent eval database and generated public data:

```bash
docker compose -p densemem_eval_full \
  -f docker-compose.yml \
  -f tests/eval/docker-compose.eval.yml \
  down

rm -rf tests/eval/data tests/eval/runtime tests/eval/seeds tests/eval/suites tests/eval/runs
```
