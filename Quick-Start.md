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
then a credential with the team permissions you need. Every credential has its own private space.
Save its one-time raw key securely.

For a shared-memory smoke test, use a credential with `read` and `write`,
and submit `remember.target_space: "team_shared"`. The user portal shows the full team UUID and the copyable
`/teams/{team-id}/mcp` URL.

See [Portals And API Keys](Portals-And-API-Keys). A user API key and a control
credential belong to different authorization boundaries.

## 4. Connect And Discover

Configure the client using [MCP Clients](MCP-Clients). Make its bearer key
available privately and allow the server's bounded synchronous writes to finish.

The authenticated `tools/list` schema is authoritative. Remember requires
exact evidence and a retained top-level idempotency key. Relationship proposals
are required and must cite every evidence item; tools can be hidden by scopes or feature settings.

## Python Wheel Option

Install the verified densemem wheel; PyPI publication is pending. Use
DenseMemClient(endpoint, token=api_key), or provide a caller-owned async token
provider. Call list_tools to discover the authenticated schemas.
[Framework Integrations](Framework-Integrations) contains complete Remember and
private-session requests, result handling and safe replay examples.

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

## Google ADK Memory Service

The #220 delivery branch adds Google ADK 2.7.0 support; use its verified wheel
until the matching package is published. Install the wheel with [adk] and enable
SESSION_INGEST_ENABLED on the server. Import DenseMemMemoryService from densemem.adk.

~~~python
from densemem.adk import DenseMemMemoryService

memory = DenseMemMemoryService(endpoint, token=alice_token, bound_user_id="alice")
snapshot = session.model_copy(deep=True)
await memory.add_session_to_memory(snapshot)
result = await memory.search_memory(app_name=session.app_name, user_id="alice", query="Ari Go")
~~~

For multiple users, resolve their distinct credentials through your existing
credential manager. The adapter does not implement token refresh:

~~~python
async def credential_provider(app_name: str, user_id: str) -> str:
    return await credentials.access_token(app_name, user_id)

memory = DenseMemMemoryService(endpoint, credential_provider=credential_provider)
await memory.add_events_to_memory(
    app_name=session.app_name, user_id=session.user_id,
    session_id=session.id, events=session.events,
)
~~~

Retain the original Session/Event snapshot for retries. Calls split automatically
into sequential batches of up to 20 user-text events; earlier batches can commit
before a later failure. Error completed_results and failed_request preserve recovery
context. Supply session_id for event deltas; nonempty custom_metadata and direct
add_memory are unsupported. See [Framework Integrations](Framework-Integrations)
for terminal outcomes, provenance metadata, bounds, isolation and exact replay.


## LangChain Retriever

The LangChain retriever passed validation in
[PR #608](https://github.com/markhuangai/dense-mem/pull/608), at
[`62147d6f`](https://github.com/markhuangai/dense-mem/commit/62147d6fa4fc9da323c4f775250dc5f3d62efe7a).
Code merge and package release remain pending. Install a wheel built from that
verified revision with the [langchain] extra, pinned to LangChain Core 1.5.4:

~~~bash
python -m pip install '/path/to/densemem-VERSION-py3-none-any.whl[langchain]'
~~~

~~~python
from densemem.langchain import DenseMemRetriever

retriever = DenseMemRetriever(endpoint=endpoint, token=api_key, k=10)
documents = retriever.invoke("Which database does this project use?")
~~~

Inside an event loop, use await retriever.ainvoke(query). Supply exactly one static
token or async token_provider; authentication determines visibility. This retriever
reads existing memory. Preserve Document.metadata["densemem"] for provenance and
inspect DenseMemRecallWarning.result when recall is degraded, including an empty
result. See [Framework Integrations](Framework-Integrations#langchain-core-154)
for options, metadata, failures and sync/async behavior.
