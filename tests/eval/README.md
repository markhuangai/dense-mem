# Dense-Mem Submission Evaluation

This directory evaluates retrieval through the production submission contract.
The client supplies exact-span entity and relationship proposals; the server
stages raw evidence, scans it, assesses it, promotes accepted evidence, and
then exposes the normal evidence-first recall model.

```text
v2 corpus row
  -> MCP remember (evidence + proposal)
  -> get_submission_status (completed + search current/not_required)
  -> evaluation mapping export
  -> evidence-first recall suite
  -> deterministic qrel scoring and comparison
```

The harness never uses `import_memories`, raw database writes, or an
answer-judge model. Generated seeds, credentials, databases, and run outputs
are local-only and ignored by git.

## V2 seed and its V1 comparison

`public_6axis_1k_v2` is a submission-ready derivation of the immutable
`public_6axis_1k_v1` corpus. It retains the V1 suite and qrels, but adds a
client proposal to every evidence row and applies narrowly logged security
normalizations where required.

| Check | Result |
| --- | --- |
| Parent seed hash | `sha256:eb09124331228e59898a93740104ab978b9974e3ebf7f7fc2e09728ef95b3d78` |
| V2 seed hash | `sha256:79c2c519c55b5e4335fb1b9eceba8a714a2311265348bd8ec3db33aa69b91966` |
| Corpus rows | 1,000 |
| Cases / qrels | 206 / 206, byte-identical to V1 |
| Row order and non-content projection | Equal |
| Runtime proposal + deterministic scan validation | Passed |
| Model-only semantic proposal audit | 1,000 rows, 0 warnings, 102 bounded regenerations |
| Security normalizations | 295, recorded by source ID and hashes only |

The ignored `comparison_report.json` lists every normalization without raw
evidence. The current derivation recorded 270 LongMemEval synthetic transcript
header removals, 22 UTF-8 mojibake repairs, one hidden-control removal, one
terminal control-directive removal, and one active imperative-override-clause
redaction. It also verifies that copied answers, cases, qrels, transforms,
licenses, and suite bytes match V1.

The evaluator binds every model-audit record to its exact corpus
`source_doc_id`, evidence SHA-256, raw proposal SHA-256, and audit-policy hash.
A report with a substituted row, duplicated ID, incomplete coverage, or
incorrect totals fails before it contacts Dense-Mem.

The seed audit creates semantically grounded client proposals only after the
deterministic write-time scan passes. The live Dense-Mem assessor remains the
authoritative security and normalization decision during import.

Use these local paths when the generated seed is available:

```bash
SEED=tests/eval/seeds/public_6axis_1k_v2/seed_manifest.json
SUITE=tests/eval/suites/public_6axis_1k_v2.jsonl
```

The committed V1 release-gate policy intentionally cannot be supplied for V2:
its seed identity and score baseline are V1-only. Establish and review a V2
baseline before declaring a new release gate; do not edit the V1 policy to
make a V2 run pass.

## Validate the seed before any live work

This reads the seed, validates exact spans with the Go submission contract,
checks deterministic security scanning, verifies the model-audit bindings, and
writes only ignored local artifacts.

```bash
go run ./cmd/eval-runner \
  --mode validate \
  --seed "${SEED}" \
  --suite "${SUITE}" \
  --out tests/eval/runs/public_6axis_1k_v2/validate
```

## Start an isolated V2 stack

Use a fresh runtime directory for each change to the server image, evaluator,
seed, provider configuration, or submission behavior. Do not reuse the old V1
runtime: it holds canonical knowledge created by a different write path.

```bash
export EVAL_COMPOSE_DATA_DIR="$(realpath -m tests/eval/runtime/v2)"

docker compose -p densemem_eval_submission \
  -f docker-compose.yml \
  -f tests/eval/docker-compose.eval.yml \
  up -d --build
```

Provision a dedicated team once, then keep its credential file in the ignored
runtime directory. Do not print or commit the generated API key.

```bash
mkdir -p tests/eval/runtime/v2

docker compose -p densemem_eval_submission \
  -f docker-compose.yml \
  -f tests/eval/docker-compose.eval.yml \
  run --rm --no-deps server \
  /app/provision-profile --name dense-mem-eval-v2 \
  > tests/eval/runtime/v2/eval_profile.json

chmod 600 tests/eval/runtime/v2/eval_profile.json
```

## Import, wait, and score

The monitor validates the seed, pins a runtime identity, imports through MCP,
waits for both submission completion and search readiness, exports the
canonical mapping, then runs the recall suite.

```bash
SEED="${SEED}" \
SUITE="${SUITE}" \
EVAL_DATA_DIR="$(realpath -m tests/eval/runtime/v2)" \
IMPORT_CONCURRENCY=3 \
SUBMISSION_TIMEOUT=10m \
tests/eval/scripts/run_full_public_rag_eval_until_done.sh
```

The monitor uses the top-level `eval:<source_doc_id>` idempotency key, so a
resume can safely reconstruct mapping artifacts without duplicating a completed
submission. Its status and compact diagnostics are written under:

It discovers the host ports of the named Compose project unless
`DENSE_MEM_BASE_URL` or `DENSE_MEM_CONTROL_URL` is explicitly supplied.

```text
tests/eval/runtime/v2/monitor/status.json
tests/eval/runtime/v2/monitor/submission_summary.json
tests/eval/runtime/v2/monitor/completed_source_doc_ids.txt
tests/eval/runtime/v2/monitor/blocked_source_doc_ids.txt
tests/eval/runtime/v2/runs/import/knowledge_mapping.json
tests/eval/runtime/v2/runs/baseline/summary.json
```

| Submission state | Monitor action |
| --- | --- |
| `completed` with search `current` or `not_required` | Resume-safe; include in the mapping. |
| `completed` with search `pending` | Wait for the embedding worker. |
| `completed` with search `failed` | Stop; retrieval would be incomplete. |
| `queued` or `processing` | Wait; do not submit a duplicate. |
| `rejected`, `quarantined`, or `failed` | Stop and report source IDs only. |

The monitor refuses a runtime with data but no matching identity file. Its
identity includes the seed and suite hashes, server image, runner binary,
provider configuration, team, MCP contract, `remember` import route, and
`get_submission_status` polling contract.

## Candidate comparison

For a retrieval-affecting change, run an approved V2 baseline and the candidate
from separate isolated runtimes using the same V2 seed and suite. Compare their
completed artifacts:

```bash
go run ./cmd/eval-runner \
  --mode compare \
  --baseline-run tests/eval/runtime/v2-baseline/runs/baseline \
  --candidate-run tests/eval/runtime/v2-candidate/runs/baseline \
  --out tests/eval/runs/public_6axis_1k_v2/comparison
```

The comparison checks the recorded seed hash before calculating retrieval,
context, evidence, and dream deltas. Keep the compact comparison result as PR
evidence; keep imported data, databases, credentials, seed copies, and full run
artifacts out of git.
