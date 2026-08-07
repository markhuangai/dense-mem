# Dense-Mem Public RAG Evaluation

This directory runs deterministic public retrieval evaluations against a
dedicated local Dense-Mem stack. Corpus ingestion uses the same public
`remember` workflow as production:

```text
corpus row
  -> POST /mcp, JSON-RPC tools/call remember
  -> asynchronous processing
  -> POST /mcp, JSON-RPC tools/call get_submission_status
  -> recall suite
  -> qrel-based retrieval metrics
```

The evaluation harness does not use legacy memory-pack import tools or an
answer-judge model. It measures retrieval against deterministic qrels and waits
for the bounded submission status projection before scoring.

The approved seed, generated diagnostic datasets, persistent databases,
credentials, and run artifacts are local-only and ignored by git. The full
release evaluation never runs in remote CI.

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
  seeds/                # approved or generated local seed packs
  suites/               # approved or generated local suites
  runtime/v1/
    dataset_identity.json
    eval_profile.json
    postgres/
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

The post-cutover evaluation flow uses `public_6axis_1k_v1` as the hard deterministic
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

## Issue #149 V2 relationship derivation

`public_6axis_1k_v2` is derived from the selected V1 cohort; it never replaces
or normalizes corpus evidence. It adds a flat `relationships` array to each
corpus row, so each imported `remember` request has the evidence plus its
client relationship proposal. The derive command copies every manifest-declared
evaluation artifact and the suite byte-for-byte, verifies the evidence identity
hash, and validates every generated row with the current public `remember`
contract before writing the V2 directory.

Before a V2 seed is imported, `validate-v2-cohort` binds the filtered V1
cohort to the frozen 1k V1 source through the committed cohort lock. It hard
fails if a source row or case is omitted without declaration, a retained JSONL
row changes or is reordered, a copied sidecar changes, or either seed hash
differs from the declared cohort.

```bash
go run ./cmd/eval-runner \
  --mode derive-v2 \
  --seed path/to/public_6axis_1k_v1/seed_manifest.json \
  --suite path/to/public_6axis_1k_v1/suite.jsonl \
  --relationship-ledger path/to/relationship_ledger.jsonl \
  --cohort-lock tests/eval/source_locks/public_6axis_1k_v2_cohort.json \
  --derived-seed-id public_6axis_1k_v2 \
  --out path/to/public_6axis_1k_v2

go run ./cmd/eval-runner \
  --mode validate-v2-cohort \
  --parent-v1-seed tests/eval/seeds/public_6axis_1k_v1/seed_manifest.json \
  --parent-v1-suite tests/eval/suites/public_6axis_1k_v1.jsonl \
  --filtered-v1-seed path/to/filtered_public_6axis_1k_v1/seed_manifest.json \
  --filtered-v1-suite path/to/filtered_public_6axis_1k_v1/suite.jsonl \
  --derived-v2-seed path/to/public_6axis_1k_v2/seed_manifest.json \
  --derived-v2-suite path/to/public_6axis_1k_v2/suite.jsonl \
  --cohort-lock tests/eval/source_locks/public_6axis_1k_v2_cohort.json

go run ./cmd/eval-runner \
  --mode compare-v2-cohort \
  --baseline-run path/to/filtered_v1_baseline \
  --candidate-run path/to/public_6axis_1k_v2_baseline \
  --parent-v1-seed tests/eval/seeds/public_6axis_1k_v1/seed_manifest.json \
  --parent-v1-suite tests/eval/suites/public_6axis_1k_v1.jsonl \
  --filtered-v1-seed path/to/filtered_public_6axis_1k_v1/seed_manifest.json \
  --filtered-v1-suite path/to/filtered_public_6axis_1k_v1/suite.jsonl \
  --derived-v2-seed path/to/public_6axis_1k_v2/seed_manifest.json \
  --derived-v2-suite path/to/public_6axis_1k_v2/suite.jsonl \
  --cohort-lock tests/eval/source_locks/public_6axis_1k_v2_cohort.json \
  --out path/to/v1_v2_comparison

go run ./cmd/eval-runner \
  --mode validate \
  --seed path/to/public_6axis_1k_v2/seed_manifest.json \
  --suite path/to/public_6axis_1k_v2/suite.jsonl \
  --out path/to/v2-validation
```

The output `v2_derivation_report.json` records the parent seed hash, evidence
identity hash, copied-artifact hashes, relationship contract count, excluded
ledger rows, and any source IDs that needed a documented sentence-bounded
fallback proposal. A fallback changes only the non-authoritative client
proposal; it never changes evidence, and the assessor still normalizes it to
an active team predicate or returns `needs_review`.

Generic `compare` remains same-seed only. `compare-v2-cohort` is the explicit
cross-seed path: it reruns cohort validation, binds the V1 and V2 run summaries
to the validated filtered and derived hashes, requires every retained case to
be scored, and writes `v2_cohort_comparison.json` with the cohort provenance.
The persistent monitor requires a release policy by default. A derived V2
comparison run must set `ALLOW_UNGATED_EVALUATION=1` and omit
`RELEASE_GATE_POLICY`; this records ordinary candidate metrics without
presenting the result as a release-gate decision.

## Use the approved local seed

The hard gate consumes the existing approved `public_6axis_1k_v1` seed and
suite. It does not generate, download, or replace them. Their required identity
is pinned by the committed policy: 206 cases and seed hash
`sha256:eb09124331228e59898a93740104ab978b9974e3ebf7f7fc2e09728ef95b3d78`.

Because these artifacts are ignored, a new worktree does not contain them.
Restore the approved local copy at these paths; do not substitute a regenerated
seed:

```bash
SEED=tests/eval/seeds/public_6axis_1k_v1/seed_manifest.json
SUITE=tests/eval/suites/public_6axis_1k_v1.jsonl
RELEASE_GATE=tests/eval/baselines/v2.1.1_public_6axis_1k_baseline.json
```

The preparation script refuses `--size 1000` so it cannot overwrite the
approved gate artifact. It may generate the optional diagnostic 5k seed:

```bash
python3 tests/eval/scripts/prepare_public_6axis_eval.py \
  --size 5000 \
  --force
```

The runner has no default seed or suite. This prevents an invocation from
silently evaluating the wrong corpus.

The old relational seed presets depended on typed claims and preloaded facts,
so they are retired. `cmd/eval-seedgen` retains the content-only
`local_eval_1k` preset.

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

After startup migrations finish, provision a dedicated team through the private
control API and keep its ignored credential file under the V1 runtime:

```bash
control_token="${CONTROL_PORTAL_TOKEN:?export CONTROL_PORTAL_TOKEN first}"
team_json="$(curl -fsS -X POST http://127.0.0.1:8090/control/api/teams \
  -H "Authorization: Bearer ${control_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"dense-mem-eval-v1"}')"
team_id="$(jq -r '.data.id' <<<"${team_json}")"

key_json="$(curl -fsS -X POST \
  "http://127.0.0.1:8090/control/api/teams/${team_id}/profiles" \
  -H "Authorization: Bearer ${control_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"eval profile"}')"
api_key="$(jq -r '.data.api_key' <<<"${key_json}")"

jq -n --arg team_id "${team_id}" --arg api_key "${api_key}" \
  '{team_id: $team_id, api_key: $api_key}' \
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
  --release-gate-policy "${RELEASE_GATE}" \
  --out tests/eval/runtime/v1/runs/validate
```

For `public_6axis_*` seeds, validation also requires the existing
`validation_report.json` named by the seed manifest. The report must have
status `passed`. The runner recomputes the complete seed hash and requires it,
the seed ID, and the case count to match the committed policy before any local
stack or import work begins.

## Import once, resume, and run recall

The long-running monitor is local and on-demand; nothing starts it
automatically or from remote CI. It builds the current runner, validates the
selected seed against the policy, imports through MCP `tools/call` `remember`,
waits for terminal placement, and then runs the baseline recall suite:

```bash
SEED="${SEED}" \
SUITE="${SUITE}" \
RELEASE_GATE_POLICY="${RELEASE_GATE}" \
IMPORT_CONCURRENCY=10 \
PLACEMENT_TIMEOUT=10m \
tests/eval/scripts/run_full_public_rag_eval_until_done.sh
```

The runner and monitor default to 10 concurrent import requests and reject a
higher value. The eval-only Compose overlay also limits embedding requests and
embedding workers to 10.

The monitor polls every 60 seconds by default. `SLEEP_SECONDS` changes only
the monitor cadence; it does not cap graph relationships or placement work.

Resume behavior is based on the latest placement attempt for each
`eval:<source_doc_id>`:

| Latest state | Resume action |
| --- | --- |
| `completed` and live fragment exists | Skip the corpus row. |
| `awaiting_review` and live fragment exists | Skip the corpus row and report review burden separately. |
| `failed` | Retry the corpus row. |
| No attempt | Import the corpus row. |
| `queued` or `processing` | Wait for the placement worker; do not duplicate it. |
| Completed checkpoint but fragment is missing | Retry the corpus row. |

One failed concurrent request stops scheduling new rows but allows already
active requests to finish. A later monitor pass continues from the latest
terminal placements instead of restarting the corpus.

The runtime identity contains the seed and suite hashes, release-policy hash,
MCP contract, runner binary hash, local server image ID, reviewer/verifier and
embedding configuration, team ID, and `remember` route. Import and baseline
artifacts also contain a canonical mapping hash. Any mismatch is a hard error.
If data exists without an identity file, the monitor refuses to adopt or erase
it.

Progress and placement analysis are written to:

```text
tests/eval/runtime/v1/monitor/status.json
tests/eval/runtime/v1/monitor/placement_summary.json
tests/eval/runtime/v1/monitor/completed_source_doc_ids.txt
tests/eval/runtime/v1/monitor/failed_source_doc_ids.txt
tests/eval/runtime/v1/runs/import/knowledge_mapping.json
tests/eval/runtime/v1/runs/baseline/summary.json
```

`placement_summary.json` reports latest completed/awaiting-review/failed/pending
counts, category and item-status counts, promotion rate, rejection rate, review
burden, and historical retry attempts. Recall starts only when all latest
placements are terminal (`completed` or `awaiting_review` with a live fragment),
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
- release policy and MCP tool contract
- runner binary and local server image
- embedding endpoint, model, and dimensions
- reviewer and verifier models
- canonical source-to-knowledge mapping
- ingestion behavior and graph schema relevant to stored data
- dedicated eval team

Resume and repeat runs may reuse the persisted data only with the same runner
binary and local server image. A changed runner, server image, ingestion path,
embedding configuration, or stored graph semantics must use a separate
`V1_DATA_DIR` and cleanly reimport the same approved seed. The monitor never
generates or replaces that seed.

## Reset safety

The scripts never delete or reset database state. To replace the V1 dataset,
stop the eval stack and explicitly inspect `tests/eval/runtime/v1` first. Remove
or archive it only after confirming that its database, identity, credentials,
and run artifacts are no longer needed.
