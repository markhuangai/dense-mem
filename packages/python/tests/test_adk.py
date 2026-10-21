import asyncio
import copy
import logging
import threading
from concurrent.futures import Future, ThreadPoolExecutor

import pytest

from densemem import (
    DenseMemAuthenticationError,
    DenseMemCancelledError,
    DenseMemOperationError,
    DenseMemProtocolError,
    DenseMemTimeoutError,
)

adk = pytest.importorskip("densemem.adk", exc_type=ModuleNotFoundError)
types = pytest.importorskip("google.genai.types")
Event = pytest.importorskip("google.adk.events").Event
Session = pytest.importorskip("google.adk.sessions").Session
EventActions = pytest.importorskip("google.adk.events.event_actions").EventActions
BaseMemoryService = pytest.importorskip("google.adk.memory.base_memory_service").BaseMemoryService


def event(index=0, text="  Ari uses Go. café 🧭\n", **kwargs):
    return Event(
        id=f"event-{index}",
        author="user",
        timestamp=1720000000.125,
        content=types.Content(role="user", parts=[types.Part(text=text)]),
        **kwargs,
    )


def session(events=None):
    return Session(app_name="app", user_id="alice", id="session", events=events or [])


def service(endpoint, **kwargs):
    return adk.DenseMemMemoryService(
        endpoint, token="static-secret", bound_user_id="alice", **kwargs
    )


def terminal():
    return {"processing_state": "completed", "search_state": "not_required", "submission_id": "s"}


@pytest.mark.parametrize(
    "kwargs",
    [
        {},
        {"token": "x"},
        {"token": "x", "bound_user_id": " "},
        {"token": "x", "credential_provider": lambda: "x"},
        {"credential_provider": lambda: "x", "bound_user_id": "alice"},
        {"credential_provider": "invalid"},
    ],
)
def test_credential_modes_are_explicit(kwargs):
    with pytest.raises(ValueError):
        adk.DenseMemMemoryService("http://localhost/mcp", **kwargs)


@pytest.mark.parametrize(
    "kwargs",
    [
        {"timeout": 0},
        {"timeout": float("inf")},
        {"timeout": float("nan")},
        {"max_concurrency": 0},
        {"max_concurrency": True},
        {"max_concurrency": 1.5},
    ],
)
def test_operation_bounds(kwargs):
    with pytest.raises(ValueError):
        service("http://localhost/mcp", **kwargs)


def test_real_adk_mapping_filters_without_mutating():
    mixed = event()
    mixed.content.parts = [
        types.Part(text="  Ari"),
        types.Part(text=" hidden", thought=True),
        types.Part(inline_data=types.Blob(data=b"binary", mime_type="image/png")),
        types.Part(function_response=types.FunctionResponse(name="tool", response={"x": "tool"})),
        types.Part(text=" uses Go. café 🧭\n"),
    ]
    mixed.actions = EventActions(state_delta={"secret": "state-only"})
    events = [mixed, event(1, partial=True), event(2, interrupted=True)]
    for author in ("assistant", "system", "tool", "agent"):
        excluded = event(len(events))
        excluded.author = author
        events.append(excluded)
    events += [
        Event(author="user", actions=EventActions(state_delta={"x": "state"})),
        event(9, "  "),
    ]
    original = copy.deepcopy(events)
    requests = adk._session_requests("app", "alice", "session", events)
    assert events == original
    assert len(requests) == 1
    assert requests[0]["events"] == [
        {
            "event_id": "event-0",
            "text": "  Ari uses Go. café 🧭\n",
            "occurred_at": "2024-07-03T09:46:40.125000Z",
        }
    ]
    assert requests[0]["framework"] == "google-adk"
    assert requests == adk._session_requests("app", "alice", "session", original)


async def test_interface_rejects_unsupported_and_foreign_empty_calls(wire_server):
    endpoint, state = wire_server
    memory = service(endpoint)
    assert isinstance(memory, BaseMemoryService)
    with pytest.raises(NotImplementedError):
        await memory.add_memory(app_name="app", user_id="alice", memories=[])
    with pytest.raises(ValueError, match="session_id"):
        await memory.add_events_to_memory(app_name="app", user_id="alice", events=[])
    with pytest.raises(ValueError, match="custom_metadata"):
        await memory.add_events_to_memory(
            app_name="app",
            user_id="alice",
            session_id="s",
            events=[],
            custom_metadata={"x": 1},
        )
    with pytest.raises(DenseMemAuthenticationError):
        await memory.add_session_to_memory(Session(app_name="app", user_id="bob", id="s"))
    with pytest.raises(DenseMemAuthenticationError):
        await memory.search_memory(app_name="app", user_id="bob", query="x")
    await memory.add_session_to_memory(session())
    assert state["requests"] == []


@pytest.mark.parametrize("count,sizes", [(21, [20, 1]), (40, [20, 20]), (41, [20, 20, 1])])
async def test_batches_and_growing_session_retain_keys_and_payload(wire_server, count, sizes):
    endpoint, state = wire_server
    state["result"] = terminal()
    memory = service(endpoint)
    snapshot = session([event(i) for i in range(count)])
    await memory.add_session_to_memory(snapshot)
    first = copy.deepcopy(state["calls"])
    assert [len(args["events"]) for _, args in first] == sizes
    assert [e["event_id"] for _, args in first for e in args["events"]] == [
        f"event-{i}" for i in range(count)
    ]
    await memory.add_session_to_memory(snapshot)
    assert state["calls"][len(first) :] == first
    snapshot.events.append(event(count))
    await memory.add_session_to_memory(snapshot)
    assert state["calls"][2 * len(first)][1] == first[0][1]


async def test_unknown_part_rejects_before_first_batch(wire_server):
    endpoint, state = wire_server

    class FuturePart(types.Part):
        future_content: str = "secret"

    unknown = event(20)
    unknown.content.parts = [FuturePart(text="x")]
    with pytest.raises(ValueError, match="shape"):
        await service(endpoint).add_session_to_memory(
            session([event(i) for i in range(20)] + [unknown])
        )
    assert not state["calls"]


@pytest.mark.parametrize(
    "part",
    [
        types.Part(text="x", inline_data=types.Blob(data=b"binary", mime_type="image/png")),
        types.Part(
            text="x", function_response=types.FunctionResponse(name="tool", response={"x": 1})
        ),
    ],
)
async def test_multi_kind_part_rejects_before_first_batch(wire_server, part):
    endpoint, state = wire_server
    invalid = event(20)
    invalid.content.parts = [part]
    with pytest.raises(ValueError, match="one content kind"):
        await service(endpoint).add_session_to_memory(
            session([event(i) for i in range(20)] + [invalid])
        )
    assert not state["requests"] and not state["calls"]


async def test_snapshot_precedes_provider_await_and_users_do_not_mix(wire_server):
    endpoint, state = wire_server
    state["result"] = terminal()
    entered, release = asyncio.Event(), asyncio.Event()
    resolutions = []

    async def provider(app_name, user_id):
        resolutions.append((app_name, user_id))
        entered.set()
        await release.wait()
        return "credential-" + user_id

    memory = adk.DenseMemMemoryService(endpoint, credential_provider=provider)
    snapshot = session([event()])
    task = asyncio.create_task(memory.add_session_to_memory(snapshot))
    await entered.wait()
    snapshot.events[0].content.parts[0].text = "changed"
    snapshot.user_id = "bob"
    release.set()
    await task
    assert state["calls"][0][1]["events"][0]["text"] == event().content.parts[0].text
    assert resolutions == [("app", "alice")]
    assert all(auth == b"Bearer credential-alice" for auth, _ in state["requests"])
    for user in ("bob", "carol"):
        await memory.add_events_to_memory(
            app_name="app", user_id=user, session_id="s", events=[event()]
        )
    assert resolutions == [("app", name) for name in ("alice", "bob", "carol")]


async def test_partial_failure_preserves_original_error_and_replay_request(wire_server):
    endpoint, state = wire_server
    failed = {
        "processing_state": "failed",
        "submission_id": "failed-receipt",
        "errors": [
            {
                "code": "provider_unavailable",
                "message": "unavailable",
                "retryable": True,
                "next_action": "retry_same_request",
            }
        ],
    }

    async def handler(name, arguments):
        return terminal() if len(state["calls"]) == 1 else failed

    state["handler"] = handler
    snapshot = session([event(i) for i in range(41)])
    memory = service(endpoint)
    with pytest.raises(DenseMemOperationError) as raised:
        await memory.add_session_to_memory(snapshot)
    error = raised.value
    assert len(state["calls"]) == 2
    assert error.submission_id == "failed-receipt" and error.result == failed
    assert error.completed_results == [terminal()]
    assert error.failed_request == state["calls"][1][1]
    first_attempt = copy.deepcopy(state["calls"])
    state.pop("handler")
    state["result"] = terminal()
    await memory.add_session_to_memory(snapshot)
    assert state["calls"][2:4] == first_attempt


@pytest.mark.parametrize(
    "result",
    [
        {},
        {"processing_state": "accepted"},
        {"processing_state": "completed", "search_state": "pending", "submission_id": "known"},
    ],
)
async def test_incomplete_result_is_not_success(wire_server, result):
    endpoint, state = wire_server
    state["result"] = result
    with pytest.raises(DenseMemProtocolError) as raised:
        await service(endpoint).add_session_to_memory(session([event()]))
    assert raised.value.result == result
    assert raised.value.submission_id == result.get("submission_id")
    assert len(state["calls"]) == 1


@pytest.mark.parametrize(
    "code,cls",
    [
        ("request_timeout", DenseMemTimeoutError),
        ("request_cancelled", DenseMemCancelledError),
    ],
)
async def test_server_timeout_and_cancellation_keep_receipts(wire_server, code, cls):
    endpoint, state = wire_server
    state["result"] = {
        "processing_state": "failed",
        "submission_id": "returned",
        "errors": [
            {"code": code, "message": "bounded failure"},
        ],
    }
    with pytest.raises(cls) as raised:
        await service(endpoint).add_session_to_memory(session([event()]))
    assert raised.value.submission_id == "returned"
    assert raised.value.failed_request == state["calls"][0][1]


async def test_deadline_and_cancellation_release_slots(wire_server):
    endpoint, state = wire_server
    state["result"] = terminal()
    entered = asyncio.Event()
    active, maximum = 0, 0

    async def provider(app_name, user_id):
        nonlocal active, maximum
        active += 1
        maximum = max(maximum, active)
        entered.set()
        try:
            await asyncio.Event().wait()
        finally:
            active -= 1

    memory = adk.DenseMemMemoryService(
        endpoint,
        credential_provider=provider,
        timeout=0.15,
        max_concurrency=1,
    )
    processing = asyncio.create_task(memory.add_session_to_memory(session([event()])))
    await entered.wait()
    waiting = asyncio.create_task(memory.add_session_to_memory(session([event()])))
    await asyncio.sleep(0.02)
    waiting.cancel()
    with pytest.raises(DenseMemCancelledError) as cancelled:
        await waiting
    assert cancelled.value.failed_request is None
    with pytest.raises(DenseMemTimeoutError) as timed_out:
        await processing
    assert timed_out.value.submission_id is None
    assert active == 0 and maximum == 1

    async def working(app_name, user_id):
        return "token"

    memory._provider = working
    await memory.add_session_to_memory(session([event()]))


@pytest.mark.parametrize("max_concurrency", [1, 2])
def test_concurrency_limit_is_shared_across_event_loops(wire_server, max_concurrency):
    endpoint, state = wire_server
    state["result"] = terminal()
    entered, queued, other_queued = threading.Event(), threading.Event(), threading.Event()
    release = Future()
    lock = threading.Lock()
    active, maximum = 0, 0

    async def provider(app_name, user_id):
        nonlocal active, maximum
        with lock:
            active += 1
            maximum = max(maximum, active)
            if active == max_concurrency:
                entered.set()
        try:
            await asyncio.wrap_future(release)
        finally:
            with lock:
                active -= 1
        return "token"

    memory = adk.DenseMemMemoryService(
        endpoint, credential_provider=provider, max_concurrency=max_concurrency, timeout=10
    )

    async def first_loop():
        holders = [
            asyncio.create_task(memory.add_session_to_memory(session([event()])))
            for _ in range(max_concurrency)
        ]
        assert await asyncio.to_thread(entered.wait, 3)
        waiting = asyncio.create_task(memory.add_session_to_memory(session([event()])))
        asyncio.get_running_loop().call_soon(queued.set)
        await asyncio.gather(*holders, waiting)

    async def other_loop():
        asyncio.get_running_loop().call_soon(other_queued.set)
        await memory.add_session_to_memory(session([event()]))

    with ThreadPoolExecutor(max_workers=2) as executor:
        first = executor.submit(asyncio.run, first_loop())
        try:
            assert queued.wait(3)
            other = executor.submit(asyncio.run, other_loop())
            assert other_queued.wait(3)
            with lock:
                assert maximum == max_concurrency
        finally:
            release.set_result(None)
        first.result(timeout=15)
        other.result(timeout=15)
    asyncio.run(memory.add_session_to_memory(session([event()])))
    assert len(state["calls"]) == max_concurrency + 3
    assert active == 0 and maximum == max_concurrency


@pytest.mark.parametrize("before_release", [True, False])
async def test_cancellation_at_slot_handoff_preserves_capacity(wire_server, before_release):
    endpoint, state = wire_server
    state["result"] = terminal()
    memory = service(endpoint, max_concurrency=1, timeout=3)
    async with memory._operation():
        waiting = asyncio.create_task(memory.add_session_to_memory(session([event()])))
        await asyncio.sleep(0)
        if before_release:
            waiting.cancel()
            with pytest.raises(DenseMemCancelledError) as raised:
                await waiting
    if not before_release:
        waiting.cancel()
        with pytest.raises(DenseMemCancelledError) as raised:
            await waiting
    assert raised.value.failed_request is None and not state["requests"]
    entered = asyncio.Event()

    async def probe():
        async with memory._operation():
            entered.set()

    async with memory._operation():
        pending = asyncio.create_task(probe())
        await asyncio.sleep(0)
        assert not entered.is_set()
    await pending
    assert entered.is_set()
    await memory.add_session_to_memory(session([event()]))
    assert len(state["calls"]) == 1


async def test_processing_cancellation_has_no_invented_receipt(wire_server):
    endpoint, state = wire_server
    state["delay"] = 1
    pending = asyncio.create_task(service(endpoint).add_session_to_memory(session([event()])))
    assert await asyncio.to_thread(state["called"].wait, 3)
    pending.cancel()
    with pytest.raises(DenseMemCancelledError) as raised:
        await pending
    assert raised.value.submission_id is None
    assert raised.value.failed_request == state["calls"][0][1]


async def test_deadline_covers_all_batches_together(wire_server):
    endpoint, state = wire_server
    state["result"] = terminal()

    async def provider(app_name, user_id):
        await asyncio.sleep(0.5)
        return "token"

    memory = adk.DenseMemMemoryService(endpoint, credential_provider=provider, timeout=0.8)
    with pytest.raises(DenseMemTimeoutError) as raised:
        await memory.add_session_to_memory(session([event(i) for i in range(21)]))
    assert len(raised.value.completed_results) == 1
    assert raised.value.failed_request["events"][0]["event_id"] == "event-20"
    assert len(state["calls"]) == 1


async def test_provider_failure_and_token_redaction(wire_server, caplog):
    endpoint, state = wire_server

    async def broken(app_name, user_id):
        raise RuntimeError("provider-secret")

    with pytest.raises(DenseMemAuthenticationError) as raised:
        await adk.DenseMemMemoryService(endpoint, credential_provider=broken).add_session_to_memory(
            session([event()])
        )
    assert "provider-secret" not in str(raised.value)
    state["result"] = terminal()
    with caplog.at_level(logging.DEBUG):
        await service(endpoint).add_session_to_memory(session([event()]))
    assert "static-secret" not in caplog.text


@pytest.mark.parametrize("empty", [False, True])
async def test_search_retains_provenance_order_and_empty_degradation(wire_server, empty):
    endpoint, state = wire_server
    evidence = [
        {
            "evidence_id": name,
            "context": " exact \n",
            "space_kind": kind,
            "equivalent_evidence_ids": ["equivalent"],
            "equivalents_truncated": False,
        }
        for name, kind in [("b", "private"), ("a", "team_shared")]
    ]
    result = {
        "results": [] if empty else evidence,
        "recall_id": "recall",
        "conflicts": [{"kind": "evidence_conflict"}],
        "degradations": [{"code": "optional_context_omitted"}],
    }
    state["result"] = result
    response = await service(endpoint).search_memory(app_name="app", user_id="alice", query="Ari")
    assert response.custom_metadata == {"densemem": result}
    assert [m.id for m in response.memories] == ([] if empty else ["b", "a"])
    for entry, source in zip(response.memories, evidence, strict=False):
        assert entry.content.parts[0].text == source["context"]
        assert entry.custom_metadata["densemem"] == source
        assert entry.timestamp is None and entry.author is None
    assert [name for name, _ in state["calls"]] == ["recall_memory"]
