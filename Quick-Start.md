# Quick Start

Use this procedure for a fresh local installation. Select a published image
whose contract matches the behavior you intend to use; a deployed older image
may differ from the source documented here.

## Local Shape

One server process uses PostgreSQL with pgvector and configured model
providers. Redis is optional for this single-process setup.

| Surface | Local URL |
| --- | --- |
| MCP | `http://127.0.0.1:8080/mcp` |
| User portal | `http://127.0.0.1:8080/ui` |
| Private control portal | `http://127.0.0.1:8090/` |

## 1. Get A Pinned Release

Replace `vX.Y.Z` with the chosen published tag. Pin the image as well as the
downloaded deployment files:

```bash
mkdir dense-mem-local
cd dense-mem-local
export DENSE_MEM_VERSION="vX.Y.Z"
export DENSE_MEM_IMAGE="ghcr.io/markhuangai/dense-mem:${DENSE_MEM_VERSION}"

curl -fsSLo docker-compose.yml \
  "https://raw.githubusercontent.com/markhuangai/dense-mem/${DENSE_MEM_VERSION}/examples/docker-compose.base.yml"
curl -fsSLo .env.example \
  "https://raw.githubusercontent.com/markhuangai/dense-mem/${DENSE_MEM_VERSION}/examples/.env.example"
cp .env.example .env
```

Retain the image pin in your deployment environment for subsequent starts.
Review the release's documented prerequisites and provenance.

## 2. Configure And Start

Edit the private `.env` file. Set `POSTGRES_PASSWORD`,
`CONTROL_PORTAL_TOKEN`, `AI_API_KEY`, and `AI_VERIFIER_MODEL`.
The Compose example supplies embedding URL/model/dimensions; change all
applicable values together when using another provider.

```bash
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8080/ready
```

Startup applies migrations under a database session lock before serving.
Continue only after required readiness checks pass. Do not expose resolved
Compose configuration or credentialed DSNs in a support report.

Application defaults can differ from explicit environment-example values.
See [Configuration](Configuration#required-startup-settings).

## 3. Create A Team And Credential

Open the private control portal with `CONTROL_PORTAL_TOKEN`. Create a team,
then a credential with the scopes and immutable memory binding you need.
Save its one-time raw key securely.

For a shared-memory smoke test, use a `shared_only` credential with `read`
and `write`. The user portal shows the full team UUID and the copyable
`/teams/{team-id}/mcp` URL.

See [Portals And API Keys](Portals-And-API-Keys). A user API key and a control
credential belong to different authorization boundaries.

## 4. Connect And Discover

Configure the client using [MCP Clients](MCP-Clients). Make its bearer key
available privately and allow the server's bounded synchronous writes to finish.

The authenticated `tools/list` schema is authoritative. Remember requires
exact evidence and a retained top-level idempotency key. Relationship proposals
are optional; tools can be hidden by scopes or feature settings.

## 5. Remember, Recall, And Trace

Ask the client to remember a short fact, for example:

```text
Remember that this project uses PostgreSQL, with a Relationship proposal
grounded in that statement. Keep the complete request for retries.
```

Check the terminal result. Safe evidence may be stored even if its Relationship
proposal is unsupported; inspect both evidence and split dispositions.

Ask the client to recall the project's database and trace an accepted
Relationship. Default recall requires active eligible support. A stored
evidence-only item is not guaranteed to appear.

If withdrawing the test source, use `retract_evidence` with its owned evidence
ID, reason, and a fresh operation key. That appends lifecycle history; it does
not physically erase the evidence. Do not assume there is a generic public
`forget` tool.

## Stop And Restart

```bash
docker compose stop
docker compose start
```

These commands preserve the named PostgreSQL volume. Volume deletion is a
separate destructive operation. Confirm readiness after restart.

## Risks And Next Steps

An unpinned image can change while the Compose file stays fixed. A too-short
client timeout can disconnect during a valid write. A stored item can be absent
from recall because its support is ineligible. Keep the image/request identity
and inspect terminal outcomes and degradations.

Continue with [Using Dense-Mem](Using-Dense-Mem),
[Public HTTPS And Redis](Public-HTTPS-And-Redis), and
[Operations Runbook](Operations-Runbook).
