# densemem Python client

The Dense-Mem Python core client uses the official MCP SDK. It supports Python
3.11–3.14 and MCP SDK 1.30 through the compatible 1.x line. This package is being
prepared for release; PyPI publication is pending. Install a verified wheel:

~~~bash
python -m pip install /path/to/densemem-VERSION-py3-none-any.whl
~~~

ADK and LangChain adapters/extras will arrive with issues #220 and #221.
The core installs neither framework.

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
compatibility is dense-mem.v2.7.0, including server-supported historical terminal
replay versions; this does not promise compatibility with older deployments.

## Complete Remember request and safe replay

~~~python
from densemem import RememberRequest

request: RememberRequest = {
    "idempotency_key": retained_operation_key,
    "evidence": [
        {
            "content": "Dense-Mem stores durable memory in PostgreSQL.",
            "source_type": "manual",
            "source": "Python core example",
            "metadata": {"origin": "python-example"},
        }
    ],
    "relationships": [
        {
            "ref": "durable-store",
            "subject": {"name": "Dense-Mem", "entity_kind": "project"},
            "predicate": {"proposed_key": "stores_durable_memory_in"},
            "object": {"entity": {"name": "PostgreSQL", "entity_kind": "product"}},
            "polarity": "+",
            "evidence_indices": [0],
        }
    ],
}
result = await client.remember(request)
~~~

Create the operation key once for a deliberate new operation, and retain this
entire request before sending it. For a replay, resend this unchanged request,
including metadata and key. Never rotate a key to bypass a conflict. The client
never retries automatically or fills in keys/defaults. Remember requires at least
one proposal citing every evidence item. The client does not infer Relationships.

Inspect evidence and relationship_results: completed processing can include
not_stored proposals and remediation. Returned warnings remain visible. Recall
results preserve server order, equivalent IDs, conflicts, topic communities,
provenance, search states and degradations. These frontiers do not
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
    "events": [
        {
            "event_id": "user-event-1",
            "text": "  Ari uses Go. café 🧭\n",
            "occurred_at": "2026-10-11T12:00:00Z",
        }
    ],
}
result = await client.ingest_session(request)
~~~

Send only exact user-authored events. Event IDs/text/timestamps are immutable;
the server owns chunking and extraction. External identifiers are provenance,
never authorization or private-space selection. The server must enable session
ingestion and the credential must have a writable private binding; discovery
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

Run packages/python/scripts/check.sh for lint/type/async tests, installed-wheel
checks, reproducible wheel/sdist builds, vulnerability scanning and SBOM. Run
packages/python/scripts/matrix.sh with a new output directory for the Python
version matrix, and scripts/e2e.sh python_client for real server boundaries.
The example scripts require DENSEMEM_ENDPOINT, DENSEMEM_TOKEN and a retained
DENSEMEM_OPERATION_KEY. Keep the exact request unchanged when rerunning them.

The server prerelease and stable-release workflows call publish-python to build
and verify artifacts after their release gates pass, then upload through their own
top-level publishing jobs. Manual recovery uploads directly from publish-python;
the reusable invocation never uploads to PyPI. Only tags whose exact source belongs
to merged main are eligible;
PR branches cannot publish. Python shares the server release version: v2.6.6-rc.10
becomes 2.6.6rc10 and v2.6.6 becomes 2.6.6. The checkout's 0.0.0 version is for local
development; release builds stamp the selected tag's version and exact Source URL
into artifact metadata without changing Git history.

Change detection compares packages/python with the last successfully published
Python source recorded on PyPI. Unchanged prereleases are skipped, and publishing
a final version after a Python RC does not require another code change. Incomplete
uploads remain eligible; retries verify every existing artifact's SHA-256 before
uploading missing files. The manual publish-python workflow accepts the same shared
tag for recovery. Never overwrite an existing version or silently accept different
bytes; a broken prerelease may be yanked through a separate maintainer action.

Verify control of the densemem PyPI name and register trusted publishers for
markhuangai/dense-mem, workflows release-rc.yml, release.yml and publish-python.yml,
each with environment pypi. For a new project, configure one pending publisher for
the first publishing workflow; after it creates the project, register the other
two as normal project publishers before using those paths. PyPI does not support
reusable workflows as trusted publishers. This repository does not register
publishers or change account settings. Release builds retain the
installed-wheel/version matrix, reproducibility,
vulnerability scan, SBOM and provenance gates.
