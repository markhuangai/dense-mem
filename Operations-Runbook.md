# Operations Runbook

This runbook covers current installations. Establish the deployed image,
effective configuration, and database state before applying a procedure.
A source checkout does not establish the deployed revision.

## Dependency Matrix

| Dependency | Role |
| --- | --- |
| PostgreSQL with pgvector | Required durable authority and search execution. |
| Embedding and verifier providers | Required configured clients; failures are surfaced by affected operations. |
| Redis | Distributed coordination when required; otherwise one process can coordinate locally. |
| Prometheus | Optional operational metric collection/querying. |

The production image has one `/app/server` executable. The private control
portal/API owns administration; the main listener owns MCP and the user portal.

## First Deployment

Follow [Quick Start](Quick-Start) with a pinned image and the matching Compose
example. Configure the required database/control/provider credentials and
model dimensions before startup.

Keep the control listener and database private. Use the expert Compose
configuration for public HTTPS or distributed coordination. Set runtime public
origins and cookie policy through the control portal.

## Startup And Readiness

The server validates a writable single-primary PostgreSQL topology and applies
ordered migrations under a database session lock before serving. Replicas that
share that primary serialize the migration phase.

The default migration deadline is 1800 seconds. If increasing it, extend the
deployment healthcheck start period accordingly. A migration still running
within that deadline is not a reason to launch a second manual migration.

```bash
docker compose ps
docker compose logs --tail=100 server
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8080/ready
```

Console logs may contain admitted user text. Inspect them within the operator
boundary; do not paste the entire output into an external report.

Readiness covers required configured dependencies and active search state.
The PostgreSQL operation-log sink is required and exposes sink failure/recovery.
Unsupported replica/distributed topology, incompatible model/dimensions,
missing pgvector, and invalid required configuration must be corrected before
traffic.

## Health Triage

1. Record the image/revision, UTC window, correlation ID, and bounded error.
2. Check readiness, startup logs, database connectivity/locks, and effective
   configuration.
3. Open the corresponding control diagnostic view and inspect its actual
   source/capture availability.
4. Follow typed remediation; do not substitute a successful empty response or
   modify canonical rows to suppress an error.

See [Telemetry](Telemetry) for the logging, capture, and metric boundaries.

## Synchronous Remember Failure Triage

The originating call returns a completed result or a typed failure. For a
completed result, distinguish stored evidence from accepted Relationship
splits. Evidence-only storage does not guarantee default recall visibility.

Use the control Remember view's admitted invocations. An invocation can be an
execution, replay, or idempotency conflict; several calls can refer to one
canonical attempt. Inspect phase, retry eligibility, request hash, assessor
validation details, provider exchange availability, and delivery observations.

| Failure | Response |
| --- | --- |
| Invalid input or security rejection | Follow bounded client guidance; initial rejected content is not captured. |
| Idempotency conflict | Compare the saved operation; do not rotate the key to bypass it. |
| Stale source/version/support | Refresh authorized state before a deliberately revised request. |
| Provider or embedding error | Check the selected endpoint/model, timeout, dimensions, and returned retry action. |
| Commit/database failure | Check database health and fence conditions; preserve exact retry semantics. |

Provider work and embeddings finish before accepted canonical commit.
Duplicate-detection embeddings can precede assessment. A capture marked
unavailable, truncated, or expired is incomplete evidence, not an empty
successful exchange.

## Search Convergence

Use the control Search convergence view:

```text
GET /control/api/search/convergence
```

Compare expected/current/drifted documents, failed sources, active embedding
contract and index generation, and reconciliation run status. Search documents
are derived; canonical evidence and lifecycle remain authoritative.

Reconciliation selects current documents, obtains required embeddings, and
applies only source/version/hash-compatible results. Fix provider or database
faults and use the supported maintenance workflow. Do not repeat Remember to
repair a derived index or manually reset document versions.

## Recall Latency Or Quality

Check source/support/time eligibility before diagnosing a missing result as
data loss. Inspect `search_states` and `degradations`, then the active
generation and authorized canonical trace.

Compare lexical and vector candidate behavior with the same query, team, space,
and time view. ANN reranking cannot recover an omitted candidate. Ranking/index
changes require the evaluation evidence in [Evaluation](Evaluation).

## Dream And Community Jobs

Inspect effective enablement, timezone/window, last durable run, lease, input
counts, provider activity, and final disposition. Graph Dream runs use a daily
local window; evidence discovery uses UTC hourly windows.

A zero-output run can be valid. Dream diagnostic phases explain no input,
previous evaluation, provider zero proposals, rejected proposals, and
stale/fenced work. See [Dreaming](Dreaming#run-diagnostics).

Community publication requires a valid complete snapshot and source
fingerprint. Stale or failed snapshots degrade recall explicitly; operators
must not treat a summary as evidence.

## Conflict Review Job

The [Conflict Queue](Conflict-Queue) is a read-only projection of open/overdue
Relationship cases and lease/assessment state. Evidence-conflict cases have
their own control view and version-fenced resolution workflow.

Check the durable case, next review time, supporter projection, lease expiry,
and bounded assessment failure class. Let normal recovery reclaim expired
work. Do not change case status, votes, source evidence, or lease fences
through ad hoc SQL.

## Private-Memory Erasure

The control portal private-memory API is the supported operational surface. It
lists private spaces, active legal holds, erasure operations, retention runs,
and `PRIVATE_MEMORY_RETENTION_DAYS`; there is no dedicated browser Privacy
workspace yet. A zero retention value disables the scheduled scan; owner and
control requests still work.

Before an erasure:

1. confirm the exact team, space kind, owner ID, credential binding, and active
   legal-hold state;
2. use a new bounded `Idempotency-Key` and the exact body
   `{"acknowledge_irreversible":true}`;
3. treat `202 Accepted` as a queued durable operation, not completed deletion;
4. poll the operation until `completed` or `failed`, preserving its ID and
   correlation window in the change record.

Acceptance seals the private space and advances its generation in the same
transaction as the operation. For an accepted SSO-owned credential deletion,
the credential is disabled in that transaction. A credential-private binding
queues erasure and permanent retirement of only that credential space;
profile-private and team-shared content remain. Profile-private owner/control
erasure reopens an empty space after completion.

The control Team-credentials delete route is a separate manager operation. It
disables the selected credential and queues the same credential-private
retirement when that binding exists, without an owner acknowledgement body;
profile-private and team-shared spaces remain preserved.

The SSO owner path appends its safe `REVOKE` audit row before the credential
disable and erasure queue become visible; the control/manager path does the
same with a `DELETE` audit row. An audit insert failure rolls back the
disablement and queueing. Session invalidation happens after commit and is
best-effort coordination cleanup: a failure is logged and does not undo the
durable credential state. If a credential deletion arrives while an erase is
already queued or processing, its second idempotency mapping upgrades that
operation to retirement while preserving the sealed target generation and
worker fence. A different request hash still returns an idempotency conflict.

Remember and correction recheck the canonical space generation in their
accepted transaction.

The erasure worker rechecks the operation, sealed generation, legal hold, and credential state, then deletes
the catalog-complete manifest in one transaction, verifies zero remaining rows,
and redacts private audit payloads while retaining the audit tombstone. If a
team hard-delete later removes a memory-space catalog row, the append-only audit
row remains and its `memory_space_id` becomes `NULL`. A worker
failure is requeued after 1, 2, 4, and 8 seconds; the fifth failed attempt or
an exhausted expired lease becomes terminal `failed`. Do not manually update
`status`, `fence`, `manifest_position`, space lifecycle, or deleted counts.

For a stuck operation:

| Observation | Response |
| --- | --- |
| live `processing` lease | wait; do not start a second worker |
| expired lease | let the normal worker reclaim it with a new fence |
| terminal `failed` | inspect `attempt_count` and bounded `last_error_code`, correct the underlying fault, and submit a new reviewed intent with a new idempotency key if erasure is still authorized; the new operation preserves the failed operation's sealed generation, action, target credential, and retirement outcome |
| `last_error_code=legal_hold` | verify the hold; release it only under the applicable legal/change authority, then let the worker retry |
| `last_error_code=manifest_mismatch` or boot reports a manifest mismatch | stop private erasure rollout; identify the unaccounted `space_id` table/FK and ship a reviewed manifest/migration fix |
| repeated database/timeout error | inspect PostgreSQL health, locks, and the bounded operation metadata; do not delete rows manually |

Automatic retention selects only active private spaces whose most recent
private content is older than the cutoff, which have no active hold, and which
have no queued or processing erasure. The retention-run receipt records the
fixed cutoff and number queued. Changing the configured days later does not
rewrite an existing run.

A terminal failure does not unseal the space or rewrite the failed operation.
An authorized owner or control request with a fresh idempotency key creates a
new queued operation against the same sealed target contract. The space reopens
or retires only after that recovery operation completes and verifies the full
manifest.

There is no cancellation or restore endpoint after an erasure is accepted.
After completion, recovery means an authorized backup/PITR restore into an
isolated environment and a separate reviewed re-ingestion decision; never copy
rows back into the live space or reset its generation. Restoring content does
not re-enable a disabled or retired credential.


## Backup

Use PostgreSQL base backups and WAL/PITR with approved encryption/secret
material. The recovery catalog should record database/pgvector versions,
application/schema revision, search contracts/generations, WAL coverage,
checksums, and retention policy.

Memory packs are selected exports, not full backups. Redis is not the canonical
backup source. Verify restores regularly in an isolated environment.

## Restore And Disaster Recovery

1. Restore the selected backup and WAL into an isolated environment.
2. Restore required protected configuration and verify schema, pgvector,
   ownership, RLS, and active search contracts.
3. Verify expected canonical counts, representative traces, private-space
   isolation, and current/temporal recall.
4. Reconcile derived search state through the supported workflow and verify
   worker lease recovery.
5. Complete an authorized deployment/traffic change only after the checks pass.

Private content erased from the live database must not be copied back by
resetting a space generation. Any recovery/re-ingestion requires its own
authorized decision.

## Routine Maintenance

Monitor database locks, slow queries, connection budget, vacuum/analyze,
disk/WAL, backups, provider error/latency, search convergence, conflict due age,
Dream/community runs, diagnostic retention, and Redis coordination health.

Adding replicas multiplies connection/provider pressure. Model or dimension
changes are not ordinary hot configuration edits; persisted vectors must
remain compatible with the active contract.

## Graceful Shutdown

Stop admitting requests, cancel owned workers/streams, and allow bounded
shutdown before closing dependencies. A worker that loses its lease cannot
commit. Shutdown never converts unfinished work to success.

## Support Bundle

Include the UTC window, image/source revision, correlation and authorized
operation IDs, safe error codes, readiness, effective non-secret settings,
search/run summaries, and bounded database/provider metadata.

External support bundles exclude raw credentials, headers, cookies, DSNs,
evidence, provider prompts/responses, vectors, and database dumps. Authorized
local diagnostic visibility does not authorize exporting its contents.

## Risks

A capture does not prove response delivery; a counter does not prove current
lifecycle; stored evidence does not guarantee recall visibility; and a sealed
space is not proof that physical erasure completed. Use the corresponding
authoritative result or durable operation state for each decision.
