# Framework Integrations

The Dense-Mem Python core client uses the official MCP SDK. It supports Python
3.11–3.14 and MCP SDK 1.30 through the compatible 1.x line. The client code is
merged through [PR #563](https://github.com/markhuangai/dense-mem/pull/563);
PyPI publication is pending. Install a verified wheel:

~~~bash
python -m pip install /path/to/densemem-0.1.0rc1-py3-none-any.whl
~~~

The Google ADK adapter is implemented on the #220 delivery branch and awaits
merge and publication. LangChain remains planned in #221. The core installs neither framework.

## Authentication and operations

~~~python
from densemem import DenseMemClient

client = DenseMemClient("https://memory.example.com/teams/TEAM_UUID/mcp", token=api_key)
tools = await client.list_tools()
~~~

Pass exactly one of token or token_provider. A provider is an async callable
returning a caller-owned access token. The client resolves it once per operation;
it does not acquire or refresh OAuth credentials. Use an authenticated team URL
or /mcp. Credentials belong only in the bearer header, never the URL. Configure
transport trust through the environment's certificate store when using a private CA.

Each call owns its SDK session, initialization and transport cleanup. The default
190-second total deadline allows the server's bounded 180-second write processing.
A shorter positive finite timeout is supported through the timeout constructor
argument. The synchronous SyncDenseMemClient exposes the same methods and rejects
use inside an active event loop.

Methods are initialize, list_tools, remember, ingest_session, recall_memory and
trace_memory. Requests/results are typed dictionaries with py.typed. Server
validation and authenticated discovery remain authoritative. Current-server
compatibility is dense-mem.v2.7.0. Refresh tool discovery: Remember requires an explicit target even when an older prerelease used the same version string. Targetless historical requests cannot replay.

## Complete Remember request and safe replay

~~~python
from densemem import RememberRequest

request: RememberRequest = {
    "target_space": "private",
    "idempotency_key": retained_operation_key,
    "evidence": [{
        "content": "Dense-Mem stores durable memory in PostgreSQL.",
        "source_type": "manual",
        "source": "Python core example",
        "metadata": {"origin": "python-example"},
    }],
    "relationships": [{
        "ref": "durable-store",
        "subject": {"name": "Dense-Mem", "entity_kind": "project"},
        "predicate": {"proposed_key": "stores_durable_memory_in"},
        "object": {"entity": {"name": "PostgreSQL", "entity_kind": "product"}},
        "polarity": "+",
        "evidence_indices": [0],
    }],
}
result = await client.remember(request)
~~~

Create the operation key once for a deliberate new operation, and retain this
entire request before sending it. For a replay, resend this unchanged request,
including target_space, metadata, and key. Never rotate a key to bypass a conflict. The client
never retries automatically or fills in keys/defaults. Remember requires at least
one proposal citing every evidence item. The client does not infer Relationships.

Inspect evidence and relationship_results: completed processing can include
not_stored proposals and remediation. Returned warnings remain visible. Recall
results preserve server order, equivalent IDs, conflicts, topic communities,
hypotheses, provenance, search states and degradations. These frontiers do not
establish new factual support. Trace requires a returned relationship_id.

## Exact private session ingestion

~~~python
from densemem import SessionRequest

request: SessionRequest = {
    "idempotency_key": retained_session_operation_key,
    "framework": "generic",
    "app_name": "python-example",
    "user_id": "external-user-1",
    "session_id": "session-1",
    "events": [{
        "event_id": "user-event-1",
        "text": "  Ari uses Go. café 🧭\n",
        "occurred_at": "2026-10-11T12:00:00Z",
    }],
}
result = await client.ingest_session(request)
~~~

Send only exact user-authored events. Event IDs/text/timestamps are immutable;
the server owns chunking and extraction. External identifiers are provenance,
never authorization or private-space selection. The server must enable session
ingestion and the caller must have effective private write access, including team-read-only callers; discovery
hides unavailable tools. Remember retains its separate proposal requirements.
Session semantic acceptance is atomic across the request. A no-fact completed
result with search_state not_required is valid.

## Failures and unknown outcomes

DenseMemAuthenticationError reports token acquisition or HTTP 401/403 failures.
DenseMemTransportError reports network/HTTP failure. DenseMemProtocolError
reports MCP/response failure. DenseMemOperationError preserves actionable public
errors and failed receipts in result, with code, reason_code, retryable,
next_action, remediation, correlation_id and submission_id when returned.
DenseMemTimeoutError reports deadlines. DenseMemCancelledError subclasses
asyncio.CancelledError and preserves a returned cancellation receipt if present.

A local timeout, disconnect or cancellation can lose a response after a commit.
An absent receipt means unknown outcome, not a fabricated failed submission.
Recovery uses only the unchanged original request/key. There is no polling,
status tool, background readiness loop or automatic new-key recovery.

SDK 1.30 negotiates protocol 2025-11-25. Local cancellation closes the client's
resources; the server can continue processing under this older protocol and may
commit or reach its bounded deadline. Cancellation does not prove that a write
was prevented. Retain the unchanged request/key for recovery.

## Development and release

Run the repository package check script for lint/type/async tests, installed-wheel
checks, reproducible wheel/sdist builds, vulnerability scanning and SBOM. Run
the repository Python matrix script with a new output directory for the Python
version matrix, and scripts/e2e.sh python_client for real server boundaries.
The example scripts require DENSEMEM_ENDPOINT, DENSEMEM_TOKEN and a retained
DENSEMEM_OPERATION_KEY. Keep the exact request unchanged when rerunning them.

The manual publish-python workflow requires an existing python-vVERSION tag on
merged main matching package metadata, verified PyPI ownership and an OIDC trusted
publisher for this repository/workflow/environment. It verifies artifacts and
produces provenance. Publication is independent of server releases. Never overwrite
a package version; a broken prerelease may be yanked through a separate maintainer
action. This implementation does not register publishers, create tags or publish.


## Google ADK 2.7.0

Install the verified wheel with its optional extra:

~~~bash
python -m pip install '/path/to/densemem-VERSION-py3-none-any.whl[adk]'
~~~

~~~python
from densemem.adk import DenseMemMemoryService

memory = DenseMemMemoryService(endpoint, token=alice_token, bound_user_id="alice")
snapshot = session.model_copy(deep=True)
await memory.add_session_to_memory(snapshot)
response = await memory.search_memory(app_name=snapshot.app_name, user_id="alice", query="Ari Go")
~~~

Use the actual ADK Session and Event types. The runnable adk_memory.py example
retains a fixed session ID, event ID and timestamp. Both add_session_to_memory
and add_events_to_memory return None only after every batch completes. Direct
add_memory raises NotImplementedError. Event deltas require a nonempty session_id;
nonempty custom_metadata is rejected. Events retain their exact visible user
text, IDs, order and UTC timestamps. Assistant, system, tool, state, transcription,
binary and thought content are excluded. Unknown part shapes fail before ingestion.

For multiple users, supply a caller-owned async provider instead of a static token:

~~~python
async def credential_provider(app_name: str, user_id: str) -> str:
    return await credentials.access_token(app_name, user_id)

memory = DenseMemMemoryService(endpoint, credential_provider=credential_provider)
await memory.add_events_to_memory(
    app_name=session.app_name, user_id=session.user_id,
    session_id=session.id, events=session.events,
)
~~~

Return the correct Dense-Mem credential for each application/user pair. The
adapter resolves it once per server request, supports caller-managed rotation,
and never owns OAuth refresh or caches user credentials. Static credentials
require bound_user_id and reject every other user, including empty ingestion.
External application/user/session IDs are provenance; credentials determine
private-space isolation and authorized shared reads. Different application IDs
using one credential do not create separate authorization boundaries.

Calls automatically send sequential batches of at most 20 eligible events.
The default 190-second deadline includes slot waiting, credential resolution,
and every batch; timeout is configurable. max_concurrency defaults to four
public operations. Server token and payload limits still apply, and an individual
event is never truncated or split by the adapter.

Each batch has a deterministic google-adk-v1 key derived from application, user,
session and ordered event identities. Keep the original snapshot until completion.
An earlier batch can commit before a later batch fails. Existing Dense-Mem error
classes retain result and available submission/correlation IDs; completed_results
contains earlier terminal receipts and failed_request contains the exact attempted
request, when a request was attempted. A timeout while waiting has no failed_request.
The batch with an unknown receipt may have committed. Replay only the unchanged
original snapshot, or send the complete unchanged failed_request through the core
client. Do not change text, timestamps, ordering or metadata to recover. Completed
batches replay safely; there is no automatic retry, key rotation or readiness polling.
A completed no-fact result is valid and does not promise a searchable memory.

Search preserves server order, evidence IDs, equivalent IDs and space kind in
MemoryEntry.custom_metadata["densemem"]. The compatible response subclass retains
the entire bounded recall result in response.custom_metadata["densemem"], including
conflicts and degradations on an empty result. Missing author/timestamps remain
unset. The adapter does not add trace calls or post-filter authorized shared memory.

The extra is pinned to google-adk==2.7.0. Core and extra installed-wheel checks,
Python 3.11–3.14 and MCP SDK minimum/latest matrices, and google_adk_memory E2E
are release gates. Package publication and live wiki publication remain separate.

The maintainer approved wiki publication before PR #601 merges and closing #220
on merge. Run google_adk_memory on the exact merged main commit as post-merge
follow-up; branch-local results prove only their tested source.
