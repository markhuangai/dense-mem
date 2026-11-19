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
exports a compressed PostgreSQL logical dump. A fresh restore must verify before
publication. The backup preserves migrations, Evidence, embeddings, provenance,
ownership and required receipts. Operation-log and Remember diagnostic payload
rows are excluded, while their table definitions remain.

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
average documents/minute. Resource records share the run identity and timestamp
with progress: interval-averaged host CPU and I/O wait, available memory, disk
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
build updates only its dataset's latest alias. The bootstrap uses the same
package with a `bootstrap-` prefix. These are data-only images containing a dump,
metadata, mappings and source notices; PostgreSQL runs in the ordinary
`pgvector/pgvector:0.8.2-pg18-trixie` image. No raw PGDATA directory is baked in.

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
key fails before ingestion. Each runtime generates its own control token.

## Run an evaluation

Use **Actions → Run evaluation → Run workflow**, select `main`, and enter a
Dense-Mem production image tag or digest. `mode=live` uses the complete public
benchmark. `seed_version=latest` resolves each dataset's current seed once; a UTC
timestamp/release suffix selects a complete historical build instead.

```bash
gh workflow run run-evaluation.yml --repo markhuangai/dense-mem --ref main \
  -f image=ghcr.io/markhuangai/dense-mem:YOUR_TAG -f seed_version=latest
```

The application must be a Linux amd64 production image from this repository with
verified source metadata. Its immutable digest and all five immutable seed
digests are resolved before provider-backed jobs. Missing or incompatible seeds
fail explicitly. There is no ingestion fallback or import-worker input.

Each dataset gets fresh PostgreSQL storage. Restore the seed first, then start
Dense-Mem so normal startup applies pending migrations. Check source locks,
counts, authenticated scope, partition mappings, canonical fingerprint, embedding
contract, search convergence and the ontology-off state before scoring:

```text
restore → migrate and verify → score with ontology off
  → enable ontology and converge → pause and drain → score with ontology on
```

Logical restore creates the schema, loads memory spaces before rows whose checks
resolve shared-space identity, then restores remaining data and indexes with two
workers. The archive retains every required table, index, constraint and policy.

Canonical Evidence and provenance must remain unchanged. A newer compatible
application can reuse an older seed; incompatible schema, search or canonical
state requires a deliberate seed rebuild. Logical restores rebuild indexes;
reports record restore and migration/readiness timings rather than assuming a
speed advantage over physical snapshots.

The own-PR route remains `production-image-e2e.yml` dispatched on the current
own-PR branch with `pr_number` and `evaluation=live`. It requires the current
head's successful full production-image receipt. `evaluation=none` runs ordinary
production E2E. Both routes accept `seed_version`.

`mode=fixtures` is the evaluation system dry run. It prepares three synthetic
documents through a deterministic provider, including one document that gets
two HTTP 524 responses before succeeding. It exports and restores the corpus,
then produces eight scored answers across two ontology conditions and two
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
There is no backing-model setting. Ontology and answer concurrency are four;
embedding concurrency is two. Seed preparation alone uses its requested workers.

Benchmark exchanges are emitted as credential-protected JSON in Actions logs. Look for
`runtime_transport_failure` for the underlying connection error and its request
and exchange IDs. Incomplete capture still fails validation. Initial security
rejection is content-free. Reports show model names without provider URLs or
credentials.

Reports separate seed identity, restored rows, restore, migration/readiness,
scoring and ontology timings. Current-run usage excludes historical seed
preparation. Host latency and throughput retain their existing definitions;
unavailable billing remains unavailable rather than an inferred monetary cost.

Model work stops after 330 minutes per dataset. Hosted dataset jobs end after
360 minutes. Scoring jobs use `fail-fast: false` and aggregate
every failed, timed-out or missing axis. There is no automatic rebuild, campaign
restart or deadline extension. Numeric artifacts retain two days; published seed
versions remain in the registry.

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
and full validation. Before a paid seed build, verify Repository CI, CodeQL
analysis, CodeQL and Production image E2E on the exact PR head. Run the internal
fixture campaign remotely to verify export, protected errors, restore, scoring
and owned cleanup. The registered PostgreSQL tests exercise sustained intake at
twenty workers, including credential activity, pool waits and commit timing.

Build and publish all five public seeds. Run a full live evaluation with the
published seed digests before human merge. Observe the live campaign for up to
seven hours. Keep the existing measured baseline until a complete replacement
campaign succeeds.
