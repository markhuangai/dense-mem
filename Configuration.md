# Configuration

> Publication pending: issue #218 branch. Publish after the code PR is merged and session ingestion passes against the exact merged-main commit.

Dense-Mem loads startup configuration from environment variables and validated
runtime policy from PostgreSQL. This page distinguishes application defaults
from values supplied by Compose examples.

## Sources And Precedence

Compose examples forward a selected set of variables. For a startup variable
not forwarded by your example, supply it in the service environment through a
deployment override.

Startup settings configure listeners, connections, credentials, provider
clients, and hard limits. Change them through the deployment environment and
restart the server.

The private control portal edits supported `app_config` sections. Effective
configuration is cached and refreshed against the database update marker.
Team overrides exist only for policies that explicitly support them, such as
Dream enablement and the memory-write confidence threshold. There is no
universal environment-overrides-everything rule for runtime policy.

## Required Startup Settings

A fresh server needs a writable PostgreSQL database, complete embedding
configuration, a verifier model/endpoint, and a control credential for the
administrative portal.

| Setting | Requirement |
| --- | --- |
| `POSTGRES_DSN` or component settings | A writable single-primary connection. |
| `AI_API_URL`, `AI_API_KEY` | Nonempty embedding endpoint and key. |
| `AI_API_EMBEDDING_MODEL`, `AI_API_EMBEDDING_DIMENSIONS` | Explicit model and positive response dimensions. |
| `AI_VERIFIER_MODEL` | Explicit chat model supported by the selected provider. |
| `CONTROL_PORTAL_TOKEN` | Required for control access in the bundled setup. |

The base Compose file supplies an OpenAI embedding URL, model, and dimensions.
Those example values are not implicit application defaults. Review the
[tracked environment example](https://github.com/markhuangai/dense-mem/blob/main/examples/.env.example)
alongside this page and the startup parser.

## Listeners

| Variable | Default | Meaning |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | Main MCP/user-portal listener. |
| `CONTROL_HTTP_ADDR` | `:8090` | Private control listener. |
| `DENSE_MEM_PORT` | `8080` in Compose | Published main port. |
| `CONTROL_PORTAL_BIND` | `127.0.0.1` in expert Compose | Published control address; base Compose fixes loopback. |
| `CONTROL_PORTAL_PORT` | `8090` in Compose | Published control port. |

The SDK provides MCP Streamable HTTP. Control and metrics should remain on a
private administrative boundary. See [Public HTTPS And Redis](Public-HTTPS-And-Redis).

## PostgreSQL And pgvector

| Variable | Application default | Meaning |
| --- | --- | --- |
| `POSTGRES_DSN` | Empty | Complete DSN; otherwise build from component settings. |
| `POSTGRES_HOST`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` | Required when constructing a DSN | Connection components. |
| `POSTGRES_PORT` | `5432` | Database port. |
| `POSTGRES_SSLMODE` | `disable` | Driver TLS mode; configure for the actual deployment. |
| `POSTGRES_MAX_OPEN_CONNS` | `25` | Per-process pool maximum. |
| `POSTGRES_MAX_IDLE_CONNS` | `10` | Idle pool target. |
| `POSTGRES_CONN_MAX_LIFETIME_SECONDS` | `1800` | Connection lifetime. |
| `POSTGRES_MIGRATION_TIMEOUT_SECONDS` | `1800` | Migration deadline, 1–86400 seconds. |
| `POSTGRES_SLOW_QUERY_THRESHOLD_MS` | `200` | Positive slow-query logging threshold. |
| `POSTGRES_HOST_PORT` | `15432` in evaluation Compose | Evaluation-only host database port; base Compose does not publish PostgreSQL. |

The application escapes connection components when constructing the DSN.
Never print the resolved credentialed URL into a support report.

Startup requires a writable primary. Replica/read-only connections and Citus
topology are unsupported; setting a separate `POSTGRES_READ_DSN` fails
validation. Tenant keys do not imply an enabled distributed deployment.

Migrations run under a database session lock before listeners start. The image
healthcheck allows the default 1800-second migration window; extend its start
period when configuring a larger migration timeout. Budget database connections
across all service instances and leave operator/failover reserve.

## Embedding Provider And Contract

| Variable | Application default | Meaning |
| --- | --- | --- |
| `AI_API_URL` | Required | OpenAI-compatible embedding base URL. |
| `AI_API_KEY` | Required | Provider credential. |
| `AI_API_EMBEDDING_MODEL` | Required | Model ID. |
| `AI_API_EMBEDDING_DIMENSIONS` | Required | Expected vector dimensions. |
| `AI_API_EMBEDDING_TIMEOUT_SECONDS` | `30` | Provider timeout, also constrained by the operation deadline. |
| `AI_API_EMBEDDING_MAX_CONCURRENCY` | `8` | Process-wide embedding request limit. |

The persisted embedding contract records provider, model, dimensions, distance,
normalization, and document/query formats. A model or dimension mismatch fails
startup; editing an environment value does not rebuild stored vectors.

Remember embeds required documents before canonical commit, with separate
bounded work possible for duplicate detection. See
[Memory Write Pipeline](Write-Pipeline#deadlines-and-atomic-commit).

## Search Index Construction

Search bootstrap derives and persists a compatible index generation from the
embedding contract. Current code defaults include cosine distance, HNSW
`m=16`, `ef_construction=64`, a candidate limit of 200, and an exact-query bound of 10,000 rows where the
exact path enforces it. HNSW `ef_search` starts at 40 and is raised to at least
the candidate limit. The active generation selects the exact or ANN branch.

These are server-owned search-generation values, not supported environment
knobs. The 3072-dimensional example model uses a `halfvec_hnsw` expression
index with full-precision reranking. See
[ANN, HNSW, And Halfvec](ANN-HNSW-And-Halfvec).

## Assessor

| Variable | Application default | Meaning |
| --- | --- | --- |
| `AI_VERIFIER_API_URL` | Embedding URL | Separate chat endpoint when set. |
| `AI_VERIFIER_API_KEY` | Embedding key only when URL is shared | A separate URL requires an explicit key. |
| `AI_VERIFIER_MODEL` | Required | Chat model. |
| `AI_VERIFIER_DISABLE_TEMPERATURE` | `false` | Set true to omit temperature; otherwise the request uses zero. |
| `AI_VERIFIER_TIMEOUT_SECONDS` | `60` | Per-provider timeout, bounded by the operation. |
| `AI_VERIFIER_MAX_CONCURRENCY` | `5` | Process-wide verifier request limit. |
| `AI_VERIFIER_MAX_INPUT_TOKENS` | `200000` | Complete assessor message-history budget. |
| `AI_VERIFIER_MAX_OUTPUT_TOKENS` | `65536` | Response acceptance budget; not a provider max-token argument. |
| `AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS` | `50000` | Candidate-context budget. |
| `AI_VERIFIER_MAX_PREDICATE_OPTIONS` | `100` | Exact plus relevant options, validated from 1–2000. |
| `AI_VERIFIER_TOKENIZER` | `o200k_base` | Token-accounting implementation. |

The environment example explicitly chooses 128000 input tokens and 16384 output
tokens for its example model. Choose limits that the configured model supports.

Remember has at most three complete assessor turns. Its total and phase
deadlines remain fixed even when a provider timeout is larger. Candidate,
grounding, and exact-match overflows are visible failures rather than silent
truncation. See [Memory Write Pipeline](Write-Pipeline#server-owned-assessment).

`MEMORY_AUTO_WRITE_CONFIDENCE_THRESHOLD` defaults to `0.7` and accepts
`0..1` inclusively. Applicable verifier-based lifecycle/correction paths use
it; the Remember structural assessor does not. A permitted team override is
`teams.config.memory_write.auto_write_confidence_threshold`.

Dreaming uses the configured verifier provider and model. There is no separate
Dream provider configuration.

## Redis, Rate Limits, And Streams

| Variable | Default | Meaning |
| --- | --- | --- |
| `REDIS_ADDR` | Empty | Empty selects process-local coordination. |
| `REDIS_PASSWORD` | Empty | Redis credential when needed. |
| `REDIS_DB` | `0` | Redis database. |
| `REDIS_TLS_ENABLED` | `false` | TLS for Redis. |
| `DISTRIBUTED_COORDINATION_REQUIRED` | `false` | Requires Redis when true. |
| `HTTP_MAX_BODY_BYTES` | `1048576` | Body size ceiling. |
| `AUTH_VERIFY_MAX_CONCURRENCY` | `8` | Authentication verification gate. |
| `RATE_LIMIT_PER_MINUTE` | `100` | Request-rate limit. |
| `SSE_HEARTBEAT_SECONDS` | `30` | Stream heartbeat interval. |
| `SSE_MAX_DURATION_SECONDS` | `300` | Stream lifetime. |
| `SSE_MAX_CONCURRENT_STREAMS` | `10` | Concurrent stream bound. |

Multi-instance deployments must use distributed coordination. Redis failure
must not silently switch such a deployment to independent process-local
limits. Redis contains no canonical knowledge.

## Conflict Review

| Environment setting | Default |
| --- | --- |
| `APP_TIMEZONE` | `Local` |
| `CONFLICT_REVIEW_START_TIME_LOCAL` | `04:00` |
| `CONFLICT_REVIEW_TTL_DAYS` | `7` |
| `CONFLICT_REVIEW_MAX_CONCURRENCY` | `1` |
| `CONFLICT_REVIEW_BATCH_SIZE` | `100` |
| `CONFLICT_REVIEW_LEASE_SECONDS` | `300` |
| `CONFLICT_REVIEW_MAX_ATTEMPTS` | `5` |
| `CONFLICT_REVIEW_JITTER_SECONDS` | `600` |

Validation enforces positive bounds and enough lease time for provider work.
The runtime general timezone setting supplies scheduler policy. Conflict
semantics belong in [Conflicts And Corrections](Conflicts-And-Corrections).

## Runtime SSO, OAuth, And Private Memory

These are PostgreSQL-backed control settings, not authoritative environment
variables:

| Key | Default | Purpose |
| --- | --- | --- |
| `SSO_PUBLIC_BASE_URL` | Empty | User-portal SSO origin. |
| `SCIM_PUBLIC_BASE_URL` | Empty | SCIM ingress base. |
| `CONTROL_PUBLIC_BASE_URL` | Empty | Administrative OIDC ingress. |
| `MCP_PUBLIC_BASE_URL` | Empty | Advertised protected-resource origin. |
| `SSO_ENTITLEMENT_CACHE_TTL_SECONDS` | `300` | Entitlement cache lifetime. |
| `SSO_SESSION_TTL_SECONDS` | `28800` | Browser session lifetime. |
| `SSO_STATE_TTL_SECONDS` | `600` | Authorization state/PKCE lifetime. |
| `SSO_HTTP_TIMEOUT_SECONDS` | `10` | Provider HTTP timeout. |
| `SSO_COOKIE_SECURE` | Derived from SSO public URL | Secure cookies for SSO and API-key portal sessions. |
| `PRIVATE_MEMORY_RETENTION_DAYS` | `0` | Automatic private-content retention; 0 disables scheduled retention. |

Use HTTPS canonical origins. The MCP base also permits localhost/127.0.0.1 HTTP
for local development. It must not include credentials, a query, or a fragment.
An empty MCP base leaves server protected-resource metadata unavailable; the
user portal can still show a clearly labeled browser-origin URL.

Set `SSO_COOKIE_SECURE=true` behind a public HTTPS ingress. Use false only for
local HTTP. The SCIM/control settings require HTTPS. Provider secrets remain
environment-backed references configured by the operator. The provider's
`protected_resource` configuration owns audience, JWKS, algorithms, identity,
team claim, and external-to-internal scope mappings.

Private retention accepts 0–36500 days. It does not disable owner erasure or
legal holds. See [User Portal SSO](User-Portal-SSO) and
[Portals And API Keys](Portals-And-API-Keys).

## Dreaming

| Runtime key | Default |
| --- | --- |
| `DREAMING_ENABLED` | `false` |
| `DREAMING_FORCE_ENABLED` | `false` |
| `DREAMING_START_TIME_LOCAL` | `03:00` |
| `DREAMING_MAX_OUTPUTS` | `5`, allowed 1–50 |

These are the editable Dream settings. Global disablement wins; force-enable
applies only when globally enabled; otherwise a team's `dreaming.enabled`
override applies. Scheduling, both lanes, and fixed processing bounds are in
[Dreaming](Dreaming).

## Community Detection And Recall Feedback

| Runtime key | Default |
| --- | --- |
| `COMMUNITY_DETECTION_ENABLED` | `false` |
| `COMMUNITY_DETECTION_START_TIME_LOCAL` | `03:30` |
| `COMMUNITY_DETECTION_MAX_CONCURRENCY` | `1` |
| `COMMUNITY_DETECTION_JITTER_SECONDS` | `600` |
| `RECALL_FEEDBACK_ENABLED` | `false` |
| `RECALL_FEEDBACK_RETENTION_DAYS` | `30`, allowed 1–365 |

The service owns detector/input bounds. Evaluation tools are selected by the
evaluation build profile, not a runtime setting.

## Telemetry And Logging

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `LOG_LEVEL` | `trace` when unset | `trace`, `debug`, `info`, `warn`/`warning`, `error`, or `fatal`. |
| `TELEMETRY_ENABLED` | `false` | Enable metrics and telemetry query APIs. |
| `TELEMETRY_PROMETHEUS_URL` | Empty | Prometheus query endpoint. |
| `TELEMETRY_PROMETHEUS_JOB` | Empty | Optional scrape-job selector. |
| `TELEMETRY_QUERY_TIMEOUT_SECONDS` | `5` | Query timeout. |
| `TELEMETRY_SCRAPE_TOKEN` | Required when enabled | Credential for the private scrape endpoint. |

The environment example sets `LOG_LEVEL=info` explicitly. It therefore differs
from the application's unset default. Logs use structured JSON. `fatal`
records severity without stopping the process.

`OPERATION_LOG_RETENTION_DAYS` is a runtime setting, default 30, range 1–365.
Diagnostic body retention is separate. Optional runtime token-price settings
are documented in [Telemetry](Telemetry#cost-rate-card).

The logger protects configured operational credentials and authentication
material, including supported serialized forms. Authorized diagnostics can
retain exact admitted user text; initial security rejections remain
content-free. Metrics and public errors have narrower exposure contracts.

## Risks

A Compose example can explicitly override an application default. A
model/dimension change can make stored vectors incompatible. A runtime policy
key placed only in an environment file may have no effect. Verify effective
configuration in the control portal and readiness output, and keep credentials
out of logs and support bundles.

## Enterprise export startup settings (issue #224)

| Variable | Default | Meaning |
| --- | --- | --- |
| `OTLP_ENABLED` | `false` | Requires `TELEMETRY_ENABLED=true` and at least one destination. |
| `OTLP_TRACE_ENDPOINT` | unset | Full absolute HTTP(S) OTLP trace URL, such as `/v1/traces`. |
| `OTLP_METRIC_ENDPOINT` | unset | Independent OTLP metric URL, such as `/v1/metrics`. |
| `OTLP_TRACE_HEADERS_FILE` | unset | Mounted JSON file containing trace destination headers. |
| `OTLP_METRIC_HEADERS_FILE` | unset | Mounted JSON file containing metric destination headers. |
| `AUDIT_EXPORT_ENABLED` | `false` | Registers the control-admin NDJSON audit download. |
| `DIAGNOSTIC_BUNDLE_ENABLED` | `false` | Registers the control-admin JSON diagnostic download. |

URLs cannot contain embedded credentials, query parameters, or fragments. Header files contain at most 16 HTTP headers within 8 KiB; values never appear in configuration serialization, exporter payloads, or diagnostics. Mount each file explicitly in the deployment. Operational credential protection includes those values and supported authentication forms. Do not put credentials in destination URLs or commit header files.

Trace queues are 2,048 spans with batches of 512. Export/shutdown deadlines are five seconds and metric exports run every 60 seconds. Audit pages default to 100 events and cap at 1,000 events and 1 MiB; diagnostic JSON is capped at 1 MiB. Both downloads have five-second execution/write bounds. These bounds are fixed in this release.

## Private Session Ingestion

`SESSION_INGEST_ENABLED` is a startup boolean, default `false`. The base Compose example forwards it. Restart the server after changing it. Enable only after deploying the session migration and matching private-erasure manifest together, with old instances stopped.

The capability reuses the configured verifier endpoint/key, Remember model, tokenizer, shared provider concurrency gate and embedding provider. Missing required provider configuration keeps the tool unavailable. Discovery and invocation also require write scope and an active authenticated private binding. It adds no separate configuration framework or runtime control endpoint.

The synchronous budget is 180 seconds from service entry. Extraction, linking and assessment share the first 160 seconds; embedding and commit retain their existing phase bounds. A configured provider timeout is a ceiling and cannot extend the remaining phase or request deadline.

Disabling the flag stops new session calls while preserving compatible durable history. A populated intake migration refuses destructive rollback. See [Write Pipeline](Write-Pipeline#private-session-intake).
