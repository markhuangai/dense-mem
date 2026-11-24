---
title: "Operations Runbook"
permalink: "/Operations-Runbook/"
legacy_source: "Operations-Runbook.md"
workflows: ["diagnose","storage","privacy"]
---

# Operations Runbook

Establish the deployed image, effective configuration, database state, and operation IDs before acting. Source documentation does not establish which binary is deployed.

## Startup And Readiness

```mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart TB
  Config["Validate configuration"] --> Primary["Connect writable PostgreSQL primary"]
  Primary --> Lock["Acquire migration session lock"]
  Lock --> Migrate["Apply pending ordered migrations"]
  Migrate --> Search["Verify embedding and index contracts"]
  Search --> Compose["Attach required sinks, listeners and workers"]
  Compose --> Ready["Ready for traffic"]
```

| Dependency | Role |
| --- | --- |
| PostgreSQL + pgvector | Required durable authority and search execution. |
| Assessment and embedding providers | Required configured clients; affected operations surface failures. |
| Redis | Required shared coordination for multiple instances; single-process local mode is supported. |
| Operation-log sink | Required PostgreSQL diagnostic sink participates in readiness. |
| Prometheus/OTLP | Optional telemetry according to configuration. |

```bash
docker compose ps
docker compose logs --tail=100 server
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8080/ready
```

Migrations have no overall deadline by default (`POSTGRES_MIGRATION_TIMEOUT_SECONDS=0`); shutdown still cancels. Structured phase/count observations and liveness heartbeats are described in [Telemetry]({{ '/Telemetry/#migration-observations' | relative_url }}). Extend startup/healthcheck grace and rollout deadlines for a long migration instead of launching a competing migration process. See [Configuration]({{ '/Configuration/#postgresql-and-pgvector' | relative_url }}).

### Destructive Migration Boundary

Before the pending Evidence-only cutover, stop incompatible readers/writers and verify a matching restore-tested backup. Migration `20261110010000` removes retired graph data and resets derived organization in one transaction with inventory, hold, dependency, and retained-Evidence checks. A blocked check stops the upgrade; inspect the bounded failure before proceeding.

Before destructive commit, transaction rollback preserves the prior state. After commit, restoration requires the matching backup; after new accepted writes, use forward recovery unless separately authorized. Ordinary rolling changes must remain compatible with the previous supported binary.

Private attribution migration also runs automatically before serving. Its read-only diagnostic command is:

```bash
/app/server private-memory-preflight
```

When an operator deliberately invokes the existing migration command with a retained inspected fingerprint:

```bash
/app/server private-memory-migrate \
  --source-fingerprint='<preflight-source-fingerprint>'
```

The standalone command writes its final status to stdout and uses a WARN-level console logger on stderr. Do not guess ownership to clear a preflight blocker. Partial migration remains a readiness failure. The deprecated maintenance-confirmation flag is not an activation requirement.

### Evidence-Only Cutover

~~~mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart TB
  Backup["Stop incompatible readers/writers<br/>Verify restore-tested backup"] --> Pending{"Migration 20261110010000<br/>pending?"}
  Pending -->|"Yes"| Migrate["Apply atomic cutover<br/>Reset derived organization"]
  Pending -->|"No"| Skip["Skip reset<br/>Keep current organization"]
  Migrate --> Receipt{"Committed receipt<br/>verified?"}
  Skip --> Receipt
  Receipt -->|"No"| Stop["Stop and inspect<br/>transaction outcome"]
  Receipt -->|"Yes"| Start["Start compatible application"]
  Start --> Enabled{"Maintenance enabled<br/>and unpaused?"}
  Enabled -->|"No"| Wait["Wait for enable / resume"]
  Enabled -->|"Yes"| Rebuild["Rediscover eligible shared Evidence<br/>Rebuild within model capacity"]
  Wait -. "Enable / resume" .-> Rebuild
~~~

[Topics And Organization]({{ '/Topics-And-Organization/#evidence-only-cutover' | relative_url }}) defines the retained and reset state. The rebuild uses existing eligible Evidence without resubmission. It runs after startup under the saved maintenance settings and can produce different topics, IDs, and assignments. Migration completion does not establish rebuild completion.

Inspect the transaction outcome and retained receipt using [migration observations]({{ '/Telemetry/#migration-observations' | relative_url }}). After a committed destructive cutover, recovery of the previous organization requires the matching backup and the restore procedure below.

## Staging Deployment

~~~mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart TB
  Select["Authorize image tag<br/>Resolve exact source revision"] --> Pin["Verify production variant<br/>Pin immutable image digest"]
  Pin --> Restore["Stop staging application<br/>Restore production copy"]
  Restore --> Migrate["Run migrations using<br/>the pinned application image"]
  Migrate -->|"Success"| Verify{"Read-only state, catalog,<br/>and Evidence checks pass?"}
  Verify -->|"Yes"| Start["Start the same pinned image<br/>Wait for health and readiness"]
  Verify -->|"No"| Stop["Leave application stopped<br/>Inspect failure and receipt"]
  Migrate -->|"Failure"| Stop
~~~

Install `deploy-staging-copy.sh` and its `sync.sh` dependency beside the staging Compose configuration before dispatching **Deploy staging**. The workflow runs through the protected `staging-migration` environment and passes the selected image revision and migration inventory to that host helper.

Validation checks migration state, catalog compatibility, and Evidence preservation. Existing completed private attribution uses exact retained fingerprints; pending legacy attribution retains its supported identity remapping. A failed migration or deployment does not restart an older fallback image.

Prerelease publication performs its separate source-based production-copy rehearsal before publishing images. It does not start the staging application. The [deployment workflow](https://github.com/markhuangai/dense-mem/blob/41e278673679c1d9634cd63a68d13c74e8a46452/.github/workflows/deploy-stage.yml) owns invocation and image selection; [Development And Testing]({{ '/Development-And-Testing/#staging-validation' | relative_url }}) describes the verification boundaries.

Use the workflow guide to rebuild a production or demo image from an existing prerelease tag. [Publish a prerelease image](https://github.com/markhuangai/dense-mem/blob/main/.github/workflows/README.md#publish-a-prerelease-image)

## Health Triage

1. Record the image/source revision, UTC window, correlation ID, and bounded public error.
2. Check readiness, database connectivity/locks, effective configuration, and required sink health.
3. Open the matching control diagnostic view and inspect actual capture availability.
4. Follow typed remediation; preserve source data and operation identity.

Console logs can contain admitted user text. Inspect within the operator boundary; collect only the authorized protected evidence needed for diagnosis.

## Synchronous Remember Failure Triage

```mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart TB
  Call["Remember call"] --> Receipt{"Terminal receipt received?"}
  Receipt -->|"Yes"| Inspect["Inspect processing state,<br/>Evidence outcomes, errors and warnings"]
  Receipt -->|"No"| Unknown["Unknown delivery outcome<br/>Retain complete original request/key"]
  Inspect --> Error{"Failure class"}
  Error -->|"Input / policy"| Correct["Follow bounded correction guidance"]
  Error -->|"Provider / database"| Diagnose["Join invocation and canonical attempt"]
  Unknown --> Recover["Unchanged-request recovery when permitted"]
  Diagnose --> Recover
```

The control Remember view links admitted invocations to canonical attempts. Execution, replay, and key-conflict calls are different invocations and can refer to one attempt. Inspect phase, request hash, validation details, retry eligibility, and provider-exchange availability.

| Failure | Operator action |
| --- | --- |
| Initial security rejection | Use content-free rule/span metadata; no rejected body was captured. |
| Changed request under the same key | Compare saved request identity; do not bypass conflict by rotating keys. |
| Stale source or generation | Refresh authorized current state before an intentional revised operation. |
| Provider/embedding failure | Check actual endpoint/model, remaining deadline, dimensions, and returned retry action. |
| Commit failure | Check transaction/fence errors and database health. |
| Retired contract | Inspect retained outcome; no replay through the removed graph contract. |

Staged exact intake can remain after required failure without new accepted semantic memory. A response-write observation is not proof of client delivery. See [Memory Write Pipeline]({{ '/Write-Pipeline/' | relative_url }}) and [Telemetry]({{ '/Telemetry/#operation-logs-and-protected-captures' | relative_url }}).

## Search Convergence

Inspect `GET /control/api/search/convergence` or the control Search convergence view. Compare expected, current, drifted, pending, and failed documents with active source/embedding/index generations.

Required foreground embeddings finish before Remember success. Later source or contract changes can need bounded reconciliation. Investigate the allowlisted failure and latest reconciliation outcome; do not repair search by rewriting canonical Evidence or relabeling incompatible vectors.

## Recall Latency Or Quality

| Observation | Investigate |
| --- | --- |
| Empty/degraded results | Public search states and degradations, then authorized source/lifecycle visibility. |
| Missing topic follow-up | Organization enablement, current memberships, dependencies, and unseen eligible counts. |
| Slow provider phase | Model, request/context size, concurrency, and provider diagnostics. |
| Slow database phase | Scoped full-text/vector plans, active index definition, connection pressure, and slow-query logs. |
| Quality changed | Same-identity deterministic comparison; see [Evaluation]({{ '/Evaluation/' | relative_url }}). |

Use trace for individual Evidence state. Metrics cannot establish the lifecycle or truth of a source.

<span id="ontology-maintenance"></span>

## Topic Maintenance

```mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart LR
  Status["Inspect status"] --> Configure["Configure maintenance"]
  Configure --> Enable["Enable"]
  Enable --> Progress["Observe progress"]
  Progress --> Pause["Pause admission"]
  Pause --> Resume["Resume"]
```

| Private control route | Purpose |
| --- | --- |
| `GET/PATCH /control/api/config/topic-maintenance` | Read/update supported settings. |
| `GET /control/api/topic-maintenance/status` | Current policy, pause state, usage, and progress. |
| `GET /control/api/topic-maintenance/runs` | Bounded run history. |
| `POST /control/api/topic-maintenance/runs` | Explicit bounded run with retained `operation_key` and optional `max_batches`. |
| `POST /control/api/topic-maintenance/runs/{runId}/retry` | Supported explicit retry of the selected run. |
| `POST /control/api/topic-maintenance/pause` or `/resume` | Idempotent admission control with retained operation key. |

Inspect Topic assignment and Community placement counts, failed or ambiguous outcomes, and unreconciled or unavailable usage. Pause stops background admission and lets dispatched work finish. Foreground assignment and Topic reads continue while enabled and paused. Disabling organization defers assignments and Topic reads. Ordinary Recall remains available.

See [Configuration]({{ '/Configuration/#topic-maintenance' | relative_url }}) and [Topics And Organization]({{ '/Topics-And-Organization/' | relative_url }}).

## Topic Storage Upgrade

1. Pause the old worker, drain leases, and verify provider usage reconciliation. Stop all old application writers.
2. Record the source image and PostgreSQL schema version. Rehearse the [custom-format restore]({{ '/Operations-Runbook/#pre-cutover-custom-format-restore' | relative_url }}) in isolation. Verify scoped counts, canonical fingerprints, holds, and historical receipts.
3. Start the compatible image with service traffic held. Follow migration phase, checkpoint, and fingerprint records in protected stdout. Startup opens listeners after the storage layout is verified.
4. Verify Recall, catalog history, and preserved operator settings before reopening traffic. If interrupted, resume with the same compatible image and inspect its checkpoint verification. Restore the verified backup only within the pre-write recovery boundary. After new writes, recover forward.

## Evidence Conflicts

Use the control Evidence-conflict workspace. Inspect exact positions and provenance before a version-fenced resolve/dismiss command. The current procedure and routes are in [Conflicts And Corrections]({{ '/Conflicts-And-Corrections/#operator-inspection-and-decision' | relative_url }}).

## Private-Memory Erasure

Inspect the exact private target, operation state, active generation, and applicable legal holds. A queued acknowledgement is not physical completion. Let the privacy owner seal/fence and verify its catalog-complete purge. Do not unseal a target or reset generations to retry a stale writer.

Erasure includes raw intake, extraction checkpoints, retained memory, dependent projections, and applicable migration copies. Unaffected spaces remain separate. Owner-facing requests and polling are in [Portals And API Keys]({{ '/Portals-And-API-Keys/#private-memory-erasure' | relative_url }}).

## Backup

Use a PostgreSQL backup/PITR policy matching durability requirements. For a logical backup of the bundled stack:

```bash
docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > dense-mem.dump
```

Protect the dump. Record database/server versions, schema, pgvector, active search contracts, WAL coverage, checksums, and protected configuration references. Memory packs and Redis are not full backups. Test restores in isolation.

## Restore And Disaster Recovery

1. Restore the selected PostgreSQL backup and WAL into an isolated environment.
2. Restore compatible protected configuration; verify schema, pgvector, ownership, RLS, and search contracts.
3. Check canonical counts, representative traces, private isolation, and current/temporal recall.
4. Verify derived reconciliation and worker lease recovery.
5. Restore traffic only through the authorized deployment procedure.

Do not restore erased private content by resetting a space generation. After incompatible migrations or new writes, retain compatible readers/writers and recover forward as required.

### Pre-Cutover Custom-Format Restore

Use a new, isolated database with the source roles available. Run each `pg_restore` stage with `--exit-on-error --single-transaction`. Preserve archive ownership and grants.

1. Restore the schema with `--section=pre-data`.
2. Load `public.memory_spaces` with `--data-only --schema=public --table=memory_spaces`. Legacy inline scope checks consult this table while dependent rows are copied.
3. Create a remaining-entries list with `pg_restore --list` and comment out only the `TABLE DATA public memory_spaces` entry. Restore with `--section=data --section=post-data --use-list=<remaining-entries>`.

If a stage fails, stop the rehearsal and use a fresh isolated database for the retry. After all stages succeed, verify the pre-cutover catalog and preserved history with the restricted runtime role.

## Routine Maintenance And Shutdown

Monitor locks, slow queries, connection budget, vacuum/analyze, disk/WAL, backups, provider failures, search/organization convergence, diagnostic retention, and coordination health. More application replicas increase database/provider pressure.

Graceful shutdown stops admission and cancels request/worker contexts before closing dependencies. A forced disconnect can leave a client uncertain about delivery; retain operation identity. Keep support bundles bounded and protect operational credentials and admitted text.
