# Portals And API Keys

Dense-Mem has a team/user portal on the main listener and a deployment control
portal on a separate private listener.

The control portal links admitted Remember invocations to canonical attempts,
shows protected diagnostic details, and exposes Dream investigation and
operation logs. Raw capture payloads are fetched only in authorized detail
views.

| Portal | Local URL | Authentication | Purpose |
| --- | --- | --- | --- |
| user | `http://127.0.0.1:8080/ui` | Dense-Mem key or configured SSO | same-team recall, graph/trace, Dreams, keys, and self telemetry |
| control | `http://127.0.0.1:8090/` | control-plane authentication | cross-team administration, config, security, logs, and operations |

Keep the control portal private.

## Boundary

```mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart LR
  User["User/team manager"] --> Main["Main listener :8080"]
  Main --> UI["/ui"]
  Main --> UserAPI["/ui/api/*"]
  Main --> MCP["/mcp"]
  UI --> Team["Authenticated team boundary"]
  UserAPI --> Team
  MCP --> Team

  Operator["Deployment operator"] --> Control["Control listener :8090"]
  Control --> ControlAPI["/control/api/*"]
  Control --> Admin["Cross-team control plane"]
  Control --> Metrics["Protected metrics"]
```

A user key never becomes a control-plane credential. A control token must not be
accepted by main memory routes.

The user and control portals use first-party server handlers built for their
own browser views. Those handlers are not a public client SDK or an alternative
memory automation contract; external memory use goes through MCP.

Portal handlers consume the access application API for authentication,
identity, credentials, SSO/OAuth/SCIM entitlements, sessions, and rate limits.
They do not import PostgreSQL, Redis, or in-process access adapters and cannot
replace the authenticated team or permanent owner with route or body data.

MCP routes are `/mcp` and `/teams/{team_id}/mcp`. First-party browser APIs
use `/ui/api/*` and `/control/api/*`; `/ui` is the user document route.
Health and identity-discovery endpoints serve their own bounded purposes.

User-portal authentication remains either a Dense-Mem API key or configured
SSO. API-key login uses the bearer path by default. When the user explicitly
checks “Keep me signed in for 7 days,” the portal exchanges that direct bearer
login for an opaque server-side browser session; the raw API key is never put
in a cookie. SSO continues to use its separate browser-session and CSRF
contract.

The API-key portal session is fixed at seven days on the server. The checked
browser cookies are persistent for seven days; an unchecked exchange creates
browser-session cookies while retaining the same seven-day server expiry. The
session stores the stable credential ID, so in-place key rotation does not
sign the user out, while revocation, expiration, team deletion, role, scopes,
and SSO-owned-key entitlement are revalidated on every request. The session
cookie is HttpOnly, host-only, SameSite=Lax, and scoped to `/ui`; the separate
CSRF cookie is readable by the first-party UI and is sent as
`X-Dense-Mem-CSRF` for state-changing requests.

## User Portal

The user portal provides:

- authenticated team/membership/credential/session display
- full selected team UUID and copyable `/teams/{team_id}/mcp` URL, refreshed
  after an SSO team switch
- ranked Evidence recall with separate compact Relationship and Community frontiers
- Entity/Value graph exploration
- Relationship trace with evidence/history panels
- Relationship details for use in the MCP correction workflow
- separate Hypothesis/community views
- self/team-scoped telemetry according to role
- current-credential key rotation
- owner-scoped private-memory erasure routes and status polling
- same-team credential management for managers
- SSO login/team switching when configured

It does not expose raw database queries, embeddings, provider prompts, or cross-team
candidates.

## Graph Explorer

```mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart LR
  Overview["Team graph overview"] --> Entity["Entity/Value nodes"]
  Overview --> Edge["SemanticEdges"]
  Edge --> Detail["Relationship detail"]
  Detail --> Trace["Evidence, VerificationEvents,<br/>transitions, conflicts"]
  Overview --> Overlay["Optional community/Hypothesis<br/>analytical overlays"]
```

The semantic graph contains only Entity/Value endpoints and active semantic
edges. Evidence, observations, verification, and transitions are detail/trace
records, not graph nodes. Hypotheses use distinct styling and never look like
active knowledge.

Overview/local queries enforce node, edge, depth, time, and response-size
budgets. Large teams should use search/local views rather than an unbounded full
graph.

Parallel authored edges show an author badge and conflict state. The portal may
visually group equivalent edges for clarity only when it retains every
`relationship_id` and author in the expandable group; visual grouping never
merges their lifecycle or data.

Graph visualization is read-only. Owned corrections use MCP. The control
portal has separate Relationship-conflict queue and evidence-conflict views;
those capabilities do not turn the graph canvas into a general memory editor.

## User Portal SSO

OIDC setup, entitlement mapping, SSO-owned keys, and session behavior are in
[User Portal SSO](User-Portal-SSO).

## Control Portal

The control portal manages:

- teams and credentials
- membership roles and credential scopes
- credential creation, rotation, expiry, and revocation
- SSO providers and mappings
- global/team runtime configuration
- versioned RRF enable/disable, branch, and deterministic-rerank contracts with
  atomic activation/rollback
- provider/embedding and search-index generation visibility without exposing
  secret values
- search document convergence and terminal Remember-attempt diagnostics
- private-space inventory, legal holds, retention policy/runs, and erasure
  operation status
- Search convergence: expected/current/drifted documents, allowlisted failure
  codes, affected teams, guidance, and the latest reconciliation run
- PostgreSQL, pgvector, query, and backup health
- operation/audit/security views
- telemetry and feedback summaries
- dream/community scheduler state
- memory-pack export and bounded omission reporting

Semantic changes use the supported Remember, evidence lifecycle, correction,
and governed conflict workflows with audit.

The control usage rollup shows the dependency observation time, `healthy/total`,
every unhealthy dependency name, bounded reason code, and check latency. The
normal checks are PostgreSQL, PostgreSQL topology, `pgvector`, authority,
search readiness, and Redis. Redis may report `single_node_mode` when the
single-node process-local coordination mode is intentionally enabled. That
status is not sufficient for a multi-instance deployment.

Telemetry cards and charts carry `ready`, `inactive`, `unavailable`, or
`unsupported` status. Ready zero values remain visible. Disabled features,
missing activity, missing provider usage/pricing, failed queries, and failed
lifecycle reads explain themselves with bounded reasons. Successful items stay
visible during partial degradation. Non-ready items appear in a collapsed
Inactive / unavailable disclosure, and unavailable charts do not render empty
axes. The user portal omits operator-only cards; control team/profile views
show scope-unsupported entries where that boundary matters.

Overview, usage rollup, and telemetry refresh every 60 seconds while visible.
Changing a scope or window cancels the previous request, and manual refresh is
kept available without allowing overlapping requests.

The Search convergence view is read-only. It reports current
expected/current/drifted documents, compatible contract/generation, failure
guidance, and reconciliation state through `GET /control/api/search/convergence`.
It has no generic retry button or provider credential editor. Fix configuration
or provider faults through their supported administrative path.

Dream diagnostics explain run phases and protected provider exchanges. Remember
diagnostics distinguish execution, replay, conflict, and observed response
delivery. Details are in [Telemetry](Telemetry) and [Dreaming](Dreaming).

## Credentials And API Keys

A credential belongs to one actor identity, membership, and team. Its API key
is bearer material for that credential; role and grants come from the resolved
membership, and effective scopes are bounded by both the membership and the
credential.

| Role | Purpose |
| --- | --- |
| `manager` | same-team credential/config administration where permitted |
| `member` | normal same-team memory use |

| Scope | Purpose |
| --- | --- |
| `read` | recall, trace, Entity/Relationship inspection, and owned private memory writes |
| `write` | team-shared Remember and owned lifecycle changes, plus supported feedback/Dream decisions |
| `feedback:read` | stored recall-feedback details |

Role does not imply scope. Without `write`, a key can remember and mutate its own private memory; team-shared mutation is denied.

Use cases:

| Key | Use |
| --- | --- |
| read-write member | main AI client |
| team-read-only member | automation that remembers private experience while reading shared memory |
| manager with minimal scopes | team administration |
| SSO-owned personal key | user client tied to current entitlement |

## Create And Rotate

Deployment operators create teams and credentials through the private control
portal or its authenticated API. For example:

```bash
control_token="<CONTROL_PORTAL_TOKEN>"

curl -fsS -X POST http://127.0.0.1:8090/control/api/teams \
  -H "Authorization: Bearer ${control_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"primary-memory"}'
```

Create the first credential from the returned team ID at
`POST /control/api/teams/{team_id}/credentials`. The response contains
`data.credential` and the one-time `data.api_key`; store the raw key once.
Release images do not contain standalone
provisioning, listing, deletion, rotation, migration, or conflict-review
commands.

Team managers create and rotate credentials through the Team tab. SSO members
use the My credentials tab subject to their current entitlement. Deployment operators
use the control portal/API on the private listener.

Do not place raw keys in command history on shared machines; prefer a protected
input/file mechanism supported by the client.

## Rotation

```mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart LR
  Create["Create replacement key"] --> Store["Store/test replacement"]
  Store --> Switch["Switch clients"]
  Switch --> Observe["Verify use/audit"]
  Observe --> Revoke["Revoke old key"]
```

Rotation must allow a bounded overlap where policy permits. Never delete the old
credential before confirming the replacement works unless compromise requires
immediate revocation.

## Deletion And Revocation

Revocation stops future authentication but preserves audit and actor references.
Credential deletion deactivates authentication while retaining its tombstone
when durable records still reference the actor/owner. Control/manager
credential deletion also queues erasure of the selected credential's
membership-owned `private` space; it preserves the issuing SSO user's separate
private space, `team_shared`, and the audit tombstone. The safe credential-deletion audit is
committed before the erasure operation becomes visible to a worker. The
explicit SSO owner deletion flow requires irreversible acknowledgement and has
the same private-space boundary.
A legal hold blocks the physical purge/retirement worker but does not keep a
credential bearer active.

Team deletion is a soft tombstone: the team row is marked deleted, active
team credentials are revoked, and future writes/jobs for that team are
rejected. Existing knowledge, support history, source lineage, conflicts, and
audit rows remain durable. There is no public restore/re-enable operation.
Recovery requires a separately authorized procedure.

## SSO-Owned Keys

An SSO-owned key is usable only while its external entitlement remains valid.
Entitlement revalidation can revoke or deny it even when the local hash is
valid. Manual rotation cannot sever that ownership silently.

Deleting an SSO-owned key requires `Idempotency-Key` and exactly
`{"acknowledge_irreversible":true}`. An accepted response is `202`, disables
the key immediately, and returns the durable operation to poll. A legal hold on
its membership-owned private space, or an affected migration copy, leaves that
operation queued until the hold is released. Every credential has a private
space eligible for erasure. The purge reaches copies of its original records
in other private spaces and invalidates dependent memories while preserving
unrelated records, shared memory, and bounded audit history.

## Private-Memory Erasure

The user portal API exposes irreversible owner routes for the authenticated
membership's private space and owner-scoped status polling.
The current browser UI exposes SSO-owned credential revocation; it does not yet
provide a dedicated privacy workspace or controls for profile erasure. The
control portal API can inventory private spaces, place/release legal holds,
request a scoped erasure, run retention, and inspect control-only operation
details. `202 Accepted` means the request is durable and queued; completion is
asynchronous.

Erasure idempotency is target-scoped. Retrying the same canonical request for
the same target returns the original operation. Reusing the same raw key for
another eligible private target creates a distinct operation; changing the
canonical request within the same target scope returns `409 Conflict`.

The erasure worker seals and generation-fences the exact private space,
rechecks holds, deletes the complete space-owned manifest, verifies zero
remaining rows, and scrubs private audit payloads. There is no cancel or restore
button. A terminal worker failure leaves the space sealed; after correcting the
fault, an authorized owner or control request with a fresh idempotency key
queues a new operation without changing the original target generation or
retirement outcome. Recovery after completion requires the backup/PITR
procedure and does not re-enable a credential.

## Security Rules

- keep control and metrics listeners private
- use HTTPS beyond loopback
- assign least privilege and separate automation keys
- store only hashes in the database
- never display a raw key again
- audit create/rotate/revoke/role/scope changes
- rate-limit authentication and key operations
- revalidate same-team paths/IDs
- do not copy keys into provider prompts, logs, or support bundles

## Risks And Mitigations

| Risk | Concrete failure mode | Mitigation |
| --- | --- | --- |
| Control portal exposed | public attacker can manage all teams | private listener/ingress and separate strong auth |
| Manager overreach | manager key writes despite lacking `write` | independent role/scope checks |
| Graph leaks provenance | overview renders evidence/verification as nodes | semantic-only graph plus authorized detail/trace panels |
| Team visibility becomes write authority | B edits A's Relationship from the portal | show immutable owner and route B through its own remember/cross-reference workflow |
| Key rotation outage | old key revoked before clients switch | create-test-switch-observe-revoke sequence |
| SSO entitlement drift | removed user keeps a local personal key | periodic/request-time entitlement revalidation |
| Profile deletion erases audit | cascading FK removes actor history | deactivate/tombstone actor and preserve durable references |
| Team deletion erases knowledge | delete path cascades team-owned semantic rows | tombstone team, revoke credentials, and preserve durable knowledge/audit |
| Private erasure removes unrelated records | SSO key deletion removes the issuer's independent memory or team knowledge | membership ownership and original-writer lineage, exact purge targets, A/B/C tests, and terminal zero-count verification |

## Acceptance Invariants

1. user portal reads only authenticated-team data
2. control authentication is never accepted by main routes
3. graph overview contains only semantic nodes/edges plus explicit analytical
   overlays
4. browser decisions use the canonical PostgreSQL write pipeline
5. role and scopes remain independent
6. raw API keys are returned once and never stored reversibly
7. credential deletion erases its membership-private space and every migration
   copy of its original records, invalidating dependent memories; applicable
   holds block purge, and unrelated private records, shared memory, and redacted
   audit history remain
8. team deletion tombstones the team and revokes credentials without deleting
   knowledge or audit rows
9. external memory automation uses MCP; portal handlers remain first-party UI
   implementation details

## Canonical Identity And Credentials

`actor_identities`, `team_memberships`, and `credentials` are the only current
identity authority. `team_admin` controls governance; it does not
imply private-memory access to another owner. Effective scopes are the
intersection of membership maximum grants and the credential's scopes.
Permanent ownership aliases preserve authored-memory, correction, audit, and
usage references independently of credential rotation.

Every authenticated request carries immutable `team_id`, actor identity,
membership, permanent owner alias, role/grants, and an optional credential ID.
SSO sessions use the selected membership's owner alias and set `credential` to
`null`. Direct API-key and API-key portal sessions carry a credential; for a
standard API credential, its stable ID is also its permanent owner alias.
Neither route bodies nor clients can select the owner alias.

The current first-party credential routes are:

```text
/control/api/teams/{team_id}/credentials[/{credential_id}[/rotate]]
/ui/api/team/credentials[/{credential_id}[/rotate]]
/ui/api/credential/rotate
/ui/api/sso/credentials
/ui/api/sso/credentials/{credential_id}
/ui/api/sso/credentials/{credential_id}/rotate
```

Rotation changes bearer material in place. Deletion disables the credential
while retaining the credential and alias for audit. Actor deactivation depends
on remaining active memberships, so another team membership is preserved. No portal or public API can invoke schema cleanup.

## Memory Destinations and Effective Access

Every credential has an inherent private space. `memory_binding` is obsolete
and rejected when supplied. Rotation preserves the credential ID, membership,
and private space; revoked identities cannot be reactivated through rotation or
provisioning. Expired credentials retain their spaces for authorized rotation.

Portals display `memory_access.private.{read,write}` and
`memory_access.team_shared.{read,write}`. Team-read-only keys can remember,
correct, retract, and erase their own private memory. They can use private
`ingest_session` when its feature gate is enabled. Shared writes require the
current write grant and retain semantic ownership checks.

The LLM chooses `private` or `team_shared` in the required `remember.target_space`
field on each submission. The portal has no destination selector. One target
covers the batch, and changing it under an existing idempotency key conflicts.
A denied shared request is not redirected. Contract `dense-mem.v2.7.0` requires
refreshed tool discovery even for clients already using that version; targetless
historical retries fail validation without rewriting stored history.
