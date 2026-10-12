import asyncio
import copy
import logging

import pytest

from densemem import (
    DenseMemAuthenticationError,
    DenseMemCancelledError,
    DenseMemClient,
    DenseMemOperationError,
    DenseMemProtocolError,
    DenseMemTimeoutError,
    DenseMemTransportError,
    SyncDenseMemClient,
)


@pytest.mark.parametrize("kwargs", [{}, {"token": "x", "token_provider": lambda: "x"}])
def test_authentication_is_exclusive(kwargs):
    with pytest.raises(ValueError, match="exactly one"):
        DenseMemClient("http://localhost/mcp", **kwargs)


@pytest.mark.parametrize("timeout", [0, -1, float("nan"), float("inf")])
def test_timeout_is_bounded(timeout):
    with pytest.raises(ValueError, match="positive and finite"):
        DenseMemClient("http://localhost/mcp", token="x", timeout=timeout)


@pytest.mark.parametrize(
    "endpoint",
    ["file:///tmp/mcp", "http://secret@localhost/mcp", "http://localhost/mcp?token=secret"],
)
def test_url_cannot_carry_credentials(endpoint):
    with pytest.raises(ValueError):
        DenseMemClient(endpoint, token="x")


async def test_sdk_discovery_and_every_method_preserve_payload_and_result(wire_server):
    endpoint, state = wire_server
    client = DenseMemClient(endpoint, token="static-secret")
    initialized = await client.initialize()
    assert initialized.serverInfo.name == "densemem-client-test"
    assert {t.name for t in (await client.list_tools()).tools} == {
        "remember",
        "ingest_session",
        "recall_memory",
        "trace_memory",
    }
    response = {
        "processing_state": "completed",
        "search_state": "not_required",
        "relationship_results": [
            {
                "ref": "one",
                "disposition": "not_stored",
                "splits": [],
                "reason": "unsupported",
                "remediation": "Supply evidence.",
            }
        ],
        "results": [{"context": " café 🧭\n", "equivalent_evidence_ids": ["B", "A"]}],
        "conflicts": [{"positions": ["second", "first"]}],
        "related_communities": [{"logical_community_id": "topic", "summary": "derived"}],
        "degradations": [{"code": "optional_context_omitted"}],
        "warnings": ["context omitted"],
        "session": {"event_id": "immutable"},
    }
    state["result"] = response
    request = {
        "idempotency_key": "retained",
        "evidence": [
            {"content": " exact \n🧭", "metadata": {"nested": [False, None, 0], "revision": "1"}}
        ],
    }
    original = copy.deepcopy(request)
    for method in (
        client.remember,
        client.ingest_session,
        client.recall_memory,
        client.trace_memory,
    ):
        assert await method(request) == response
    assert all(args == original for _, args in state["calls"])
    assert request == original
    assert len(state["calls"]) == 4
    assert all(auth == b"Bearer static-secret" for auth, _ in state["requests"])


async def test_provider_is_once_per_operation_and_rotation_is_caller_owned(wire_server):
    endpoint, state = wire_server
    count = 0

    async def provider():
        nonlocal count
        count += 1
        return f"access-token-{count}"

    client = DenseMemClient(endpoint, token_provider=provider)
    await client.list_tools()
    first = len(state["requests"])
    await client.remember({"idempotency_key": "retained"})
    assert count == 2
    assert all(auth == b"Bearer access-token-1" for auth, _ in state["requests"][:first])
    assert all(auth == b"Bearer access-token-2" for auth, _ in state["requests"][first:])


async def test_request_is_snapshotted_before_credential_await(wire_server):
    endpoint, state = wire_server
    started, release = asyncio.Event(), asyncio.Event()

    async def provider():
        started.set()
        await release.wait()
        return "token"

    request = {"idempotency_key": "retained", "metadata": {"revision": 1}}
    original = copy.deepcopy(request)
    pending = asyncio.create_task(
        DenseMemClient(endpoint, token_provider=provider).remember(request)
    )
    await started.wait()
    request["metadata"]["revision"] = 2
    release.set()
    await pending
    assert state["calls"][0][1] == original


async def test_failed_receipt_preserves_identifiers_and_actionable_fields(wire_server):
    endpoint, state = wire_server
    state["result"] = {
        "processing_state": "failed",
        "submission_id": "server-receipt",
        "correlation_id": "corr",
        "errors": [
            {
                "code": "provider_unavailable",
                "reason_code": "provider_busy",
                "message": "Provider unavailable",
                "retryable": True,
                "next_action": "retry_same_request",
                "remediation": "Resend the same request.",
            }
        ],
    }
    state["is_error"] = True
    with pytest.raises(DenseMemOperationError) as raised:
        await DenseMemClient(endpoint, token="token").remember({"idempotency_key": "retained"})
    error = raised.value
    assert error.result == state["result"]
    assert (error.submission_id, error.correlation_id, error.code) == (
        "server-receipt",
        "corr",
        "provider_unavailable",
    )
    assert error.retryable and error.next_action == "retry_same_request"
    assert error.reason_code == "provider_busy"
    assert len(state["calls"]) == 1


@pytest.mark.parametrize(
    "code,error_type",
    [("request_timeout", DenseMemTimeoutError), ("request_cancelled", DenseMemCancelledError)],
)
async def test_server_deadline_or_cancellation_preserves_receipt(wire_server, code, error_type):
    endpoint, state = wire_server
    state["is_error"] = True
    state["result"] = {
        "processing_state": "failed",
        "submission_id": "returned-receipt",
        "correlation_id": "returned-correlation",
        "errors": [
            {
                "code": code,
                "message": "Request stopped",
                "retryable": True,
                "next_action": "retry_same_request",
                "remediation": "Replay unchanged.",
            }
        ],
    }
    with pytest.raises(error_type) as raised:
        await DenseMemClient(endpoint, token="token").remember({})
    assert raised.value.submission_id == "returned-receipt"
    assert raised.value.result == state["result"]


@pytest.mark.parametrize("status", [401, 403, 404, 429, 503])
async def test_http_auth_errors_are_typed_and_do_not_leak(wire_server, caplog, status):
    endpoint, state = wire_server
    state["status"] = status
    caplog.set_level(logging.DEBUG)
    secret = "private-static-token-sentinel"
    state["http_error"] = {
        "code": "expired_token" if status == 401 else "request_rejected",
        "reason_code": "expired_credential",
        "message": "Use a fresh access token.",
        "retryable": False,
        "next_action": "refresh_credentials",
        "remediation": "Acquire a new access token and resubmit.",
        "details": [],
    }
    state["http_headers"] = {"X-Correlation-ID": "returned-http-correlation"}
    cls = DenseMemAuthenticationError if status in (401, 403) else DenseMemTransportError
    with pytest.raises(cls) as raised:
        await DenseMemClient(endpoint, token=secret).initialize()
    assert raised.value.status_code == status
    assert raised.value.submission_id is None
    assert raised.value.result == {
        **state["http_error"],
        "correlation_id": "returned-http-correlation",
    }
    assert raised.value.code == state["http_error"]["code"]
    assert raised.value.reason_code == "expired_credential"
    assert raised.value.next_action == "refresh_credentials"
    assert raised.value.remediation == state["http_error"]["remediation"]
    assert raised.value.retryable is False
    assert raised.value.correlation_id == "returned-http-correlation"
    assert secret not in repr(raised.value) + caplog.text


@pytest.mark.parametrize("status", [401, 403, 404, 429, 503])
async def test_http_errors_after_initialization_preserve_status_and_guidance(wire_server, status):
    endpoint, state = wire_server
    state["status_after_initialize"] = status
    state["http_error"] = {
        "code": "expired_token" if status == 401 else "request_rejected",
        "message": "Request rejected after initialization",
        "next_action": "refresh_credentials",
        "remediation": "Acquire a new credential.",
        "retryable": False,
    }
    state["http_headers"] = {"X-Correlation-ID": "late-http-correlation"}
    cls = DenseMemAuthenticationError if status in (401, 403) else DenseMemTransportError
    with pytest.raises(cls) as raised:
        await DenseMemClient(endpoint, token="token", timeout=3).remember(
            {"idempotency_key": "retained"}
        )
    assert raised.value.status_code == status
    assert raised.value.result == {**state["http_error"], "correlation_id": "late-http-correlation"}
    assert raised.value.submission_id is None
    assert not state["calls"]


async def test_concurrent_http_failures_stay_with_their_operation(wire_server):
    endpoint, state = wire_server
    state["status_after_initialize"] = {b"Bearer token-1": 401, b"Bearer token-2": 503}
    state["http_error"] = {"code": "request_rejected", "message": "Request rejected"}
    count = 0

    async def provider():
        nonlocal count
        count += 1
        return f"token-{count}"

    client = DenseMemClient(endpoint, token_provider=provider, timeout=3)
    first, second = await asyncio.gather(
        client.remember({"idempotency_key": "first"}),
        client.remember({"idempotency_key": "second"}),
        return_exceptions=True,
    )
    assert isinstance(first, DenseMemAuthenticationError) and first.status_code == 401
    assert isinstance(second, DenseMemTransportError) and second.status_code == 503
    assert first.result == second.result == state["http_error"]
    assert count == 2 and not state["calls"]


async def test_provider_exception_is_sanitized_and_has_no_recovery(wire_server, caplog):
    endpoint, state = wire_server

    async def provider():
        raise RuntimeError("private-access-token-sentinel")

    with pytest.raises(DenseMemAuthenticationError) as raised:
        await DenseMemClient(endpoint, token_provider=provider).initialize()
    assert "private-access-token-sentinel" not in repr(raised.value) + caplog.text
    assert not state["requests"]


async def test_sdk_debug_messages_redact_only_the_operation_credential(wire_server, caplog):
    endpoint, state = wire_server
    secret = "private-echoed-credential-sentinel"
    state["is_error"] = True
    state["result"] = {"code": "invalid_request", "message": "Rejected " + secret}
    caplog.set_level(logging.DEBUG)
    with pytest.raises(DenseMemOperationError) as raised:
        await DenseMemClient(endpoint, token=secret).remember({"diagnostic": secret})
    assert secret not in str(raised.value) + repr(raised.value) + caplog.text
    assert "[REDACTED]" in caplog.text
    logging.getLogger("mcp.client.streamable_http").debug("Outside operation: %s", secret)
    assert "Outside operation: " + secret in caplog.text


async def test_malformed_response_fails_without_retry(wire_server):
    endpoint, state = wire_server
    state["malformed"] = True
    with pytest.raises(DenseMemProtocolError):
        await DenseMemClient(endpoint, token="token", timeout=2).initialize()


async def test_cross_origin_redirect_is_not_followed(wire_server):
    endpoint, state = wire_server
    state["redirect"] = "http://127.0.0.1:1/credential-sink"
    with pytest.raises(DenseMemTransportError):
        await DenseMemClient(endpoint, token="token").initialize()


async def test_timeout_closes_sdk_tasks_and_preserves_unknown_receipt(wire_server):
    endpoint, state = wire_server
    state["delay"] = 10
    baseline = set(asyncio.all_tasks())
    with pytest.raises(DenseMemTimeoutError) as raised:
        await DenseMemClient(endpoint, token="token", timeout=0.3).remember(
            {"idempotency_key": "one"}
        )
    assert raised.value.submission_id is None
    assert raised.value.result is None
    assert len(state["calls"]) == 1
    assert set(asyncio.all_tasks()) == baseline


async def test_cancellation_closes_sdk_tasks(wire_server):
    endpoint, state = wire_server
    state["delay"] = 10
    baseline = set(asyncio.all_tasks())
    pending = asyncio.create_task(DenseMemClient(endpoint, token="token").remember({}))
    assert await asyncio.to_thread(state["called"].wait, 5)
    pending.cancel()
    with pytest.raises(DenseMemCancelledError):
        await pending
    assert set(asyncio.all_tasks()) == baseline
    assert len(state["calls"]) == 1


async def test_sync_rejects_nested_event_loop(wire_server):
    endpoint, state = wire_server
    with pytest.raises(RuntimeError, match="event loop"):
        SyncDenseMemClient(endpoint, token="token").initialize()
    assert not state["requests"]


def test_sync_uses_the_same_sdk_transport(wire_server):
    endpoint, state = wire_server
    client = SyncDenseMemClient(endpoint, token="token")
    assert client.initialize().serverInfo.name == "densemem-client-test"
    assert len(client.list_tools().tools) == 4
    assert client.remember({"idempotency_key": "retained"}) == state["result"]
