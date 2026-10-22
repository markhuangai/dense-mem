import asyncio
import copy
import json
import logging
import threading

import pytest

pytest.importorskip("langchain_core")

from langchain_core.callbacks import BaseCallbackHandler
from langchain_core.documents import Document
from langchain_core.retrievers import BaseRetriever

from densemem import (
    DenseMemAuthenticationError,
    DenseMemCancelledError,
    DenseMemOperationError,
    DenseMemProtocolError,
    DenseMemTimeoutError,
)
from densemem.langchain import DenseMemRecallWarning, DenseMemRetriever


@pytest.fixture
def recall():
    return {
        "recall_id": "recall-1",
        "results": [
            {
                "evidence_id": "private-1",
                "context": "  Ari uses Go. café 🧭\n",
                "space_kind": "private",
                "equivalent_evidence_ids": [],
                "equivalents_truncated": False,
            },
            {
                "evidence_id": "shared-1",
                "context": "  Ari uses Go. café 🧭\n",
                "space_kind": "team_shared",
                "equivalent_evidence_ids": ["shared-alternate"],
                "equivalents_truncated": True,
            },
        ],
        "related_relationships": [
            {
                "relationship_id": "relationship-1",
                "equivalent_relationship_ids": ["alternate"],
                "evidence_ids": ["shared-1"],
                "space_kind": "team_shared",
            },
        ],
        "related_communities": [{"community_id": "community-1", "rank": 2}],
        "conflicts": [{"conflict_id": "conflict-1", "status": "open"}],
        "search_states": {"evidence": "current", "relationships": "pending"},
        "degradations": [],
        "suggested_actions": [{"tool": "submit_recall_session_feedback", "guidance": "optional"}],
    }


class Callbacks(BaseCallbackHandler):
    def __init__(self):
        self.events = []

    def on_retriever_start(self, serialized, query, **kwargs):
        self.events.append(("start", serialized, query, kwargs))

    def on_retriever_end(self, documents, **kwargs):
        self.events.append(("end", documents, kwargs))

    def on_retriever_error(self, error, **kwargs):
        self.events.append(("error", error, kwargs))


def assert_mapping(documents, result):
    assert all(isinstance(document, Document) for document in documents)
    assert [document.id for document in documents] == ["private-1", "shared-1"]
    assert [document.page_content for document in documents] == [
        "  Ari uses Go. café 🧭\n",
        "  Ari uses Go. café 🧭\n",
    ]
    for document, evidence in zip(documents, result["results"], strict=True):
        metadata = document.metadata["densemem"]
        assert metadata["evidence_id"] == evidence["evidence_id"]
        assert metadata["equivalent_evidence_ids"] == evidence["equivalent_evidence_ids"]
        assert metadata["equivalents_truncated"] is evidence["equivalents_truncated"]
        assert metadata["space_kind"] == evidence["space_kind"]
        assert metadata["recall_id"] == "recall-1"
        for key in (
            "related_relationships",
            "related_communities",
            "conflicts",
            "search_states",
            "degradations",
            "suggested_actions",
        ):
            assert metadata[key] == result[key]
        assert not {"source", "score", "rank", "relationship_ids"} & metadata.keys()
    documents[0].metadata["densemem"]["related_relationships"].clear()
    assert documents[1].metadata["densemem"]["related_relationships"]
    assert result["related_relationships"]


def test_sync_real_interface_order_metadata_callbacks_and_read_only(wire_server, recall):
    endpoint, state = wire_server
    state["result"] = recall
    callback = Callbacks()
    retriever = DenseMemRetriever(endpoint, token="private-credential", tags=["densemem-test"])
    assert isinstance(retriever, BaseRetriever) and retriever.k == 10
    documents = retriever.invoke("  Ari Go? 🧭\n", config={"callbacks": [callback]})
    assert_mapping(documents, recall)
    assert state["calls"] == [("recall_memory", {"query": "  Ari Go? 🧭\n", "limit": 10})]
    assert [item[0] for item in callback.events] == ["start", "end"]
    assert "densemem-test" in callback.events[0][3]["tags"]
    assert "private-credential" not in repr(callback.events)
    assert not hasattr(retriever, "remember") and not hasattr(retriever, "add_documents")


async def test_native_async_auth_rotation_and_options_snapshot(wire_server, recall):
    endpoint, state = wire_server
    state["result"] = recall
    options = {
        "relationship_limit": 0,
        "community_limit": 0,
        "community_relationship_limit": 1,
        "known_at": None,
        "valid_at": "2026-10-22T00:00:00Z",
        "known_evidence_ids": ["known"],
        "known_relationship_ids": [],
        "expand_from_entity_ids": [],
    }
    snapshot = copy.deepcopy(options)
    resolutions = []

    async def provider():
        resolutions.append(len(resolutions) + 1)
        retriever.recall_options["known_evidence_ids"].append("later")
        return "token-" + str(resolutions[-1])

    retriever = DenseMemRetriever(endpoint, token_provider=provider, recall_options=options)
    options["known_evidence_ids"].append("caller-mutation")
    callback = Callbacks()
    assert_mapping(await retriever.ainvoke("q", config={"callbacks": [callback]}), recall)
    assert state["calls"][0] == ("recall_memory", {**snapshot, "query": "q", "limit": 10})
    await retriever.ainvoke("again")
    assert resolutions == [1, 2]
    calls = [entry for entry in state["requests"] if entry[1].get("method") == "tools/call"]
    assert [entry[0] for entry in calls] == [b"Bearer token-1", b"Bearer token-2"]
    assert [item[0] for item in callback.events] == ["start", "end"]


@pytest.mark.parametrize("k", [1, 50])
async def test_k_boundaries_reach_server(wire_server, recall, k):
    endpoint, state = wire_server
    state["result"] = {**recall, "results": recall["results"][:1]}
    documents = await DenseMemRetriever(endpoint, token="key", k=k).ainvoke("q")
    assert len(documents) == 1 and state["calls"][0][1]["limit"] == k


@pytest.mark.parametrize("k", [0, 51, True, 1.0, "10", None])
def test_invalid_k_fails_before_network(wire_server, k):
    endpoint, state = wire_server
    with pytest.raises(ValueError):
        DenseMemRetriever(endpoint, token="key", k=k)
    assert not state["calls"]


@pytest.mark.parametrize("key", ["query", "limit", "k", "team_id", "space_id", "target_space", "x"])
async def test_options_cannot_override_authorization_or_invocation(wire_server, key):
    endpoint, state = wire_server
    with pytest.raises(ValueError):
        DenseMemRetriever(endpoint, token="key", recall_options={key: "forbidden"})
    retriever = DenseMemRetriever(endpoint, token="key")
    with pytest.raises((ValueError, TypeError)):
        await retriever.ainvoke("q", **{key: "forbidden"})
    retriever.recall_options[key] = "forbidden"
    with pytest.raises(ValueError):
        await retriever.ainvoke("q")
    assert not state["calls"]


@pytest.mark.parametrize("kwargs", [{}, {"token": "key", "token_provider": lambda: None}])
def test_auth_exclusivity_uses_core_validation(kwargs):
    with pytest.raises(ValueError, match="exactly one"):
        DenseMemRetriever("https://example.test/mcp", **kwargs)


def test_credentials_are_absent_from_pydantic_and_langchain_serialization(wire_server):
    endpoint, _ = wire_server
    token = "secret-static-value"
    retriever = DenseMemRetriever(endpoint, token=token)
    assert token not in repr(retriever) + str(retriever) + retriever.model_dump_json()
    assert token not in json.dumps(retriever.to_json())
    with pytest.raises(ValueError) as error:
        DenseMemRetriever(endpoint, token=token, k="invalid")
    assert token not in str(error.value) + repr(error.value)


@pytest.mark.parametrize("empty", [True, False])
async def test_optional_warning_preserves_response_even_when_empty(wire_server, recall, empty):
    endpoint, state = wire_server
    recall["degradations"] = [
        {
            "frontier": "feedback",
            "code": "unavailable",
            "message": "sensitive recalled text",
            "optional": True,
        }
    ]
    if empty:
        recall["results"] = []
    state["result"] = recall
    with pytest.warns(DenseMemRecallWarning) as warnings:
        documents = await DenseMemRetriever(endpoint, token="key").ainvoke("q")
    assert len(documents) == len(recall["results"])
    warning = warnings[0].message
    assert warning.result == recall and "sensitive recalled text" not in str(warning)
    warning.result["degradations"].clear()
    assert recall["degradations"]


async def test_required_degradation_raises_without_success_callback(wire_server, recall):
    endpoint, state = wire_server
    recall["degradations"] = [
        {
            "frontier": "evidence",
            "code": "unavailable",
            "message": "required",
            "required_failure": True,
        }
    ]
    state["result"] = recall
    callback = Callbacks()
    with pytest.raises(DenseMemOperationError) as error:
        await DenseMemRetriever(endpoint, token="key").ainvoke("q", {"callbacks": [callback]})
    assert error.value.result == recall
    assert [event[0] for event in callback.events] == ["start", "error"]


@pytest.mark.parametrize(
    "field,value",
    [
        ("context", None),
        ("context", "x" * 2001),
        ("evidence_id", 12),
        ("space_kind", "foreign"),
        ("equivalent_evidence_ids", "ids"),
        ("equivalents_truncated", "false"),
    ],
)
async def test_malformed_evidence_never_returns_partial_documents(
    wire_server, recall, field, value
):
    endpoint, state = wire_server
    recall["results"][1][field] = value
    state["result"] = recall
    with pytest.raises(DenseMemProtocolError):
        await DenseMemRetriever(endpoint, token="key").ainvoke("q")


@pytest.mark.parametrize(
    "field", ["recall_id", "related_relationships", "search_states", "results"]
)
async def test_missing_public_fields_are_protocol_errors(wire_server, recall, field):
    endpoint, state = wire_server
    del recall[field]
    state["result"] = recall
    with pytest.raises(DenseMemProtocolError):
        await DenseMemRetriever(endpoint, token="key").ainvoke("q")


@pytest.mark.parametrize("status", [401, 403])
async def test_http_failures_remain_typed_and_redacted(wire_server, caplog, status):
    endpoint, state = wire_server
    state["status"] = status
    caplog.set_level(logging.DEBUG)
    callback = Callbacks()
    with pytest.raises(DenseMemAuthenticationError):
        await DenseMemRetriever(endpoint, token="secret-key").ainvoke(
            "q", {"callbacks": [callback]}
        )
    assert [event[0] for event in callback.events] == ["start", "error"]
    assert "secret-key" not in caplog.text + repr(callback.events)


async def test_timeout_cancellation_and_nested_loop_use_core_lifetime(wire_server, recall):
    endpoint, state = wire_server
    state.update(result=recall, delay=2)
    with pytest.raises(DenseMemTimeoutError):
        await DenseMemRetriever(endpoint, token="key", timeout=0.05).ainvoke("timeout")
    entered = threading.Event()

    async def hold_for_cancellation(name, arguments):
        if arguments["query"] == "cancel":
            entered.set()
            await asyncio.sleep(2)
        return recall

    state.update(delay=0, handler=hold_for_cancellation)
    callback = Callbacks()
    retriever = DenseMemRetriever(endpoint, token="key")
    baseline = set(asyncio.all_tasks())
    pending = asyncio.create_task(retriever.ainvoke("cancel", {"callbacks": [callback]}))
    assert await asyncio.to_thread(entered.wait, 5)
    pending.cancel()
    with pytest.raises(DenseMemCancelledError):
        await pending
    assert set(asyncio.all_tasks()) == baseline
    assert not any(event[0] == "end" for event in callback.events)
    with pytest.raises(RuntimeError, match="inside an event loop"):
        retriever.invoke("nested")
    state["delay"] = 0
    assert len(await retriever.ainvoke("after")) == 2


async def test_concurrent_native_batch_keeps_invocation_results_separate(wire_server, recall):
    endpoint, state = wire_server

    def response(name, arguments):
        result = copy.deepcopy(recall)
        result["recall_id"] = arguments["query"]
        result["results"][0]["context"] = arguments["query"]
        return result

    state["result_factory"] = response
    retriever = DenseMemRetriever(endpoint, token="key")
    results = await retriever.abatch(["first", "second"], {"max_concurrency": 2})
    assert [documents[0].page_content for documents in results] == ["first", "second"]
    assert [documents[0].metadata["densemem"]["recall_id"] for documents in results] == [
        "first",
        "second",
    ]
    sync = await asyncio.to_thread(retriever.batch, ["third", "fourth"])
    assert [documents[0].page_content for documents in sync] == ["third", "fourth"]
