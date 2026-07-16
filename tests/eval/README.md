# Dense-Mem Public RAG Evaluation

This directory runs deterministic public retrieval evaluations against a
dedicated local Dense-Mem stack. Corpus ingestion uses the same public
`remember` workflow as production:

```text
corpus row
  -> POST /mcp tools/call remember
  -> asynchronous AI placement
  -> POST /mcp tools/call get_memory_placement
  -> exactly one production recall per case
  -> qrel retrieval metrics + exact-initial-response AI judge
```

The evaluation harness does not use `import_memories`, direct database writes,
`assemble_context`, a second retrieval, or a judge-driven discovery loop. It
reports deterministic qrel metrics over `results[].evidence_id`, separate
discovery exposure from `discovery_paths[].evidence_ids`, recall latency,
placement outcomes, and an optional closed-schema AI judgment of the exact
initial response.

Generated datasets, persistent databases, credentials, and run artifacts are
local-only and ignored by git.

## Layout

```text
tests/eval/
  README.md
  docker-compose.eval.yml
  scripts/
    prepare_public_6axis_eval.py
    prepare_public_semantic_eval.py       # legacy 3-axis semantic builder
    prepare_full_public_rag_eval.py      # legacy BEIR utility
    run_full_public_rag_eval_until_done.sh
  data/                 # downloaded public datasets
  seeds/                # generated seed packs
  suites/               # generated suites
  .runtime/
    final-v1/            # isolated main-branch runtime and baseline artifacts
    final-v2/            # isolated branch runtime and candidate artifacts
    final-comparison/    # paired summary and case deltas
```

## Public six-axis seeds

| Axis | Dataset | Purpose |
| --- | --- | --- |
| `scifact` | BEIR SciFact | scientific claim/document retrieval. |
| `msmarco` | BEIR MS MARCO | web-passage retrieval. |
| `hotpotqa` | BEIR HotpotQA | public multi-hop document retrieval. |
| `musique` | MuSiQue answerable dev v1.0 | 2/3/4-hop relationship retrieval. |
| `qasper` | QASPER train/dev v0.3 | Paper QA evidence retrieval across extractive, freeform, yes/no, and mixed answers. |
| `longmem_oracle` | LongMemEval-S cleaned | Long-memory chat recall using non-abstention oracle rows. |

The active comparison seeds are:

```text
tests/eval/seeds/public_6axis_1k_v1
tests/eval/seeds/public_6axis_5k_v1
tests/eval/suites/public_6axis_1k_v1.jsonl
tests/eval/suites/public_6axis_5k_v1.jsonl
```

`public_6axis_1k_v1` has exactly 1,000 corpus rows and is the smoke comparison.
`public_6axis_5k_v1` has exactly 5,000 corpus rows and is the primary
comparison. The 1k seed uses the same stable ID namespace and deterministic
ordering as the 5k seed so it can be validated as a strict subset.

A seed corpus row is plain evidence: `source_doc_id`, `content`, and optional
source metadata. Content is split, never truncated, and the Go harness rejects
any corpus row above 999 Unicode code points. Legacy `claims` and
`auto_promote` fields are rejected because they bypass production extraction
and placement.

## Prepare a seed

Generate both six-axis seeds:

```bash
python3 tests/eval/scripts/prepare_public_6axis_eval.py \
  --size 1000 \
  --force

python3 tests/eval/scripts/prepare_public_6axis_eval.py \
  --size 5000 \
  --force
```

Use the checked local paths explicitly in every command:

```bash
SEED=tests/eval/seeds/public_6axis_1k_v1/seed_manifest.json
SUITE=tests/eval/suites/public_6axis_1k_v1.jsonl
```

The runner has no default seed or suite. This prevents an invocation from
silently evaluating the wrong corpus.

Validate before starting any stack:

```bash
go run ./cmd/eval-runner \
  --mode validate \
  --seed "${SEED}" \
  --suite "${SUITE}" \
  --out tests/eval/.runtime/validate-public-6axis-1k
```

The generator writes `validation_report.json`; `eval-runner` refuses
`public_6axis_*` seeds when the report is missing, failed, or hash-mismatched.
Before final generation, stop eval compose projects and clear ignored
`tests/eval/seeds/`, `tests/eval/suites/`, and `tests/eval/.runtime/` contents.
Preserve `tests/eval/data/` and tracked source files.

## Start the persistent eval stack

The default override stores database state under `tests/eval/.runtime/v1`.
Set `EVAL_DATA_DIR` and `EVAL_COMPOSE_DATA_DIR` before both `up` and later
eval commands to use another isolated runtime root. `V1_COMPOSE_DATA_DIR`
remains supported for compatibility. The v2 eval stack is Postgres-only.
For v2 manual Compose commands, also export `V2_COMPOSE_DATA_DIR` to the same
runtime root because the v2 override mounts Postgres, Redis, and Prometheus
from that variable. Missing eval env falls back to default host ports and the
default runtime root; stop and re-render `docker compose config` before
starting containers if the rendered ports or mount paths are not the intended
eval runtime.

```bash
export EVAL_DATA_DIR="$(realpath -m tests/eval/.runtime/v1)"
export EVAL_COMPOSE_DATA_DIR="${EVAL_DATA_DIR}"
export EVAL_COMPOSE_PROJECT=densemem_eval_full
export EVAL_COMPOSE_OVERRIDE=tests/eval/docker-compose.eval.yml

docker compose -p "${EVAL_COMPOSE_PROJECT}" \
  -f docker-compose.yml \
  -f "${EVAL_COMPOSE_OVERRIDE}" \
  up -d --build
```

For the PostgreSQL-only v2 side of a paired local comparison, use
`EVAL_COMPOSE_OVERRIDE=tests/eval/docker-compose.eval-v2.yml`,
`EVAL_COMPOSE_PROJECT=densemem_eval_final_v2`, and an isolated
`EVAL_DATA_DIR`, for example `tests/eval/.runtime/final-v2`. This override does
not enable the legacy Neo4j Compose profile.

Do not use this team or stack for manual memory work. The monitor assumes one
seed dataset per runtime root.

## Provision the eval team once

After migrations finish, provision a dedicated team and keep its ignored
credential file under the runtime root:

```bash
docker compose -p "${EVAL_COMPOSE_PROJECT}" \
  -f docker-compose.yml \
  -f "${EVAL_COMPOSE_OVERRIDE}" \
  run --rm --no-deps server \
  /app/provision-profile --name dense-mem-eval-v1 \
  > tests/eval/.runtime/v1/eval_profile.json

chmod 600 tests/eval/.runtime/v1/eval_profile.json
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
  --out tests/eval/.runtime/v1/runs/validate
```

## Import once, resume, and run recall

The long-running monitor is on-demand; nothing starts it automatically. It
builds the current runner, validates the selected seed, imports through
`remember`, waits for terminal placement and derived embeddings, then runs one
production recall per case followed by deterministic and AI-judge scoring:

```bash
SEED="${SEED}" \
SUITE="${SUITE}" \
EVAL_DATA_DIR="${EVAL_DATA_DIR}" \
EVAL_COMPOSE_DATA_DIR="${EVAL_COMPOSE_DATA_DIR}" \
EVAL_COMPOSE_PROJECT="${EVAL_COMPOSE_PROJECT}" \
EVAL_COMPOSE_OVERRIDE="${EVAL_COMPOSE_OVERRIDE}" \
IMPORT_CONCURRENCY=30 \
PLACEMENT_TIMEOUT=30m \
DENSE_MEM_EVAL_TOOL_TRANSPORT=mcp \
EVAL_RUN_MODE=baseline \
AI_JUDGE_ENABLED=true \
AI_JUDGE_MODEL=gpt-5.5 \
AI_JUDGE_CONCURRENCY=10 \
AI_JUDGE_TIMEOUT=2m \
tests/eval/scripts/run_full_public_rag_eval_until_done.sh
```

The monitor polls every 60 seconds by default. `SLEEP_SECONDS` changes only
the monitor cadence; it does not cap placement work.
The v2 eval compose override sets `AI_VERIFIER_TIMEOUT_SECONDS=240`, so the
placement timeout must remain above the server retry envelope
(`AI_VERIFIER_TIMEOUT_SECONDS * MEMORY_PLACEMENT_MAX_ATTEMPTS`) plus polling
overhead.

The one-recall evaluator does not forward legacy case-label accommodations such
as `use_communities`. Its initial request contains the query, limit, temporal
filters, and follow-up anchors only. Hypotheses are server-controlled by the
team dream setting, not by an evaluation request flag, so both versions are
measured on the same default production-recall contract.

The public semantic corpus importer sends one evidence row per production
`remember` call. `IMPORT_CONCURRENCY` controls how many of those calls run at
once; it does not change server-side worker policy. Failed latest placements
are retried on a later monitor pass; completed corpus rows are not remembered
again.

Resume behavior is based on the latest placement attempt for each
`eval:<source_doc_id>`:

| Latest state | Resume action |
| --- | --- |
| `completed` and mapped relationship/evidence ref exists | Skip the corpus row. |
| `failed` | Retry the corpus row. |
| No attempt | Import the corpus row. |
| `queued` or `processing` | Wait for the placement worker; do not duplicate it. |
| Completed checkpoint but mapped ref is missing | Retry the corpus row. |

Monitor counts and resume snapshots are item-level by evidence
`idempotency_key`; they are not raw `memory_placement_runs` row counts. Run
status drives queued/processing/completed/failed progress for each evidence
item.

One failed concurrent request stops scheduling new rows but allows already
active requests to finish. A later monitor pass continues from the latest
completed placements instead of restarting the corpus.

The runtime identity contains the seed and suite hashes, embedding
model/dimensions/endpoint hash, verifier and judge models, run role, server
revision, instrumentation hash, evaluator revision/dirty-state fingerprint,
team ID, `remember` route, and tool transport. A mismatch is a hard error. If
data exists without an identity file, the monitor refuses to adopt or erase it.

Progress and placement analysis are written to:

```text
tests/eval/.runtime/v1/monitor/status.json
tests/eval/.runtime/v1/monitor/placement_summary.json
tests/eval/.runtime/v1/monitor/completed_source_doc_ids.txt
tests/eval/.runtime/v1/monitor/failed_source_doc_ids.txt
tests/eval/.runtime/v1/runs/import/knowledge_mapping.json
tests/eval/.runtime/v1/runs/baseline/summary.json
```

`placement_summary.json` reports latest completed/failed/pending counts,
category and item-status counts, promotion rate, rejection rate, and historical
retry attempts. Recall starts only when all latest placements are completed,
there are no failed or pending latest attempts, the mapped relationship/evidence
ref count equals `counts.corpus`, remember-only import artifacts exist, and all
required semantic embedding/search-document work is current with zero failures.

The recall artifacts include:

```text
recall_traces.jsonl       exact production initial_response and ranked refs
retrieval_scores.jsonl    direct top-K and separate discovery scores
summary.json              aggregate retrieval and recall-latency metrics
judge_scores.jsonl        one gpt-5.5 judgment per exact model/input hash
judge_summary.json        aggregate answerability/relevance/completeness/faithfulness
```

The judge cannot call Dense-Mem, follow `discovery_guidance`, or use its own
knowledge to fill a missing fact. Invalid judge JSON receives the exact
validation error in the same conversation and must be regenerated completely.
An interrupted judge pass reuses only records whose case ID, model, and input
SHA-256 still match.

## Run recall again without reimporting

Once ingestion is complete, reuse the persisted Postgres state and mapping:

```bash
set -a
. ./.env
set +a
export DENSE_MEM_API_KEY="$(jq -r .api_key tests/eval/.runtime/v1/eval_profile.json)"

go run ./cmd/eval-runner \
  --mode baseline \
  --seed "${SEED}" \
  --suite "${SUITE}" \
  --out tests/eval/.runtime/v1/runs/baseline-rerun \
  --mapping tests/eval/.runtime/v1/runs/import/knowledge_mapping.json \
  --tool-transport mcp \
  --max-page-size 500 \
  --ai-judge \
  --judge-model gpt-5.5 \
  --judge-concurrency 10 \
  --judge-timeout 2m
```

Use `--mode candidate` for a candidate run, then compare two run directories:

```bash
go run ./cmd/eval-runner \
  --mode candidate \
  --seed "${SEED}" \
  --suite "${SUITE}" \
  --out tests/eval/.runtime/v1/runs/candidate \
  --mapping tests/eval/.runtime/v1/runs/import/knowledge_mapping.json \
  --tool-transport mcp \
  --max-page-size 500

go run ./cmd/eval-runner \
  --mode compare \
  --baseline-run tests/eval/.runtime/v1/runs/baseline \
  --candidate-run tests/eval/.runtime/v1/runs/candidate \
  --out tests/eval/.runtime/v1/runs/comparison
```

Compare mode reads and checks the seed hashes recorded in the two completed
summaries, so it does not take seed or suite paths. If judge artifacts exist,
both sides must have the same exact judge model and case count; compare mode
then reports judge deltas alongside direct retrieval, discovery, and latency
deltas. The first v1/v2 report is observational and applies no quality gate.

## When the persisted dataset is reusable

Reuse `tests/eval/.runtime/v1` when all of these remain unchanged:

- seed contents and source document IDs
- suite contents
- embedding endpoint, model, and dimensions
- ingestion behavior and semantic schema relevant to stored data
- dedicated eval team

Recall code and scoring changes can be evaluated repeatedly against the same
persisted data. Changes to ingestion, embeddings, or stored semantic-memory data
require a separately confirmed clean reingestion.

## Reset safety

The scripts never delete or reset database state. To replace an eval dataset,
stop the eval stack and explicitly inspect `tests/eval/.runtime/v1` first. Remove
or archive it only after confirming that its database, identity, credentials,
and run artifacts are no longer needed.
