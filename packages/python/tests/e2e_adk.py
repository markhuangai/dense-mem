import asyncio
import copy
import importlib.metadata
import json
import logging
import os
import threading
from concurrent.futures import Future
from pathlib import Path
from uuid import uuid4

from e2e import wait_for_embedding
from e2e_support import (
    SECRETS,
    TEAM,
    USER_URL,
    assert_no_credentials_in_logs,
    control,
    credential,
    literal,
    provider_health,
    sql,
    write_result,
)
from google.adk.events import Event
from google.adk.events.event_actions import EventActions
from google.adk.sessions import InMemorySessionService, Session
from google.genai import types

from densemem import (
    DenseMemAuthenticationError,
    DenseMemCancelledError,
    DenseMemClient,
    DenseMemError,
    DenseMemOperationError,
    DenseMemTimeoutError,
)
from densemem.adk import DenseMemMemoryService


def event(event_id="one", text="  Ari uses Go. café 🧭\n", author="user"):
    return Event(
        id=event_id,
        author=author,
        timestamp=1792497600,
        content=types.Content(
            role="user" if author == "user" else "model", parts=[types.Part(text=text)]
        ),
    )


def session(events, session_id=None):
    return Session(
        app_name="adk-e2e", user_id="alice", id=session_id or str(uuid4()), events=events
    )


async def rejected(call, cls=DenseMemError):
    try:
        await call
    except cls as error:
        assert all(secret not in str(error) + repr(error) for secret in SECRETS)
        return error
    raise AssertionError("expected typed adapter failure")


def intake(session_id):
    return "team_id=" + literal(TEAM) + "::uuid AND session_id=" + literal(session_id)


def count_events(session_id):
    return int(sql("SELECT count(*) FROM session_events WHERE " + intake(session_id)))


async def main():
    assert importlib.metadata.version("google-adk") == "2.7.0"
    keys = {"alice": credential(TEAM), "bob": credential(TEAM)}
    other_team = control("/teams", {"name": "ADK C " + str(uuid4())})["id"]
    keys["carol"] = credential(other_team)
    endpoint = USER_URL + "/mcp"
    memory = DenseMemMemoryService(endpoint, token=keys["alice"], bound_user_id="alice")
    sessions = InMemorySessionService()
    live = await sessions.create_session(app_name="adk-e2e", user_id="alice")
    eligible = event()
    eligible.actions = EventActions(state_delta={"not_evidence": "SecretState uses Python."})
    await sessions.append_event(session=live, event=eligible)
    for author in ("assistant", "tool", "system"):
        await sessions.append_event(session=live, event=event(author, "Foreign uses Rust.", author))
    await sessions.append_event(
        session=live,
        event=Event(
            author="user",
            actions=EventActions(state_delta={"state": "StateOnly uses Ruby."}),
        ),
    )
    await sessions.append_event(
        session=live,
        event=Event(
            author="user",
            content=types.Content(
                role="user",
                parts=[
                    types.Part(inline_data=types.Blob(data=b"binary", mime_type="image/png")),
                    types.Part(text="Hidden uses Java.", thought=True),
                    types.Part(
                        function_response=types.FunctionResponse(
                            name="tool", response={"text": "Tool uses C."}
                        )
                    ),
                ],
            ),
        ),
    )
    await memory.add_session_to_memory(live)
    assert count_events(live.id) == 1
    response = await memory.search_memory(app_name="adk-e2e", user_id="alice", query="Ari Go")
    assert response.memories
    evidence_ids = {entry.id for entry in response.memories}
    for entry in response.memories:
        assert entry.custom_metadata["densemem"]["space_kind"] == "private"
        assert entry.id == entry.custom_metadata["densemem"]["evidence_id"]
    related = response.custom_metadata["densemem"]["related_relationships"]
    assert related
    client = DenseMemClient(endpoint, token=keys["alice"])
    trace = await client.trace_memory({"relationship_id": related[0]["relationship_id"]})
    for evidence in trace["evidence"]:
        provenance = evidence["session"]
        assert provenance["framework"] == "google-adk" and provenance["session_id"] == live.id
        assert (
            evidence["content"]
            == eligible.content.parts[0].text[provenance["span_start"] : provenance["span_end"]]
        )
    before = await provider_health()
    await memory.add_session_to_memory(live)
    assert count_events(live.id) == 1 and await provider_health() == before
    changed = copy.deepcopy(live)
    changed.events[0].content.parts[0].text = "Ari uses Rust."
    await rejected(memory.add_session_to_memory(changed), DenseMemOperationError)
    assert count_events(live.id) == 1
    await rejected(
        memory.add_events_to_memory(
            app_name="adk-e2e",
            user_id="bob",
            session_id="empty",
            events=[],
        ),
        DenseMemAuthenticationError,
    )
    try:
        await memory.add_memory(app_name="adk-e2e", user_id="alice", memories=[])
    except NotImplementedError:
        pass
    else:
        raise AssertionError("direct add must be unsupported")
    resolutions = []

    async def provider(app_name, user_id):
        resolutions.append((app_name, user_id))
        return keys[user_id]

    multi = DenseMemMemoryService(endpoint, credential_provider=provider)
    foreign_ids = set()
    for user in ("bob", "carol"):
        foreign = await multi.search_memory(app_name="adk-e2e", user_id=user, query="Ari Go")
        assert not evidence_ids & {entry.id for entry in foreign.memories}
        await multi.add_events_to_memory(
            app_name="adk-e2e",
            user_id=user,
            session_id="same-external-session",
            events=[event(text="Blair uses Rust." if user == "bob" else "Casey uses Python.")],
        )
        owned = await multi.search_memory(
            app_name="adk-e2e", user_id=user, query="Blair Casey Rust Python"
        )
        assert owned.memories
        foreign_ids.update(entry.id for entry in owned.memories)
    alice = await multi.search_memory(
        app_name="adk-e2e", user_id="alice", query="Blair Casey Rust Python"
    )
    assert not foreign_ids & {entry.id for entry in alice.memories}
    assert {user for _, user in resolutions} == {"alice", "bob", "carol"}
    await reject_multi_kind_parts(memory)
    await cross_loop_memory(endpoint, keys["alice"], evidence_ids)
    await batching(memory)
    await timeout_and_cancellation(memory, keys["alice"])
    assert_no_credentials_in_logs()
    receipt = {
        "status": "ok",
        "scenario": "google_adk_memory",
        "tested_commit": os.environ["DENSE_MEM_E2E_COMMIT_SHA"],
        "adk": "2.7.0",
        "installed_wheel": True,
        "user_text_only": True,
        "private_isolation_actors": 3,
        "exact_provenance": True,
        "duplicate_noop_and_changed_conflict": True,
        "sequential_batches_and_partial_recovery": True,
        "direct_terminal_timeout_and_cancellation": True,
        "multi_kind_preflight_rejection": True,
        "cross_loop_concurrency_and_waiter_cancellation": True,
        "credential_redaction": True,
        "mapping": json.loads(await asyncio.to_thread(
            Path(os.environ["DENSEMEM_ADK_MAPPING_REPORT"]).read_text
        )),
    }
    write_result(receipt)
    print(json.dumps(receipt))


async def reject_multi_kind_parts(memory):
    for part in (
        types.Part(text="x", inline_data=types.Blob(data=b"binary", mime_type="image/png")),
        types.Part(
            text="x", function_response=types.FunctionResponse(name="tool", response={"x": 1})
        ),
    ):
        invalid = event("invalid")
        invalid.content.parts = [part]
        snapshot = session([event(str(i)) for i in range(20)] + [invalid])
        before = await provider_health()
        try:
            await memory.add_session_to_memory(snapshot)
        except ValueError as error:
            assert "one content kind" in str(error)
        else:
            raise AssertionError("multi-kind ADK part must be rejected before ingestion")
        assert count_events(snapshot.id) == 0 and await provider_health() == before


async def cross_loop_memory(endpoint, token, evidence_ids):
    entered, queued = threading.Event(), threading.Event()
    release = Future()
    lock = threading.Lock()
    active, maximum = 0, 0

    async def provider(app_name, user_id):
        nonlocal active, maximum
        with lock:
            active += 1
            maximum = max(maximum, active)
            entered.set()
        try:
            await asyncio.wrap_future(release)
        finally:
            with lock:
                active -= 1
        return token

    memory = DenseMemMemoryService(endpoint, credential_provider=provider, max_concurrency=1)

    async def search():
        return await memory.search_memory(app_name="adk-e2e", user_id="alice", query="Ari Go")

    async def first_loop():
        first = asyncio.create_task(search())
        assert await asyncio.to_thread(entered.wait, 5)
        second = asyncio.create_task(search())
        asyncio.get_running_loop().call_soon(queued.set)
        return await asyncio.gather(first, second)

    calls = [asyncio.create_task(asyncio.to_thread(asyncio.run, first_loop()))]
    try:
        assert await asyncio.to_thread(queued.wait, 5)
        waiting = asyncio.create_task(search())
        await asyncio.sleep(0)
        waiting.cancel()
        error = await rejected(waiting, DenseMemCancelledError)
        assert error.submission_id is None
        calls.append(asyncio.create_task(search()))
        await asyncio.sleep(0)
        with lock:
            assert maximum == 1
    finally:
        release.set_result(None)
        results = await asyncio.gather(*calls)
    assert active == 0 and maximum == 1
    for response in [*results[0], results[1]]:
        assert evidence_ids <= {entry.id for entry in response.memories}


async def batching(memory):
    for count in (21, 40, 41):
        snapshot = session([event(str(i), "Thanks. Let's continue.") for i in range(count)])
        await memory.add_session_to_memory(snapshot)
        assert count_events(snapshot.id) == count
        await memory.add_session_to_memory(snapshot)
        assert count_events(snapshot.id) == count
        snapshot.events.append(event("new", "Thanks. Let's continue."))
        await memory.add_session_to_memory(snapshot)
        assert count_events(snapshot.id) == count + 1
    partial = session(
        [event(str(i), "Thanks. Let's continue.") for i in range(20)]
        + [
            event("late", "Taylor uses Ruby. [fixture-fault:embedding-session-cancel]"),
        ]
    )
    error = await rejected(memory.add_session_to_memory(partial), DenseMemTimeoutError)
    assert len(error.completed_results) == 1 and error.submission_id
    assert error.failed_request["events"][0]["event_id"] == "late"
    assert count_events(partial.id) == 21
    failed_id = error.submission_id
    assert (
        sql(
            "SELECT count(*) FROM evidence_fragments WHERE ingest_id="
            + literal(failed_id)
            + "::uuid"
        )
        == "0"
    )
    await memory.add_session_to_memory(partial)
    assert (
        sql(
            "SELECT result->>'processing_state' FROM session_submissions WHERE submission_id="
            + literal(failed_id)
            + "::uuid"
        )
        == "completed"
    )
    assert count_events(partial.id) == 21


async def timeout_and_cancellation(memory, token):
    timeout = session([event(text="Taylor uses Ruby. [fixture-fault:embedding-only-timeout]")])
    error = await rejected(memory.add_session_to_memory(timeout), DenseMemTimeoutError)
    assert error.submission_id and error.result["processing_state"] == "failed"
    short = DenseMemMemoryService(
        USER_URL + "/mcp", token=token, bound_user_id="alice", timeout=0.01
    )
    error = await rejected(short.add_session_to_memory(session([event()])), DenseMemTimeoutError)
    assert error.submission_id is None
    before = (await provider_health())["embedding_calls"]
    pending = asyncio.create_task(
        memory.add_session_to_memory(
            session(
                [
                    event(text="Taylor uses Ruby. [fixture-fault:embedding-cancel]"),
                ]
            )
        )
    )
    await wait_for_embedding(before)
    pending.cancel()
    cancelled = await rejected(pending, DenseMemCancelledError)
    assert cancelled.submission_id is None and cancelled.failed_request is not None
    await memory.add_session_to_memory(session([event(text="Thanks. Let's continue.")]))


if __name__ == "__main__":
    logging.basicConfig(level=logging.DEBUG)
    asyncio.run(main())
