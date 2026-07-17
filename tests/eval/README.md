# Dense-Mem Public RAG Evaluation

This directory runs deterministic public retrieval evaluations against a
dedicated local Dense-Mem stack. Corpus ingestion uses the same public
`remember` workflow as production:

```text
corpus row
  -> POST /api/v1/tools/remember
  -> asynchronous AI placement
  -> POST /api/v1/tools/get_memory_placement
  -> recall suite
  -> qrel-based retrieval metrics
```

The evaluation harness does not use `import_memories`, direct Neo4j writes, or
an answer-judge model. It measures retrieval against deterministic qrels and
reports placement outcomes separately.

Generated datasets, persistent databases, credentials, and run artifacts are
local-only and ignored by git.

## Layout

```text
tests/eval/
  README.md
  docker-compose.eval.yml
  scripts/
    prepare_public_6axis_eval.py
    prepare_public_semantic_eval.py
    prepare_full_public_rag_eval.py
    run_full_public_rag_eval_until_done.sh
  data/                 # downloaded public datasets
  seeds/                # generated seed packs
  suites/               # generated suites
  runtime/v1/
    dataset_identity.json
    eval_profile.json
    postgres/
    neo4j/
    redis/
    prometheus/
    monitor/
    runs/
```

## Public Six-Axis Gate

| Axis | Dataset | Purpose |
| --- | --- | --- |
| `scifact` | BEIR SciFact | Scientific claim/document retrieval. |
| `msmarco` | BEIR MS MARCO | Web-passage retrieval. |
| `hotpotqa` | BEIR HotpotQA | Public multi-hop document retrieval. |
| `musique` | MuSiQue answerable dev v1.0 | 2/3/4-hop relationship retrieval. |
| `qasper` | QASPER train/dev v0.3 | Paper QA evidence retrieval. |
| `longmem_oracle` | LongMemEval-S cleaned | Long-memory chat recall over non-abstention oracle rows. |

The V2 release train uses `public_6axis_1k_v1` as the hard deterministic
release gate. `public_6axis_5k_v1` is diagnostic only unless a later roadmap
issue promotes it.

The approved gate policy is committed at:

```text
tests/eval/baselines/v2.1.1_public_6axis_1k_baseline.json
```

A seed corpus row is plain evidence: `source_doc_id`, `content`, and optional
source metadata. Content is split rather than truncated, and the Go harness
rejects any corpus row above 999 Unicode code points. Legacy `claims` and
`auto_promote` fields are rejected because they bypass production extraction
and placement.

## Prepare a seed

Generate the required 1k seed:

```bash
python3 tests/eval/scripts/prepare_public_6axis_eval.py \
  --size 1000 \
  --force
```

Generate the optional diagnostic 5k seed:

```bash
python3 tests/eval/scripts/prepare_public_6axis_eval.py \
  --size 5000 \
  --force
```

Use the checked local paths explicitly in every command:

```bash
SEED=tests/eval/seeds/public_6axis_1k_v1/seed_manifest.json
SUITE=tests/eval/suites/public_6axis_1k_v1.jsonl
RELEASE_GATE=tests/eval/baselines/v2.1.1_public_6axis_1k_baseline.json
```

The runner has no default seed or suite. This prevents an invocation from
silently evaluating the wrong corpus.

The old relational seed presets depended on typed claims and preloaded facts,
so they are retired. `cmd/eval-seedgen` retains the content-only
`local_eval_1k_v2` preset.

## Start the persistent V1 stack

The override stores all database state under `tests/eval/runtime/v1` by
default. Set `V1_COMPOSE_DATA_DIR` before both `up` and later eval commands to
use another V1 root.

```bash
export V1_COMPOSE_DATA_DIR="$(realpath -m tests/eval/runtime/v1)"

docker compose -p densemem_eval_full \
  -f docker-compose.yml \
  -f tests/eval/docker-compose.eval.yml \
  up -d --build
```

Do not use this team or stack for manual memory work. The monitor assumes one
seed dataset per V1 runtime.

## Provision the eval team once

After migrations finish, provision a dedicated team and keep its ignored
credential file under the V1 runtime:

```bash
docker compose -p densemem_eval_full \
  -f docker-compose.yml \
  -f tests/eval/docker-compose.eval.yml \
  run --rm --no-deps server \
  /app/provision-profile --name dense-mem-eval-v1 \
  > tests/eval/runtime/v1/eval_profile.json

chmod 600 tests/eval/runtime/v1/eval_profile.json
```

The monitor also accepts `DENSE_MEM_API_KEY` and `EVAL_TEAM_ID` directly when
`PROFILE_PATH` is not used. Never commit the profile file or print its API key
in logs.

## Validate without ingesting

Validation reads the seed and suite, verifies cross-file qrels, and writes
artifacts. It does not start the stack or write memory:

```bash
scripts/eval-local.sh \
  --mode validate \
  --seed "${SEED}" \
  --suite "${SUITE}" \
  --out tests/eval/runtime/v1/runs/validate
```

For `public_6axis_*` seeds, validation also requires the generated
`validation_report.json` named by the seed manifest. The report must have
status `passed` and its seed hash must match the current generated files.

## Import once, resume, and run recall

The long-running monitor is on-demand; nothing starts it automatically. It
builds the current runner, validates the selected seed, imports through
`remember`, waits for terminal placement, and then runs the baseline recall
suite:

```bash
SEED="${SEED}" \
SUITE="${SUITE}" \
RELEASE_GATE_POLICY="${RELEASE_GATE}" \
IMPORT_CONCURRENCY=3 \
PLACEMENT_TIMEOUT=10m \
tests/eval/scripts/run_full_public_rag_eval_until_done.sh
```

The monitor polls every 60 seconds by default. `SLEEP_SECONDS` changes only
the monitor cadence; it does not cap graph relationships or placement work.

Resume behavior is based on the latest placement attempt for each
`eval:<source_doc_id>`:

| Latest state | Resume action |
| --- | --- |
| `completed` and live fragment exists | Skip the corpus row. |
| `failed` | Retry the corpus row. |
| No attempt | Import the corpus row. |
| `queued` or `processing` | Wait for the placement worker; do not duplicate it. |
| Completed checkpoint but fragment is missing | Retry the corpus row. |

One failed concurrent request stops scheduling new rows but allows already
active requests to finish. A later monitor pass continues from the latest
completed placements instead of restarting the corpus.

The runtime identity contains the seed content hash, suite hash, embedding
model/dimensions/endpoint hash, team ID, and `remember` route. A mismatch is a
hard error. If data exists without an identity file, the monitor refuses to
adopt or erase it.

Progress and placement analysis are written to:

```text
tests/eval/runtime/v1/monitor/status.json
tests/eval/runtime/v1/monitor/placement_summary.json
tests/eval/runtime/v1/monitor/completed_source_doc_ids.txt
tests/eval/runtime/v1/monitor/failed_source_doc_ids.txt
tests/eval/runtime/v1/runs/import/knowledge_mapping.json
tests/eval/runtime/v1/runs/baseline/summary.json
```

`placement_summary.json` reports latest completed/failed/pending counts,
category and item-status counts, promotion rate, rejection rate, and historical
retry attempts. Recall starts only when all latest placements are completed,
there are no failed or pending latest attempts, the team-scoped eval fragment
count equals `counts.corpus`, and the remember-only import artifacts exist.

## Run recall again without reimporting

Once ingestion is complete, reuse the persisted graph and mapping:

```bash
set -a
. ./.env
set +a
export DENSE_MEM_API_KEY="$(jq -r .api_key tests/eval/runtime/v1/eval_profile.json)"

go run ./cmd/eval-runner \
  --mode baseline \
  --seed "${SEED}" \
  --suite "${SUITE}" \
  --out tests/eval/runtime/v1/runs/baseline-rerun \
  --mapping tests/eval/runtime/v1/runs/import/knowledge_mapping.json \
  --release-gate-policy "${RELEASE_GATE}" \
  --max-page-size 500
```

Use `--mode candidate` for a candidate run, then compare two run directories:

```bash
go run ./cmd/eval-runner \
  --mode compare \
  --baseline-run tests/eval/runtime/v1/runs/baseline \
  --candidate-run tests/eval/runtime/v1/runs/candidate \
  --out tests/eval/runtime/v1/runs/comparison
```

Compare mode reads and checks the seed hashes recorded in the two completed
summaries, so it does not take seed or suite paths.

## When the persisted dataset is reusable

Reuse `tests/eval/runtime/v1` when all of these remain unchanged:

- seed contents and source document IDs
- suite contents
- embedding endpoint, model, and dimensions
- ingestion behavior and graph schema relevant to stored data
- dedicated eval team

Recall code and scoring changes can be evaluated repeatedly against the same
persisted data. Changes to ingestion, embeddings, or stored graph semantics
require a separately confirmed clean reingestion.

## Reset safety

The scripts never delete or reset database state. To replace the V1 dataset,
stop the eval stack and explicitly inspect `tests/eval/runtime/v1` first. Remove
or archive it only after confirming that its database, identity, credentials,
and run artifacts are no longer needed.
