import asyncio
import importlib.metadata
import json
import logging
import os
import warnings
from uuid import uuid4

from e2e import example, rejected, wait_for_embedding
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
from langchain_core.callbacks import BaseCallbackHandler
from langchain_core.documents import Document
from langchain_core.retrievers import BaseRetriever

from densemem import (
    DenseMemAuthenticationError,
    DenseMemCancelledError,
    DenseMemClient,
    DenseMemTimeoutError,
)
from densemem.langchain import DenseMemRecallWarning, DenseMemRetriever

ENDPOINT = USER_URL + "/mcp"
QUERY = "Dense-Mem durable memory PostgreSQL"
OPTIONS = {"community_limit": 0, "relationship_limit": 20}


class Callbacks(BaseCallbackHandler):
    def __init__(self):
        self.events = []

    def on_retriever_start(self, serialized, query, **kwargs):
        self.events.append("start")

    def on_retriever_end(self, documents, **kwargs):
        self.events.append("end")

    def on_retriever_error(self, error, **kwargs):
        self.events.append("error")


async def remember(token, space):
    request = example("remember").make_request(str(uuid4()))
    request["target_space"] = space
    result = await DenseMemClient(ENDPOINT, token=token).remember(request)
    assert result["processing_state"] == "completed" and result["space_kind"] == space
    assert all(item["disposition"] == "stored" for item in result["relationship_results"])
    return result["evidence"][0]["evidence_id"]


def semantic_snapshot():
    tables = (
        "knowledge_ingests",
        "evidence_fragments",
        "entity_records",
        "relationship_records",
        "relationship_observations",
        "relationship_evidence_supports",
        "relationship_transition_events",
    )
    return {
        table: sql(
            "SELECT md5(COALESCE(string_agg(row_to_json(t)::text, '' "
            "ORDER BY row_to_json(t)::text), '')) FROM " + table + " t"
        )
        for table in tables
    }


def assert_fidelity(documents, raw, team):
    assert documents and all(isinstance(document, Document) for document in documents)
    assert [document.id for document in documents] == [
        item["evidence_id"] for item in raw["results"]
    ]
    assert [document.page_content for document in documents] == [
        item["context"] for item in raw["results"]
    ]
    recall_ids = set()
    for document, evidence in zip(documents, raw["results"], strict=True):
        metadata = document.metadata["densemem"]
        for key in (
            "evidence_id",
            "space_kind",
            "equivalent_evidence_ids",
            "equivalents_truncated",
        ):
            assert metadata[key] == evidence[key]
        for key in (
            "related_relationships",
            "related_communities",
            "conflicts",
            "search_states",
            "degradations",
        ):
            assert metadata[key] == raw[key]
        assert not {"rank", "score", "source", "relationship_ids"} & metadata.keys()
        recall_ids.add(metadata["recall_id"])
        for action in metadata["suggested_actions"]:
            if "recall_event_id" in action:
                assert action["recall_event_id"] == metadata["recall_id"]
    assert len(recall_ids) == 1
    recall_id = recall_ids.pop()
    assert recall_id and recall_id != raw["recall_id"]
    assert (
        sql(
            "SELECT count(*) FROM recall_feedback_events WHERE recall_id="
            + literal(recall_id)
            + " AND team_id="
            + literal(team)
            + "::uuid"
        )
        == "1"
    )


async def faults(token, foreign_team, all_visible):
    invalid = DenseMemRetriever(ENDPOINT, token="invalid-bearer")
    await rejected(invalid.ainvoke(QUERY), DenseMemAuthenticationError)
    foreign_endpoint = USER_URL + f"/teams/{foreign_team}/mcp"
    wrong_team = DenseMemRetriever(foreign_endpoint, token=token)
    mapped = await rejected(wrong_team.ainvoke(QUERY), DenseMemAuthenticationError)
    direct = await rejected(
        DenseMemClient(foreign_endpoint, token=token).recall_memory({"query": QUERY}),
        DenseMemAuthenticationError,
    )
    assert type(mapped) is type(direct)
    assert (mapped.code, mapped.rpc_code, mapped.status_code) == (
        direct.code,
        direct.rpc_code,
        direct.status_code,
    )
    for k in (0, 51, True):
        try:
            DenseMemRetriever(ENDPOINT, token=token, k=k)
        except ValueError:
            pass
        else:
            raise AssertionError("unbounded k was accepted")
    for option in ("team_id", "space_id", "limit", "query"):
        try:
            DenseMemRetriever(ENDPOINT, token=token, recall_options={option: "override"})
        except ValueError:
            pass
        else:
            raise AssertionError("recall override was accepted")
    with warnings.catch_warnings(record=True) as observed:
        warnings.simplefilter("always", DenseMemRecallWarning)
        empty = await DenseMemRetriever(
            ENDPOINT,
            token=token,
            recall_options={**OPTIONS, "known_evidence_ids": sorted(all_visible)},
        ).ainvoke("zzzznotfound [fixture-fault:embedding-unavailable]")
    assert not empty
    emitted = [item.message for item in observed if isinstance(item.message, DenseMemRecallWarning)]
    assert emitted and emitted[0].result["degradations"] and not emitted[0].result["results"]
    assert "zzzznotfound" not in str(emitted[0])
    retriever = DenseMemRetriever(ENDPOINT, token=token)
    await retriever.ainvoke(QUERY)
    # Older MCP cancellation leaves server attempts running, so inject stalls after other recalls.
    callback = Callbacks()
    short = DenseMemRetriever(ENDPOINT, token=token, timeout=1)
    await rejected(
        short.ainvoke("[fixture-fault:embedding-only-timeout]", {"callbacks": [callback]}),
        DenseMemTimeoutError,
    )
    assert callback.events == ["start", "error"]
    before = (await provider_health())["embedding_calls"]
    callback = Callbacks()
    pending = asyncio.create_task(
        retriever.ainvoke(
            "[fixture-fault:embedding-only-timeout]",
            {"callbacks": [callback]},
        )
    )
    await wait_for_embedding(before)
    pending.cancel()
    await rejected(pending, DenseMemCancelledError)
    assert "end" not in callback.events


async def main():
    assert importlib.metadata.version("langchain-core") == "1.5.4"
    assert "/site-packages/" in __import__("densemem").__file__
    control(
        "/config/recall-feedback",
        {"items": [{"key": "RECALL_FEEDBACK_ENABLED", "value": "true"}]},
        method="PATCH",
    )
    token_a = credential(TEAM)
    token_b = credential(TEAM, scopes=["read"])
    team_c = control("/teams", {"name": "LangChain C " + str(uuid4())})["id"]
    token_c = credential(team_c)
    oauth_token, _, _, _ = oauth_credentials()
    private_a = await remember(token_a, "private")
    shared_a = await remember(token_a, "team_shared")
    private_b = await remember(token_b, "private")
    shared_c = await remember(token_c, "team_shared")
    private_oauth = await remember(oauth_token, "private")
    baseline = semantic_snapshot()
    retriever = DenseMemRetriever(ENDPOINT, token=token_a, k=20, recall_options=OPTIONS)
    assert isinstance(retriever, BaseRetriever)
    serialized = repr(retriever) + retriever.model_dump_json() + json.dumps(retriever.to_json())
    assert all(secret not in serialized for secret in SECRETS)
    raw = await raw_call("recall_memory", {**OPTIONS, "query": QUERY, "limit": 20}, token_a)
    callback = Callbacks()
    sync = await asyncio.to_thread(retriever.invoke, QUERY, {"callbacks": [callback]})
    assert callback.events == ["start", "end"]
    assert_fidelity(sync, raw, TEAM)
    assert_fidelity(await retriever.ainvoke(QUERY), raw, TEAM)
    assert {document.id for document in sync} == {private_a, shared_a}
    assert len(sync) == 2 and sync[0].page_content == sync[1].page_content
    assert {document.metadata["densemem"]["space_kind"] for document in sync} == {
        "private",
        "team_shared",
    }
    for token, team, expected, forbidden in (
        (token_b, TEAM, {private_b, shared_a}, {private_a, shared_c, private_oauth}),
        (token_c, team_c, {shared_c}, {private_a, private_b, shared_a, private_oauth}),
    ):
        direct = await raw_call("recall_memory", {**OPTIONS, "query": QUERY, "limit": 20}, token)
        documents = await DenseMemRetriever(
            ENDPOINT, token=token, k=20, recall_options=OPTIONS
        ).ainvoke(QUERY)
        assert_fidelity(documents, direct, team)
        assert {document.id for document in documents} == expected
        assert not forbidden & {document.id for document in documents}
        for document in documents:
            for relationship in document.metadata["densemem"]["related_relationships"]:
                assert not forbidden & set(relationship["evidence_ids"])
    supplied, resolutions = [oauth_token], []

    async def provider():
        resolutions.append(True)
        return supplied[0]

    oauth = DenseMemRetriever(ENDPOINT, token_provider=provider, k=20, recall_options=OPTIONS)
    direct = await raw_call("recall_memory", {**OPTIONS, "query": QUERY, "limit": 20}, oauth_token)
    documents = await oauth.ainvoke(QUERY)
    assert_fidelity(documents, direct, TEAM)
    assert {document.id for document in documents} == {private_oauth, shared_a}
    assert len(resolutions) == 1
    supplied[0] = token_a
    documents = await oauth.ainvoke(QUERY)
    assert {document.id for document in documents} == {private_a, shared_a} and len(
        resolutions
    ) == 2
    for k in (1, 50):
        options = {**OPTIONS, "known_evidence_ids": [shared_a]}
        direct = await raw_call("recall_memory", {**options, "query": QUERY, "limit": k}, token_a)
        bounded = await DenseMemRetriever(
            ENDPOINT, token=token_a, k=k, recall_options=options
        ).ainvoke(QUERY)
        assert_fidelity(bounded, direct, TEAM)
        assert len(bounded) <= k and shared_a not in {document.id for document in bounded}
    batches = await retriever.abatch([QUERY, QUERY], {"max_concurrency": 2})
    assert all({document.id for document in batch} == {private_a, shared_a} for batch in batches)
    assert (
        batches[0][0].metadata["densemem"]["recall_id"]
        != batches[1][0].metadata["densemem"]["recall_id"]
    )
    await faults(token_a, team_c, {private_a, shared_a})
    assert semantic_snapshot() == baseline, "retrieval changed canonical knowledge"
    assert_no_credentials_in_logs()
    receipt = {
        "status": "ok",
        "scenario": "langchain_retriever",
        "tested_commit": os.environ["DENSE_MEM_E2E_COMMIT_SHA"],
        "langchain-core": "1.5.4",
        "installed_wheel": True,
        "sync_and_native_async": True,
        "exact_order_content_and_public_metadata": True,
        "per_call_recall_ids": True,
        "cross_space_duplicate_preservation": True,
        "private_isolation_actors": 3,
        "static_and_signed_oauth": True,
        "rotation_and_concurrent_calls": True,
        "k_and_options": True,
        "typed_auth_scope_timeout_cancellation": True,
        "empty_degradation_warning": True,
        "canonical_knowledge_unchanged": True,
        "credential_redaction": True,
    }
    write_result(receipt)
    print(json.dumps(receipt))


if __name__ == "__main__":
    logging.basicConfig(level=logging.DEBUG)
    asyncio.run(main())
