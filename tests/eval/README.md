# Dense-Mem Public RAG Evaluation

This directory runs deterministic public retrieval evaluations against a
dedicated local Dense-Mem stack. Corpus ingestion uses the same public
`remember` workflow as production:

```text
corpus row
  -> POST /mcp, JSON-RPC tools/call remember
  -> synchronous assessment, embedding, and commit
  -> recall suite
  -> qrel-based retrieval metrics
```

The evaluation harness does not use legacy memory-pack import tools or an
answer-judge model. It measures retrieval against deterministic qrels after the
terminal Remember result has been returned.

The normal release image contains no evaluation MCP tools. The committed
`examples/docker-compose.evaluation.yml` builds the separate `evaluation`
Docker target, whose binary is compiled with the `evaluation` build tag. That
target adds only `eval_list_knowledge_refs`, `eval_run_dream_cycle`, and
`eval_run_recall_case`; there is no runtime switch that can expose them in the
production binary.

The approved seed, generated diagnostic datasets, persistent databases,
credentials, and run artifacts are local-only and ignored by git. The full
release evaluation never runs in remote CI.

## Layout

```text
tests/eval/
  README.md
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
    eval_credential.json
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

The post-cutover release procedure retains `public_6axis_1k_v1` as a historical
deterministic comparison. Issue #291 explicitly waives that comparison for this
cutover; `public_6axis_5k_v1` remains diagnostic unless a later roadmap issue
promotes it.

The approved gate policy is committed at:

```text
tests/eval/baselines/v2.1.1_public_6axis_1k_baseline.json
```

A seed corpus row is plain evidence: `source_doc_id`, `content`, and optional
source metadata. Content is split rather than truncated, and the Go harness
rejects any corpus row above 999 Unicode code points. Legacy `claims` and
`auto_promote` fields are rejected because they bypass production extraction
and synchronous Remember.

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
an active team predicate or returns `unresolved`.

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
`local_eval_1k` preset and provides `local_eval_100` for a fast image/harness
smoke check.

## Run the 100-row smoke check

The `local_eval_100` CLI preset emits the versioned `local_eval_100_v2` seed
identity. It contains 100 corpus rows: 25 scored sanity cases, each with one
required document and three hard negatives. Generated seed, suite, database,
and run files remain ignored.

```bash
go run ./cmd/eval-seedgen \
  --preset local_eval_100 \
  --out tests/eval/seeds/local_eval_100 \
  --suite tests/eval/suites/local_eval_100.jsonl

export SEED=tests/eval/seeds/local_eval_100/seed_manifest.json
export SUITE=tests/eval/suites/local_eval_100.jsonl
export ALLOW_UNGATED_EVALUATION=1
export IMPORT_CONCURRENCY=5
export MIN_RECALL_AT_K=0.8429611650485437
export MIN_REQUIRED_RANK1_RATE=0.7621359223300971
export MAX_AVERAGE_BAD_AT_K=0
export MAX_BAD_RANK1_RATE=0
export MAX_UNMAPPED_SOURCE_REFS=0

tests/eval/scripts/run_full_public_rag_eval_until_done.sh
```

These retrieval floors mirror the historical public gate only to catch smoke
regressions. This run is not release evidence and does not replace the waived
1k comparison. The monitor requires all 100 latest Remember attempts and
fragments to be terminal with no failed rows before passing. Its ignored
`dataset_identity.json` binds the seed/suite hashes, runner hash, server image
ID, team, and model configuration; `gate_result.json` records the smoke
thresholds and metrics.

## Start the persistent V1 stack

The evaluation compose stores database state under `tests/eval/runtime/v1` by
default and reads the ignored repository-root `.env`. Set
`V1_COMPOSE_DATA_DIR` or `DENSE_MEM_EVAL_ENV_FILE` before both `up` and later
eval commands to use another local runtime root or env file.

```bash
export V1_COMPOSE_DATA_DIR="$(realpath -m tests/eval/runtime/v1)"
export DENSE_MEM_EVAL_ENV_FILE="$(realpath .env)"
export DENSE_MEM_EVAL_COMPOSE_PROJECT=densemem_eval_full

docker compose -p "${DENSE_MEM_EVAL_COMPOSE_PROJECT}" \
  -f examples/docker-compose.evaluation.yml \
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

credential_json="$(curl -fsS -X POST \
  "http://127.0.0.1:8090/control/api/teams/${team_id}/credentials" \
  -H "Authorization: Bearer ${control_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"eval credential"}')"
api_key="$(jq -r '.data.api_key' <<<"${credential_json}")"

jq -n --arg team_id "${team_id}" --arg api_key "${api_key}" \
  '{team_id: $team_id, api_key: $api_key}' \
  > tests/eval/runtime/v1/eval_credential.json

chmod 600 tests/eval/runtime/v1/eval_credential.json
```

The monitor also accepts `DENSE_MEM_API_KEY` and `EVAL_TEAM_ID` directly when
`CREDENTIAL_PATH` is not used. Never commit the credential file or print its API key
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
and then runs the baseline recall suite after each Remember call has returned a
terminal result:

```bash
SEED="${SEED}" \
SUITE="${SUITE}" \
RELEASE_GATE_POLICY="${RELEASE_GATE}" \
IMPORT_CONCURRENCY=10 \
tests/eval/scripts/run_full_public_rag_eval_until_done.sh
```

The runner and monitor default to 10 concurrent import requests and reject a
higher value. The eval-only Compose overlay also limits embedding requests to
10.

The monitor polls every 60 seconds by default. `SLEEP_SECONDS` changes only
the monitor cadence; it does not control terminal Remember processing.

Resume behavior is based on the latest Remember attempt for each
`eval:<source_doc_id>`:

| Latest state | Resume action |
| --- | --- |
| `completed` and live fragment exists | Skip the corpus row. |
| `failed` | Stop the monitor; retry the same idempotency key only when the failure is marked retryable, otherwise investigate before using a fresh isolated team/runtime. |
| No attempt | Import the corpus row. |
| Completed checkpoint but fragment is missing | Retry the corpus row. |

One failed concurrent request stops scheduling new rows but allows already
active requests to finish. The monitor then fails instead of retrying the
stable idempotency key. After correcting the cause, use a fresh isolated
team/runtime; non-failed interruptions still resume from the latest Remember
attempts.

The runtime identity contains the seed and suite hashes, release-policy hash,
MCP contract, runner binary hash, local server image ID, reviewer/verifier and
embedding configuration, team ID, and `remember` route. Import and baseline
artifacts also contain a canonical mapping hash. Any mismatch is a hard error.
If data exists without an identity file, the monitor refuses to adopt or erase
it.

Progress and Remember-import analysis are written to:

```text
tests/eval/runtime/v1/monitor/status.json
tests/eval/runtime/v1/monitor/attempt_summary.json
tests/eval/runtime/v1/monitor/completed_source_doc_ids.txt
tests/eval/runtime/v1/monitor/failed_source_doc_ids.txt
tests/eval/runtime/v1/runs/import/knowledge_mapping.json
tests/eval/runtime/v1/runs/baseline/summary.json
```

`attempt_summary.json` reports latest completed/failed counts and historical
retry attempts. Recall starts only when all latest Remember attempts are
completed with a live fragment, there are no failed latest attempts, the
team-scoped eval fragment count equals `counts.corpus`, and the Remember-only
import artifacts exist.

## Run recall again without reimporting

Once ingestion is complete, reuse the persisted graph and mapping:

```bash
set -a
. ./.env
set +a
export DENSE_MEM_API_KEY="$(jq -r .api_key tests/eval/runtime/v1/eval_credential.json)"

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

## Search and Recall read-pipeline benchmark

Issue #456 uses a separate disposable PostgreSQL benchmark. It does not consume
the public 1k evaluation dataset. The benchmark seeds deterministic evidence,
one supported Relationship, a credential-private evidence item, fixed vectors,
and an HNSW index through the real PostgreSQL adapters.

Run it after the plan-conformance audit passes:

```bash
mkdir -p tests/eval/runs/issue-456
env -u DATABASE_URL DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 \
  go test -tags=integration ./internal/recall/postgres \
  -run '^$' -bench '^BenchmarkRecallReadPipeline$' \
  -benchtime=200x -benchmem -count=5 \
  > tests/eval/runs/issue-456/benchmark.txt 2>&1

python3 tests/eval/scripts/compare_recall_read_performance.py \
  --input tests/eval/runs/issue-456/benchmark.txt \
  --output tests/eval/runs/issue-456/comparison.json
```

The fixture analyzes its search tables after creating the HNSW index so query
plans are settled before timing. Each workload uses `pair_a` and `pair_b` slots
that alternate telemetry mode across the five repetitions. Every run has 20
warmups and 200 measured reads, and reports its mode as `telemetry-enabled/op`.
The comparator rejects a missing or incorrect mode sequence and checks
median p50 and p95 increases against the greater of 5% or 1 ms, requires exact
per-operation SQL-statement and transaction counts between the two modes, and
records allocations and the measured source fingerprint. It fails when a
workload, repetition, metric, or stable result is missing or when either
latency limit is exceeded. GORM statement counts exclude driver transaction
begin and commit calls; those are reported separately as transaction counts.
Preserve raw benchmark output under the ignored
`tests/eval/runs/issue-456` directory; commit only the compact baseline summary
with the measured source commit.

## Issue #457 relationship projection query comparison

After the plan audit passes, compare the exact base commit and candidate on the
same host without concurrent test load. Keep the base production files at the
recorded commit. Copy only these candidate test files into the detached base
checkout: `relationship_projection_query_contract_test.go`,
`relationship_projection_selection_integration_test.go`, and
`recall_read_performance_benchmark_test.go` from `internal/recall/postgres/`.
Record their digests as the test-only base overlay.

In each checkout, run `capture_issue_457_projection_run.py` from the candidate
checkout, using its absolute path when the current directory is the base
checkout. Supply `--benchmark-output` and `--query-report` under that checkout's
ignored `tests/eval/runs/issue-457/` directory. The capture runs
`TestRelationshipProjectionQueryContract` and `BenchmarkRecallReadPipeline`,
then writes a source lock beside the benchmark log. The query report records
executed SQL, bound arguments, and decoded results for Search
readiness/full-text/exact-vector and Recall
readiness/full-text/exact-vector/ANN/expansion/hydration. Run the real
PostgreSQL `TestRelationshipProjectionANNAndAllTeamReadinessKeepDistinctGenerationPolicies`
with `DENSE_MEM_REPOSITORY_TESTCONTAINERS=1`, `DATABASE_URL` unset, and
`DENSE_MEM_PROJECTION_PLAN_REPORT` set under the same ignored directory. Compare
the emitted `EXPLAIN (ANALYZE, BUFFERS)` plan node types, scans, joins, index
choices, and relevant row/buffer counts; record any plan drift explicitly.

The capture runs `BenchmarkRecallReadPipeline` in both checkouts with 20
warmups, 200 measured iterations, five repetitions, `-benchmem`, and identical
test-only overlay. Pass the raw logs, base SHA, and two query reports to
`compare_recall_read_performance.py` using `--baseline-input`,
`--baseline-source-sha`, `--baseline-query-report`, and
`--candidate-query-report` before either checkout changes. The comparator
checks each log and query report against the capture-time source lock and
requires the nine planned query cases with 15 statements. Base comparisons
require both query reports from their respective checkouts.
Its existing telemetry-mode gate remains active.
The base checkout's telemetry-mode result is recorded as a diagnostic; the
candidate telemetry result and paired base-to-candidate comparison are gates.
The additional source gate requires identical SQL, bound arguments, decoded
results, statement counts, and transaction counts; each workload and mode must
keep median p50 and p95 increases within the greater of 5% or 1 ms. Commit a
compact comparison with the measured source fingerprints and keep raw logs,
query reports, plans, and comparison output ignored.

## Issue #458 Recall retrieval ownership comparison

Run the inherited benchmark and source comparator on the exact base and the
candidate after the plan audit passes. Keep the two checkouts' production code
at their measured revisions. The baseline checkout may receive only the
candidate's benchmark, retrieval-equivalence, and integration fixture helper
test files as a recorded test-only overlay. Use a unique subdirectory of
`tests/eval/runs/issue-457/` in each checkout because the existing query
capture restricts reports to that ignored prefix.

Run `TestRecallRetrievalEquivalenceCorpus` with
`DENSE_MEM_RECALL_EQUIVALENCE_REPORT` set to an absolute path under that
checkout's ignored `tests/eval/runs/` directory. The test checks selected
database IDs against fixed seeded IDs and emits ordered IDs, contexts,
ordering, scores, search states, and statement/transaction counts. Require
byte-identical base and candidate reports. The benchmark comparator then
requires unchanged SQL and transaction counts and median p50 and p95 increases
within the greater of 5% or 1 ms across five measured runs. Commit only a
compact comparison with both source fingerprints; keep raw reports ignored.

## Issue #493 usage flush scaling

After the independent plan audit passes, run the current Operations service and
PostgreSQL repository benchmark against disposable PostgreSQL:

```bash
env -u DATABASE_URL DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 \
  go test -tags=integration ./internal/operations/postgres \
  -run '^$' -bench '^BenchmarkUsageFlushScaling$' \
  -benchtime=200x -count=5 -benchmem -timeout=60m -v
```

Capture the output under an unused `tests/eval/runs/issue-493/` directory and
record source hashes and Go, PostgreSQL, CPU and host-idleness provenance before
and after the run. Each of the 45 workloads runs 20 warmups and 200 measured
operations across five repetitions. Go also performs a one-operation calibration;
only `usage_flush_scaling_result` records with `iterations: 200` belong to the
measurement. Join each record to its Go benchmark allocation result. Commit the
compact summary to `tests/eval/baselines/usage_flush_scaling.json`; raw runs stay
ignored. Pending evidence is replaced only after verified measurement.

The matrix covers 1, 100 and 1,000 service buckets without credentials, with one
credential per owner, and with ten credentials per owner. The one-bucket grouped
case has one credential; credential fan-out does not apply without credentials.
The grouped case still performs one owner write per service bucket, although
multiple writes target the same durable owner row. Inserts and updates use fresh
flush IDs. Replay primes a real committed flush with a test-only lost-reply
signal, then times the service retry with the same ID.

Each operation resets the fixture outside timing. SQL, transaction and repository
timing instrumentation forwards calls to the real database. Data SQL is split
into ledger, owner and credential statements; RLS setup and unknown statements
are counted separately. Counts describe connection-pool SQL calls, not driver
protocol messages. Transaction begin, commit and rollback are separate counts.
Clock and counter instrumentation remains inside the measured call.

The concurrent-update case coordinates the first producer event after the map
drains, then allows remaining events during the repository call. Coordination
waits, including that first producer event, are excluded from flush timing and
Go's allocation window. All producer latencies are reported. Allocations cover
the process while the benchmark timer runs, including remaining producer work;
they are not isolated per-goroutine allocations. The recording-only control times
recording into the same prefilled map without a flush. Every sample verifies all
owner and credential rows, additive counts, maxima and timestamps outside timing;
the subsequent flush proves concurrent events were retained.

The baseline compares actual data-statement counts with `1+B+C` for writes,
one ledger statement for replay, and zero SQL for recording-only operations.
It records discrepancies rather than substituting the predicted count. The
batching recommendation is conditional on owner/credential SQL consuming over
half of flush time at 100 or 1,000 buckets. No production workload frequency or
batching latency improvement is inferred.

A future bounded candidate groups the complete owner primary key, sums additive
counters, takes the maximum latency and latest timestamp, and writes owner and
credential rows in chunks of at most 1,000 rows. Grouping prevents duplicate
`ON CONFLICT` targets within one statement. The existing flush-ledger transaction,
RLS context, credential attribution and all-or-nothing rollback must remain.
This ticket contains no batching prototype or production algorithm change.

## Private Session Gate (#218)

The issue records the approved historical-1k waiver. Generate the frozen 32-case
cohort and twelve existing Remember payload controls into an ignored path:

```bash
python3 tests/eval/scripts/generate_session_ingest_v1.py --output tmp/session-ingest/cohort.json
```

Use disposable production stacks with isolated PostgreSQL databases for the
recorded base and candidate. Enable `SESSION_INGEST_ENABLED=true` only on the
candidate. The provider accounting proxy forwards the configured real verifier
and embedding endpoints without retaining prompts, credentials or responses:

```bash
node tests/eval/scripts/session_provider_proxy.mjs
```

Point both stack provider URLs at the proxy's `/v1` route. Supply the real model
and embedding contract unchanged. Never run this gate against a shared database.
For each stack set `DENSE_MEM_USER_URL`, `DENSE_MEM_CONTROL_URL`,
`DENSE_MEM_CONTROL_TOKEN`, `DENSE_MEM_EVAL_POSTGRES_CONTAINER`,
`DENSE_MEM_EVAL_PROXY_URL`, and `DENSE_MEM_EVAL_SOURCE_SHA`; provide the disposable
PostgreSQL user/database through `POSTGRES_USER`/`POSTGRES_DB` when nondefault.
The runner creates fresh private actors and reads canonical facts from that
isolated PostgreSQL container. It never includes credentials in its output.
Chat input/output counts require provider-reported usage. When an embedding
provider omits usage, the report explicitly counts those calls under
`embedding_usage_unavailable`; it reports no estimated or complete combined
token total. This measurement clarification was approved for #218; quality,
provenance, isolation and deadline requirements remain unchanged.

```bash
node tests/eval/scripts/run_session_ingest_v1.mjs remember tmp/session-ingest/cohort.json tmp/session-ingest/base.json
node tests/eval/scripts/run_session_ingest_v1.mjs remember tmp/session-ingest/cohort.json tmp/session-ingest/candidate.json
node tests/eval/scripts/run_session_ingest_v1.mjs quality tmp/session-ingest/cohort.json tmp/session-ingest/quality.json
python3 tests/eval/scripts/compare_session_ingest_v1.py --quality tmp/session-ingest/quality.json --base tmp/session-ingest/base.json --candidate tmp/session-ingest/candidate.json --out tmp/session-ingest/comparison.json
```

Run the first command against base and the remaining commands against candidate.
Quality runs three fresh repetitions. Every admissible call must finish within
180 seconds; each repetition requires precision ≥98%, recall ≥95%, complete
provenance, designated boundary facts and actor isolation. All Remember controls
must retain expected facts. Reports include processing latency percentiles,
structured provider turns, embedding turns and provider-reported chat token usage.
Missing chat usage fails the gate. Failures retain an incomplete report and stop;
thresholds and expected facts must not be weakened after a run.
