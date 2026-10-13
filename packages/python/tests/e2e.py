import asyncio
import copy
import importlib.util
import json
import logging
import os
from pathlib import Path
from uuid import uuid4

from e2e_support import (
    SECRETS,
    TEAM,
    USER_URL,
    assert_no_credentials_in_logs,
    control,
    credential,
    literal,
    oauth_credentials,
    provider_health,
    raw_call,
    sql,
    write_result,
)

from densemem import (
    DenseMemAuthenticationError,
    DenseMemCancelledError,
    DenseMemClient,
    DenseMemError,
    DenseMemOperationError,
    DenseMemProtocolError,
    DenseMemTimeoutError,
    SyncDenseMemClient,
)


def example(name):
    path = Path(os.environ["DENSEMEM_E2E_EXAMPLES"]) / (name + ".py")
    spec = importlib.util.spec_from_file_location("example_" + name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def relationships(result):
    return [
        split["relationship_id"]
        for item in result["relationship_results"]
        for split in item["splits"]
    ]


async def rejected(call, cls=DenseMemError):
    try:
        await call
    except cls as error:
        assert all(token not in str(error) + repr(error) for token in SECRETS)
        return error
    raise AssertionError("expected typed Dense-Mem failure")


def no_canonical_write(key):
    assert (
        sql(
            "SELECT count(*) FROM evidence_fragments e JOIN knowledge_ingests i "
            "ON i.team_id=e.team_id AND i.ingest_id=e.ingest_id WHERE i.team_id="
            + literal(TEAM)
            + "::uuid "
            "AND i.idempotency_key=" + literal(key) + ";"
        )
        == "0"
    )


async def main():
    token_a, token_b = credential(TEAM), credential(TEAM)
    team_c = control("/teams", {"name": "Python C " + str(uuid4())})["id"]
    token_c = credential(team_c)
    shared = credential(TEAM, "shared_only")
    readonly = credential(TEAM, scopes=["read"])
    client = DenseMemClient(USER_URL + "/mcp", token=token_a)
    assert (await client.initialize()).serverInfo.name
    tools = {tool.name for tool in (await client.list_tools()).tools}
    assert {"remember", "ingest_session", "recall_memory", "trace_memory"} <= tools
    assert "get_submission_status" not in tools
    for token in (shared, readonly):
        hidden = DenseMemClient(USER_URL + "/mcp", token=token)
        assert "ingest_session" not in {t.name for t in (await hidden.list_tools()).tools}
        await rejected(hidden.ingest_session(example("session").make_request(str(uuid4()))))
    await rejected(
        DenseMemClient(USER_URL + "/mcp", token="invalid-bearer").initialize(),
        DenseMemAuthenticationError,
    )
    await rejected(
        DenseMemClient(USER_URL + f"/teams/{team_c}/mcp", token=token_a).initialize(),
        DenseMemAuthenticationError,
    )
    result = await example("remember").run(client, str(uuid4()))
    assert result["processing_state"] == "completed" and relationships(result)
    request = example("remember").make_request(str(uuid4()))
    original = copy.deepcopy(request)
    retained = await client.remember(request)
    assert request == original
    assert await raw_call("remember", request, token_a) == retained
    changed = copy.deepcopy(request)
    changed["evidence"][0]["metadata"]["revision"] = "changed"
    conflict = await rejected(client.remember(changed), DenseMemOperationError)
    assert conflict.code == "idempotency_conflict"
    assert await client.remember(request) == retained
    missing = {"idempotency_key": str(uuid4()), "evidence": [{"content": "Ari uses Go."}]}
    await rejected(client.remember(missing), DenseMemProtocolError)
    no_canonical_write(missing["idempotency_key"])
    session_request = example("session").make_request(str(uuid4()))
    session_result = await example("session").run(client, session_request["idempotency_key"])
    assert relationships(session_result) and session_result["search_state"] == "current"
    trace = await client.trace_memory({"relationship_id": relationships(session_result)[0]})
    for evidence in trace["evidence"]:
        provenance = evidence["session"]
        event = session_request["events"][0]
        assert (
            evidence["content"] == event["text"][provenance["span_start"] : provenance["span_end"]]
        )
        assert provenance["user_id"] == session_request["user_id"]
        assert provenance["occurred_at"] == event["occurred_at"]
    for foreign in (token_b, token_c):
        other = DenseMemClient(USER_URL + "/mcp", token=foreign)
        recall = await other.recall_memory({"query": "Ari Go", "limit": 20, "community_limit": 0})
        assert not set(relationships(session_result)) & {
            item["relationship_id"] for item in recall["related_relationships"]
        }
        await rejected(other.trace_memory({"relationship_id": relationships(session_result)[0]}))
    changed = copy.deepcopy(session_request)
    changed["idempotency_key"] = str(uuid4())
    changed["events"][0]["text"] = "Ari uses Rust."
    error = await rejected(client.ingest_session(changed), DenseMemOperationError)
    assert error.reason_code == "session_event_conflict"
    empty = example("session").make_request(str(uuid4()))
    empty["session_id"] = str(uuid4())
    empty["events"][0]["text"] = "Thanks. Let's continue."
    empty_result = await client.ingest_session(empty)
    assert empty_result["search_state"] == "not_required" and not relationships(empty_result)
    failed = example("session").make_request(str(uuid4()))
    failed["session_id"] = str(uuid4())
    failed["events"][0]["text"] = "Ari uses Java. [fixture-fault:assessment-malformed]"
    error = await rejected(client.ingest_session(failed), DenseMemOperationError)
    assert error.submission_id and error.result["events"][0]["evidence_ids"] == []
    assert error.code == "provider_response_invalid" and error.next_action == "retry_same_request"
    overlapping = copy.deepcopy(failed)
    overlapping["idempotency_key"] = str(uuid4())
    overlap = await rejected(client.ingest_session(overlapping), DenseMemOperationError)
    assert overlap.reason_code == "session_event_retry_required"
    await session_recovery(client)
    mixed = example("remember").make_request(str(uuid4()))
    suffix = str(uuid4())
    subject, database, annotation = (
        "Dense-Mem Mixed " + suffix,
        "PostgreSQL Mixed " + suffix,
        "annotation Mixed " + suffix,
    )
    mixed["evidence"] = [
        {
            "content": f"{subject} stores its durable memory in {database}. "
            "[fixture:mixed-a] [fixture-fault:mixed]"
        },
        {
            "content": f"The unsupported {annotation} is not a durable claim by {subject}. "
            "[fixture:mixed-b] [fixture-fault:mixed]"
        },
    ]
    proposal = mixed["relationships"][0]
    proposal["ref"], proposal["subject"]["name"] = "supported", subject
    proposal["object"]["entity"]["name"] = database
    proposal["evidence_indices"] = [0, 1]
    unsupported = copy.deepcopy(proposal)
    unsupported["ref"] = "unsupported"
    unsupported["object"] = {"entity": {"name": annotation, "entity_kind": "concept"}}
    unsupported["evidence_indices"] = [1]
    mixed["relationships"].append(unsupported)
    mixed_result = await client.remember(mixed)
    assert [item["disposition"] for item in mixed_result["relationship_results"]] == [
        "stored",
        "not_stored",
    ]
    assert mixed_result["relationship_results"][1]["remediation"]
    await cancellation_and_timeout(client)
    oauth, profile_key, oauth_owner, credential_owner = oauth_credentials()
    count = 0

    async def provider():
        nonlocal count
        count += 1
        return oauth

    signed = DenseMemClient(USER_URL + f"/teams/{TEAM}/mcp", token_provider=provider)
    assert "ingest_session" in {t.name for t in (await signed.list_tools()).tools}
    assert count == 1
    signed_result = await example("remember").run(signed, str(uuid4()))
    assert signed_result["processing_state"] == "completed"
    oauth_session = example("session").make_request(str(uuid4()))
    oauth_session["session_id"] = str(uuid4())
    oauth_private = await signed.ingest_session(oauth_session)
    assert oauth_private["processing_state"] == "completed" and relationships(oauth_private)
    assert await signed.ingest_session(oauth_session) == oauth_private
    profile = DenseMemClient(USER_URL + "/mcp", token=profile_key)
    assert "ingest_session" in {t.name for t in (await profile.list_tools()).tools}
    profile_session = example("session").make_request(str(uuid4()))
    profile_session["session_id"] = str(uuid4())
    profile_private = await profile.ingest_session(profile_session)
    assert profile_private["processing_state"] == "completed" and relationships(profile_private)
    assert await profile.ingest_session(profile_session) == profile_private
    for receipt, owner in ((oauth_private, oauth_owner), (profile_private, credential_owner)):
        assert (
            sql(
                "SELECT owner_profile_id::text FROM session_submissions WHERE submission_id="
                + literal(receipt["submission_id"])
                + "::uuid;"
            )
            == owner
        )
        for foreign in (token_b, token_c):
            await rejected(
                DenseMemClient(USER_URL + "/mcp", token=foreign).trace_memory(
                    {"relationship_id": relationships(receipt)[0]}
                )
            )
    await rejected(
        DenseMemClient(USER_URL + "/mcp", token=oauth + ".bad").initialize(),
        DenseMemAuthenticationError,
    )
    for request in ({"query": "Ari Go", "limit": 5, "community_limit": 1},):
        mapped = await client.recall_memory(request)
        raw = await raw_call("recall_memory", request, token_a)
        for name in (
            "results",
            "related_relationships",
            "related_communities",
            "conflicts",
            "search_states",
            "degradations",
        ):
            assert mapped[name] == raw[name], "client changed recall field " + name
    sync = SyncDenseMemClient(USER_URL + "/mcp", token=token_a)
    assert (await asyncio.to_thread(sync.initialize)).serverInfo.name
    assert await asyncio.to_thread(
        sync.remember, request := example("remember").make_request(str(uuid4()))
    ) == await client.remember(request)
    assert_no_credentials_in_logs()
    receipt = {
        "status": "ok",
        "scenario": "python_client",
        "tested_commit": os.environ["DENSE_MEM_E2E_COMMIT_SHA"],
        "installed_wheel": True,
        "static_and_signed_oauth": True,
        "profile_bound_api_credential": True,
        "semantic_owner_aliases_preserved": True,
        "private_isolation_actors": 3,
        "terminal_results": True,
        "unchanged_replay": True,
        "unfinished_session_recovery": True,
        "exact_provenance": True,
        "cancellation_and_timeout": True,
        "credential_redaction": True,
        "fidelity": True,
    }
    write_result(receipt)
    print(json.dumps(receipt))


async def wait_for_embedding(before):
    async with asyncio.timeout(20):
        for _ in range(200):
            if (await provider_health())["embedding_calls"] > before:
                return
            await asyncio.sleep(0.1)
    raise AssertionError("Controlled embedding phase was not reached")


async def wait_for_failure(query):
    async with asyncio.timeout(20):
        for _ in range(200):
            value = sql(query)
            if value:
                receipt = json.loads(value)
                assert receipt["processing_state"] == "failed"
                assert receipt["errors"][0]["code"] == "request_timeout", receipt["errors"]
                assert receipt["errors"][0]["retryable"] is True
                assert receipt["errors"][0]["next_action"] == "retry_same_request"
                return receipt
            await asyncio.sleep(0.1)
    raise AssertionError("Controlled provider deadline did not produce a failed receipt")


async def session_recovery(client):
    request = example("session").make_request(str(uuid4()))
    request["session_id"] = str(uuid4())
    request["events"][0]["text"] += " [fixture-fault:embedding-session-cancel]"
    before = (await provider_health())["embedding_calls"]
    pending = asyncio.create_task(client.ingest_session(request))
    await wait_for_embedding(before)
    pending.cancel()
    error = await rejected(pending, DenseMemCancelledError)
    assert error.submission_id is None
    selector = (
        "team_id="
        + literal(TEAM)
        + "::uuid AND idempotency_key="
        + literal(request["idempotency_key"])
    )
    failed = await wait_for_failure(
        "SELECT result FROM session_submissions WHERE "
        + selector
        + " AND result->>'processing_state'='failed'"
    )
    submission = sql("SELECT submission_id FROM session_submissions WHERE " + selector)
    assert failed["submission_id"] == submission
    evidence_count = (
        "SELECT count(*) FROM evidence_fragments WHERE team_id="
        + literal(TEAM)
        + "::uuid AND ingest_id="
        + literal(submission)
        + "::uuid"
    )
    assert sql(evidence_count) == "0"
    overlapping = copy.deepcopy(request)
    overlapping["idempotency_key"] = str(uuid4())
    conflict = await rejected(client.ingest_session(overlapping), DenseMemOperationError)
    assert conflict.reason_code == "session_event_retry_required"
    recovered = await client.ingest_session(request)
    assert recovered["processing_state"] == "completed" and relationships(recovered)
    assert recovered["submission_id"] == submission
    assert recovered["accepted_event_count"] == 1 and recovered["duplicate_event_count"] == 0
    count = len(recovered["events"][0]["evidence_ids"])
    assert count > 0 and sql(evidence_count) == str(count)
    assert await client.ingest_session(request) == recovered
    assert sql(evidence_count) == str(count)
    assert sql("SELECT count(*) FROM session_submissions WHERE " + selector) == "1"
    assert (
        sql(
            "SELECT count(*) FROM session_events WHERE team_id="
            + literal(TEAM)
            + "::uuid AND submission_id="
            + literal(submission)
            + "::uuid"
        )
        == "1"
    )


async def cancellation_and_timeout(client):
    request = example("remember").make_request(str(uuid4()))
    request["evidence"][0]["content"] += " [fixture-fault:embedding-cancel]"
    before = (await provider_health())["embedding_calls"]
    pending = asyncio.create_task(client.remember(request))
    await wait_for_embedding(before)
    pending.cancel()
    cancelled = await rejected(pending, DenseMemCancelledError)
    assert cancelled.submission_id is None
    await wait_for_failure(
        "SELECT public_result FROM remember_attempts WHERE team_id="
        + literal(TEAM)
        + "::uuid AND idempotency_key="
        + literal(request["idempotency_key"])
        + " AND outcome='failed' ORDER BY created_at DESC LIMIT 1"
    )
    no_canonical_write(request["idempotency_key"])
    assert (await client.remember(request))["processing_state"] == "completed"
    timed = example("remember").make_request(str(uuid4()))
    timed["evidence"][0]["content"] += " [fixture-fault:embedding-only-timeout]"
    short = DenseMemClient(USER_URL + "/mcp", token=SECRETS[1], timeout=5)
    error = await rejected(short.remember(timed), DenseMemTimeoutError)
    assert error.submission_id is None
    no_canonical_write(timed["idempotency_key"])
    server_error = await rejected(client.remember(timed), DenseMemTimeoutError)
    assert server_error.submission_id and server_error.code == "request_timeout"


if __name__ == "__main__":
    logging.basicConfig(level=logging.DEBUG)
    asyncio.run(main())
