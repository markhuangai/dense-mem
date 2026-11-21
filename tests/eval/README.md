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

Each dataset restores the bootstrap, imports its complete corpus with ontology
disabled and paused, verifies the source/partition mapping and search state, and
exports a private PostgreSQL archive. The image build restores it once into a
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
build updates only its dataset's latest alias. Dataset images contain a
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

## Prepare ontology-on images

Use the existing production preview and the published ontology-off seeds.

```bash
node --env-file=eval.env tests/eval/scripts/local-seeds.mjs --variant ontology-on --pr 794
```

The builder pins the selected off digests, restores fresh databases, and
prepares ontology serially with eight ontology and verifier workers and a
500,000-token context. The 24-hour model deadline and cooldown survive a
checkpoint. A confirmed HTTP 429 suspends new ontology attempts for at least
60 seconds from the latest 429 or until the provider's later Retry-After.
Admitted work reconciles. Resume with `--resume <build-id>` after a paused,
drained checkpoint. Full discovery and positive, complete coverage are required
for publication. Unresolved ambiguity or failed accounting leaves the checkpoint
private.

Private preparation can start once the trusted current-head preview and CodeQL
receipts exist, while Repository CI or Production image E2E runs. Promotion
requires the exact-head checks and the hosted synthetic pair. The builder
fresh-restores the credential-checked export before publishing the dated
`<dataset>-ontology-on-<version>` image and then
`<dataset>-ontology-on-latest`. The child records the off parent digest,
canonical, search and ontology fingerprints, and durable window and usage
receipts. Build and scoring each record their own runtime image.

## Run an evaluation

Use **Actions → Run evaluation → Run workflow**, select `main`, and enter a
Dense-Mem production image tag such as `test-777`, `v2.8.1-rc.6`, or `v2.8.1`.
You can also enter a full GHCR tag or digest reference. In `mode=live`, the five
public datasets are checked by default. Uncheck any you want to omit and leave
at least one checked. `seed_version` selects the off seed and
`ontology_seed_version` selects the on seed. Both default to `latest`.
Use a UTC timestamp/release suffix to select a dated version. The on seed
must cite the selected off digest as its parent.

Set `answer_workers` to an integer from 4 to 10. The default is 4. Both manual
workflows limit parallel dataset and condition jobs to
`floor(20 / answer_workers)`, so `answer_workers=10` runs at most two
jobs and 20 answer requests at once
per campaign.

```bash
gh workflow run run-evaluation.yml --repo markhuangai/dense-mem --ref main \
  -f image=test-777 -f run_musique=false -f run_2wikimultihopqa=false \
  -f run_nq_rear=false -f run_popqa=false
```

The application must be a Linux amd64 production image from this repository with
verified source metadata. The workflow pins the application and each selected
seed to a digest before provider-backed jobs. Missing or incompatible conditions
remain incomplete while verified conditions finish. Use a complete report with `full_public_campaign=true`
to replace the overall baseline.

Each condition starts its pinned PostgreSQL image in fresh writable storage.
Dense-Mem applies pending migrations and verifies source locks, row counts,
authenticated scope, mappings, fingerprints, embedding and search contracts,
and the frozen condition. The on image must have complete paused coverage and
verifiable durable preparation windows, including after those windows expire.
Scoring verifies the state again and checks for new ontology attempts.

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
routes accept the dataset checkboxes, `seed_version`, and `ontology_seed_version`. `evaluation=none` runs
ordinary production E2E.

`mode=fixtures` is the evaluation system dry run. It prepares three synthetic
documents through a deterministic provider, including one document that gets
two HTTP 524 responses before succeeding. It derives and boots the off/on pair
and produces eight scored answers across two ontology conditions and two
answer policies. It makes no external model calls and publishes no seed image.

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

Every question gets one initial Recall per condition, shared unchanged between
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
equivalent IDs cannot inflate retrieval. Failed and missing answers retain the
complete 20,000-answer denominator. No model-based judge is used.

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

Once local ontology discovery and ordinary work drain, the builder can recover
scoped stale sources once and confirmed rate-limited failures within the
original deadline. Protected receipts identify the originating runs and outcomes.

Benchmark exchanges are emitted as credential-protected JSON at DEBUG. Use
`gh run view <run> --job <job> --log` to collect a dataset job. Match answer
attempts with their operation and exchange IDs and ontology recovery with its
originating run and assessment IDs. Reassemble chunks by run, sequence and
hash. The wire exchange owns the protected body and the attempt record carries
usage and retry decisions. Join each policy observation to the shared
`initial_recall` record using its question and condition. The numeric reports contain identities, counts and timings. Initial
security rejection stays content-free.

Reports include seed identity, pull, PostgreSQL startup, migration, readiness,
scoring through log flush, queue wait and HTTP timing. Current-run usage excludes historical seed
preparation. Host latency and throughput retain their existing definitions;
unavailable billing remains unavailable rather than an inferred monetary cost.

Model work stops after 330 minutes per dataset and condition. Hosted jobs end after
360 minutes. The runner stops the owned server before collecting terminal logs.
The preservation window allows up to two minutes for shutdown and up to twenty
minutes for capture. A verified pre-cutoff result can finish capture during
that window. Interrupted model work or incomplete capture fails the dataset.
Scoring jobs use `fail-fast: false` and aggregate every failed, timed-out or
missing axis. Numeric artifacts retain two days.

Replace [baseline.json](baseline.json) only after a newer complete overall
measurement. Preserve historical evidence. Maintenance-only retirement remains
documented in [evaluation-retirement.md](../../architecture/evaluation-retirement.md).

## Delivery and acceptance

Require independent plan-audit conformance before tests, then selective local
formatting and synthetic checks.
Run Development E2E on the exact pushed SHA with PostgreSQL shards `all` and
`synchronous_write`, `mcp_boundaries`, `google_adk_memory`, `enterprise_exports`,
`ontology_organization`, `space_aware_recall`, and `operation_logging`. Its
observation budget is ninety minutes, including owned-image cleanup.

Require current-head reviews and CodeQL before requesting the trusted image
and full validation. For ontology-on exports, use the private preparation and
public promotion checks above. Run the hosted synthetic pair to prove export,
protected failure capture, seeded startup, scoring, and owned cleanup. The registered PostgreSQL tests exercise sustained intake at
twenty workers, including credential activity, pool waits and commit timing.

Verify both seed digests for each of the five datasets before the full live
evaluation and run it before human merge. At ten answer workers and two
concurrent jobs, observe the full campaign for up to 32 hours. Replace the measured baseline after
all 20,000 answers and the required coverage and capture checks pass.
