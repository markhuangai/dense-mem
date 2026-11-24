# Runner evaluation and public dataset seeds

Build public dataset seeds with local Docker. Evaluation campaigns and benchmarks
run on GitHub-hosted runners. Selective synthetic unit and policy checks use
`bash scripts/test-evaluation.sh`.

## Build public dataset seeds

Use the [seed-building skill](../../.agents/skills/dense-mem-build-seeds/SKILL.md)
or run the builder from a clean tracked checkout on a Linux amd64 machine with a
local Docker daemon, Node.js 24, Git, `flock` and an authenticated GitHub CLI. The builder
compiles its Go helper in Docker from the checked-out commit.

| Argument | Default | Meaning |
| --- | --- | --- |
| `--pr` | Current main release | Use the trusted image for your own open, non-draft same-repository PR |
| `--dataset` | `all` | All five complete public packs, or one named pack |
| `--workers` | `20` | Concurrent Remember requests per dataset, from 1–100 |
| `--skip-ai-security-check` | Off | Skip contextual AI review for the pinned public rows in this build |
| `--resume <build-id>` | New build | Continue a saved local build with its original settings and dated tags |

For a PR, check out the exact head and wait for the trusted production image,
Repository CI, CodeQL analysis, CodeQL and Production image E2E. On current
main, omit `--pr` to use its published release after main CI and the release's
merged-PR checks pass. The builder requires repository-admin access and checks
the source and validation again before public pushes.

```bash
node --env-file=eval.env tests/eval/scripts/local-seeds.mjs --pr 777
```

The builder uses the shared empty bootstrap and processes one dataset at a time.
When an import stops, the builder keeps its PostgreSQL volume on the local
Docker daemon. It removes the other owned runtime resources during cleanup and
keeps the private build receipt under `tmp/eval-seeds/`. Resume using the printed
`local-…` build ID and the same tested checkout and evaluation credentials.

```bash
node --env-file=eval.env tests/eval/scripts/local-seeds.mjs --resume "$build_id"
```

Set `build_id` to the ID printed by the builder. Resume uses the saved dataset
selection, workers, admission setting, bootstrap digest and dated tags. The
builder checks the retained database against the pinned requests, imports
unfinished rows, and reuses completed publications. It removes the checkpoint
volume after successful publication. Earlier builds whose volumes were
removed need a fresh import. After resume, the preparation time in the seed
receipt measures the successful invocation. Earlier import times remain in
that build's logs.

Each dataset restores the bootstrap, imports its complete corpus with Topic
maintenance disabled and paused, verifies the source/partition mapping and
search state, and exports a private PostgreSQL archive. The image build restores it once into a
clean cluster and bakes the stopped data directory, including indexes, into a
bootable image. Before publication the builder starts that image in fresh
storage and verifies startup, source mappings, canonical and search fingerprints,
and search convergence. Operation-log and Remember diagnostic tables retain
their schemas with empty payloads.

The optional switch sets `DANGEROUSLY_SKIP_AI_SECURITY_CHECK` for the local
import instance. Remember requires the original request hash and operation key
in a read-only manifest bound to the authenticated bootstrap identity and
shared space. The initial structural scan still applies. The seed and each
assessment record `ai_security_review=skipped`. Restore starts the application
with ordinary AI review enabled.

At twenty workers the application pool has fifty connections: twenty may be
held by request advisory locks while foreground queries and background activity
use the remainder. PostgreSQL also reserves connections for the separate logging
and observer pools and administration. Capacity is not a throughput guarantee.
Target at least forty completed documents/minute and aim for sixty for deadline
margin; confirm this over sustained live ingestion, including provider latency.

A confirmed HTTP 429 pauses new Remember requests from all seed import
workers for 60 seconds. Responses from requests already in flight can extend
the pause. The importer requeues the same request and idempotency key, with up
to five retries shared with the retryable network failures. Cancellation and
the dataset deadline interrupt the wait.

Import progress and resource logs appear initially, every sixty seconds and at
completion. Progress includes completed/failed/remaining documents and the
average documents/minute for rows imported in the current invocation. Resource
records share the run identity and timestamp with progress: interval-averaged
host CPU and I/O wait, available memory, disk
free, sampled container CPU/memory, and PostgreSQL active connections/lock waits.
Container CPU is a sampled per-core percentage and can exceed 100%. Missing
measurements have explicit unavailable reasons. Application pool wait counters
are measured by the authenticated sustained PostgreSQL regression; the production
runtime does not expose them. Resource sampling does not print environment
variables, SQL text, provider URLs or credentials.

The public registry package is `ghcr.io/markhuangai/dense-mem-eval-postgres`:

```text
hotpotqa-20261116T150001234Z-v2.8.1-rc.5
hotpotqa-latest
```

Dated tags are immutable. Their release suffix is the release associated with
current main, including release candidates; during PR testing it names that base
release. The timestamp prevents same-day rebuild collisions. Each successful
build updates only its dataset's latest alias. Run the publisher from one
designated Linux host and OS account. Builds from different checkouts on that
host share a per-alias lock during publication. A competing publisher or a
stale or ambiguous latest generation produces an explicit conflict and keeps
the verified build available to resume. The bootstrap alias is created once.
Dataset images contain a
PostgreSQL 18/pgvector cluster on the pinned base, metadata, mappings and source
notices. The private archive is a build input. The shared bootstrap uses a
logical archive under the same package with a `bootstrap-` prefix.

Supply the following dedicated settings through the environment or a private,
ignored `eval.env` file with mode `0600`. Configure the corresponding Secrets
for hosted scoring separately.

- `DENSE_MEM_EVAL_API_KEY` and `DENSE_MEM_EVAL_VERIFIER_URL` for generation
- `DENSE_MEM_EVAL_EMBED_API_KEY` for OpenAI embeddings
- `DENSE_MEM_EVAL_TEAM_API_KEY` for the shared evaluation team credential
- `DENSE_MEM_EVAL_DATABASE_PASSWORD` for the isolated PostgreSQL instances

The local verifier may use HTTP or HTTPS and must be reachable from Docker.
Log Docker into `ghcr.io` with `write:packages` access to the public seed package.
Use `--password-stdin` and retrieve the registry token from your secret manager.

The bootstrap preserves the evaluation team, credential, owner and space IDs
across isolated dataset databases. It stores the credential hash. Plaintext
keys and database login passwords stay outside the images. A mismatched team
key fails before ingestion. The seeded PostgreSQL entrypoint applies the
configured database password through a local socket before accepting TCP
connections. Each Dense-Mem runtime generates its own control token.

## Run an evaluation

Use **Actions → Run evaluation → Run workflow**, select `main`, and enter a
Dense-Mem production image tag such as `test-777`, `v2.8.1-rc.6`, or `v2.8.1`.
You can also enter a full GHCR tag or digest reference. In `mode=live`, the five
public datasets are checked by default. Select any of HotpotQA, MuSiQue,
2WikiMultiHopQA, NQ Rear, and PopQA. Each selected dataset runs one job with
Topic maintenance disabled. `seed_version` defaults to `latest`. Use a UTC
timestamp/release suffix to select a dated seed available for each selected
dataset.

Set `answer_workers` to an integer from 4 to 10. The default is 4. Both manual
workflows limit parallel dataset jobs to `floor(20 / answer_workers)`.
Ten answer workers allow up to two jobs and 20 answer requests at once.

```bash
gh workflow run run-evaluation.yml --repo markhuangai/dense-mem --ref main \
  -f image=test-777 -f run_musique=false -f run_2wikimultihopqa=false \
  -f run_nq_rear=false -f run_popqa=false
```

The application must be a Linux amd64 production image from this repository
with verified source metadata. The workflow pins the application and each
selected seed to a digest before model work. A missing or incompatible seed
leaves its selected dataset incomplete. Other verified datasets can finish.

Each job starts its pinned PostgreSQL image in fresh writable storage.
Dense-Mem applies pending migrations. Before and after scoring, the runner
verifies the locked inputs, authenticated scope, row counts, source mappings,
canonical and search fingerprints, and embedding contract. Topic maintenance
must be disabled and drained with zero organized sources and provider attempts.

The seed image build creates the schema, loads memory spaces before rows whose
checks resolve shared-space identity, then restores remaining data and indexes
with two workers. Each evaluation starts from the same seeded contents.

Canonical Evidence and provenance must remain unchanged. A newer compatible
application can reuse an older seed; incompatible schema, search or canonical
state requires a deliberate seed rebuild. Older logical dataset images also
require a rebuild. Reports record seed pull, PostgreSQL startup, and Dense-Mem
migration and readiness times separately.

The own-PR route remains `production-image-e2e.yml` dispatched on the current
own-PR branch with `pr_number` and `evaluation=live`. It requires the current
head's successful full production-image receipt and uses that image. Both live
routes accept the dataset checkboxes, `seed_version`, and `answer_workers`.
`evaluation=none` runs ordinary production E2E.

The `fixtures` mode prepares three synthetic documents through a deterministic
provider, including a document that succeeds after two HTTP 524 responses.
The fixture builds and boots one seed and scores four answers across two
questions and two policies. The provider is local to the GitHub-hosted job.

## Prove seed intake before a push

Build the Linux amd64 `production` target from the working tree using a build
context that contains only intended source files. Set `IMAGE_REVISION` to the
checkout commit and label the image `io.dense-mem.eval.source-sha256` with the
build context fingerprint. Build `./cmd/eval-runner` from the same source tree.

```bash
node --env-file=eval.env tests/eval/scripts/local-intake-proof.mjs \
  --image <local-image-id> --runner <eval-runner-binary>
```

The proof freezes 20 public rows per pack, including the 28 previously rejected
rows, and verifies their source locks. Both runs use fresh databases from the
same pinned bootstrap and use four workers. The comparison records all 100
ordinary outcomes. Passing requires 100 seed-mode imports and 100 verified
after fresh restores, including exact text, partition receipts, current search
state, and matching fingerprints. Private proof receipts and exports live
under `tmp/eval-seeds/`.

## Sources, scoring and measurements

[datasets.json](datasets.json) pins the complete HippoRAG_2 packs at revision
`5ec05b38deecc3318bb432c69865959c56058990`: 45,895 corpus rows and 5,000 questions.
The [source lock](source_locks/hipporag_2.json) retains hashes, joins and upstream
attribution/licensing limitations. QASPER and custom question subsets are
excluded. Downloaded sources, databases and runtime files stay in private
temporary directories on the build host or runner.

Every question gets one initial Recall, shared between
independent single-call and multi-turn conversations. Multi-turn permits Recall,
returned-topic follow-ups and authorized Trace, with eight total memory calls
including the initial Recall. Gold references stay outside provider inputs.
Answer regeneration is bounded to three complete attempts per turn. During seed
preparation, a confirmed HTTP 502, 503, 504 or 524, temporary DNS failure,
connection reset, interrupted response or request-level network timeout puts
the row at the back of the queue. It can run up to six times total, including
the initial attempt, with the same authenticated scope, content and idempotency key.
The existing deadline still applies. Other seed import failures retain their
existing one-retry limit when eligible. Attempt logs preserve the status and
outcome, and exhausted retries leave the run incomplete.

Deterministic scoring keeps answer/alias EM/F1, exact citation validity,
retrieval R@1/5/10, all-support recovery and delivered supporting text coverage
separate. Original sources receive credit once; partitions and undelivered
equivalent IDs cannot inflate retrieval. Expect 2,000 answers per dataset and
10,000 for all five. Failed and missing answers count toward the selected
denominator. Scoring uses the locked references and rubric.

Generation is pinned to `dense-mem-testing` with the maintainer-specified
500000-token context. Embeddings use OpenAI `text-embedding-3-small`, 1536
dimensions, and `https://api.openai.com/v1`. Use the existing dedicated Secrets
`DENSE_MEM_EVAL_API_KEY`, `DENSE_MEM_EVAL_EMBED_API_KEY`, and
`DENSE_MEM_EVAL_VERIFIER_URL`. Ordinary settings resolve workflow value, repository
Variable, repository Secret, then explicit error; invalid supplied values fail.
Embedding concurrency is two. The report records the effective answer worker
count, and the local import builder records its requested import workers.

Answer turns can retry two confirmed transport failures across response
regeneration. Connection interruptions and HTTP 429, 502, 503, 504 and 524 use
one- and two-second backoffs. On 429, new answer dispatches for that dataset wait
through the longer of the backoff and a valid `Retry-After`. The model deadline
bounds every wait. Each attempt and any unavailable usage remain visible.

Use `gh run view <run> --job <job> --log` to collect the credential-protected
DEBUG console records for a dataset job. Reassemble chunks by run and sequence
and check the SHA-256. Match requests, responses and explicit no-response
records by exchange ID, and join attempt usage and retries by operation and
attempt. Both policy observations reference the same `initial_recall` record
by question. Initial security rejection stays content-free. For failures,
`eval_seed_pin_failed` gives the input and reason. Read the first typed capture
failure in `eval_capture_unavailable` or `eval_capture_failed`, including the
phase, record kind, sequence when assigned, and budget or sink error. The same
cause is retained in the numeric report when the console sink fails.

Reports include seed identity, pull, PostgreSQL startup, migration, readiness,
scoring through log flush, queue wait and HTTP timing. Current-run usage excludes historical seed
preparation. Host latency and throughput retain their existing definitions;
unavailable billing remains unavailable rather than an inferred monetary cost.

Model work stops after 330 minutes per dataset. Hosted jobs end after
360 minutes. The runner stops the owned server before collecting terminal logs.
The preservation window allows up to two minutes for shutdown and up to twenty
minutes for capture. A verified pre-cutoff result can finish capture during
that window. Interrupted model work or incomplete capture fails the dataset.
Scoring jobs use `fail-fast: false`. Read `evaluation-<dataset>-off` for
independent scores and cleanup receipts, and `evaluation-comparison` for the
selected totals. A result is complete when every selected dataset finishes
both policies and passes restore, capture and cleanup checks. Numeric
artifacts are available for two days.

Replace [baseline.json](baseline.json) only after a newer verified complete
campaign with `full_public_campaign=true`. Preserve historical evidence. Maintenance-only retirement remains
documented in [evaluation-retirement.md](../../architecture/evaluation-retirement.md).

## Delivery and acceptance

Use the [repository validation policy](../../AGENTS.md#release-and-evaluation-rules)
for changes to scoring or retrieval. The hosted synthetic fixture verifies
seed export and restore, both answer policies, cutoff, protected failure
capture and owned cleanup. The registered PostgreSQL regressions cover seed
identity, native state, migrations and sustained Remember intake. Record the
tested source and image, seed pins and run receipts with any measured result.
