# Runner evaluation and public dataset seeds

All seed preparation, evaluation campaigns and benchmarks run on GitHub-hosted
runners. Missing runner capacity or Actions configuration blocks execution.
Selective synthetic unit and policy checks use `bash scripts/test-evaluation.sh`.
There is no local campaign, replay, partial-import checkpoint or resume interface.

## Build public dataset seeds

Use **Actions → Build evaluation seeds → Run workflow**. Its only inputs are:

| Input | Default | Meaning |
| --- | --- | --- |
| `dataset` | `all` | All five complete public packs, or one named pack to rebuild |
| `workers` | `20` | Concurrent Remember requests for the current dataset, from 1–100 |

```bash
gh workflow run build-evaluation-seeds.yml --repo markhuangai/dense-mem --ref main \
  -f dataset=all -f workers=20
```

Before the implementation reaches main, select PR #777's approved branch. Both
initiating and triggering actors must be repository admins. A PR dispatch must
use the actor's own current, open, non-draft same-repository PR head and its
successful trusted production-image E2E receipt. Main dispatches use its published
release image. Job reruns are rejected; a failed dataset requires a new dispatch.

The workflow creates or verifies a shared empty bootstrap database, then builds
one dataset at a time. The first failure stops remaining datasets. Completed
images remain available. Rebuild a failed dataset explicitly, for example:

```bash
gh workflow run build-evaluation-seeds.yml --repo markhuangai/dense-mem --ref main \
  -f dataset=hotpotqa -f workers=30
```

Each dataset restores the bootstrap, imports its complete corpus with ontology
disabled and paused, verifies the source/partition mapping and search state, and
exports a compressed PostgreSQL logical dump. A fresh restore must verify before
publication. The backup preserves migrations, Evidence, embeddings, provenance,
ownership and required receipts. Operation-log and Remember diagnostic payload
rows are excluded, while their table definitions remain.

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

Provision two additional Actions Secrets before preparation:

- `DENSE_MEM_EVAL_TEAM_API_KEY`: one fixed, sufficiently random evaluation key.
- `DENSE_MEM_EVAL_DATABASE_PASSWORD`: one fixed PostgreSQL password.

The bootstrap's team, credential, owner and space IDs remain identical across
isolated dataset databases. The credential hash is preserved; plaintext keys and
database login passwords do not enter the images. A mismatched team key fails
without replacing the credential. Control tokens are generated at startup.
The maintainer configures the package's public visibility. Synthetic test images
are never published as dataset seeds.

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

Canonical Evidence and provenance must remain unchanged. A newer compatible
application can reuse an older seed; incompatible schema, search or canonical
state requires a deliberate seed rebuild. Logical restores rebuild indexes;
reports record restore and migration/readiness timings rather than assuming a
speed advantage over physical snapshots.

The own-PR route remains `production-image-e2e.yml` dispatched on the current
own-PR branch with `pr_number` and `evaluation=live`. It requires the current
head's successful full production-image receipt. `evaluation=none` runs ordinary
production E2E. Both routes accept `seed_version`.

`mode=fixtures` is the existing evaluation system dry run. Internal automated
fixtures prepare three synthetic documents with a deterministic provider,
export and restore them, then produce eight scored answers across two ontology
conditions and two answer policies. It makes no external model calls and
publishes no seed image. The public seed-building workflow has no mode input.

## Sources, scoring and measurements

[datasets.json](datasets.json) pins the complete HippoRAG_2 packs at revision
`5ec05b38deecc3318bb432c69865959c56058990`: 45,895 corpus rows and 5,000 questions.
The [source lock](source_locks/hipporag_2.json) retains hashes, joins and upstream
attribution/licensing limitations. QASPER and custom question subsets are
excluded. Downloaded sources, databases and runtime files stay in runner-private
temporary directories.

Every question gets one initial Recall per condition, shared unchanged between
independent single-call and multi-turn conversations. Multi-turn permits Recall,
returned-topic follow-ups and authorized Trace, with eight total memory calls
including the initial Recall. Gold references stay outside provider inputs.
Answer regeneration is bounded to three complete attempts per turn. Each intake
has at most two supported attempts; retries retain identities and outcomes.

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

Preparation emits initial, sixty-second and final document counts. Benchmark
exchanges remain readable, credential-protected JSON in Actions logs. Initial
security rejection is content-free. Required failures and unavailable usage remain
explicit. Reports show model names without provider URLs or credentials.

Reports separate seed identity, restored rows, restore, migration/readiness,
scoring and ontology timings. Current-run usage excludes historical seed
preparation. Host latency and throughput retain their existing definitions;
unavailable billing remains unavailable rather than an inferred monetary cost.

Model work stops after 330 minutes; dataset jobs end after 360 minutes. Seed jobs
run serially and stop on failure. Scoring jobs use `fail-fast: false` and aggregate
every failed, timed-out or missing axis. There is no automatic rebuild, campaign
restart or deadline extension. Numeric artifacts retain two days; published seed
versions remain in the registry.

Replace [baseline.json](baseline.json) only after a newer complete overall
measurement. Preserve historical evidence. Maintenance-only retirement remains
documented in [evaluation-retirement.md](../../architecture/evaluation-retirement.md).

## Delivery and acceptance

The workflow-registration prerequisite must be human-merged before dispatching
the implementation from its PR branch. Require independent plan-audit
conformance before tests, then selective local formatting and synthetic checks.
Run Development E2E on the exact pushed SHA with PostgreSQL shards `all` and
`synchronous_write`, `mcp_boundaries`, `google_adk_memory`, `enterprise_exports`,
`ontology_organization`, `space_aware_recall`, and `operation_logging`. Its
observation budget is ninety minutes, including owned-image cleanup.

Require current-head review completion followed by passing full Repository CI,
CodeQL analysis, CodeQL and Production image E2E. Run the internal fixture
round-trip, including actual exporter, credential-exclusion and protected-failure
regressions, remotely before publishing any public dataset seed.

After the maintainer provisions the shared Secrets and public package visibility,
dispatch one full public seed build with twenty workers and serial datasets.
Observe it for up to thirty-one hours. After all five seeds exist, run one full
live evaluation before human merge, with a seven-hour observation budget. A
failed seed build or campaign requires investigation and a new explicit dispatch;
paid work never restarts automatically. Keep the existing measured baseline
until a complete replacement campaign succeeds.
