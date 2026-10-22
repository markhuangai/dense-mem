"""Read-only LangChain Core 1.5.4 integration, installed with densemem[langchain]."""

import copy
import warnings
from collections.abc import Mapping
from importlib.metadata import PackageNotFoundError, version
from typing import Any, cast

from pydantic import ConfigDict, Field, PrivateAttr

from .client import DenseMemClient, SyncDenseMemClient, TokenProvider
from .errors import DenseMemOperationError, DenseMemProtocolError
from .requests import RecallRequest
from .results import RecallEvidence, RecallResult

try:
    from langchain_core.callbacks.manager import (
        AsyncCallbackManagerForRetrieverRun,
        CallbackManagerForRetrieverRun,
    )
    from langchain_core.documents import Document
    from langchain_core.retrievers import BaseRetriever
except ImportError:
    raise ImportError("Install densemem[langchain] with langchain-core==1.5.4") from None

try:
    if version("langchain-core") != "1.5.4":
        raise ImportError("densemem[langchain] requires langchain-core==1.5.4")
except PackageNotFoundError:
    raise ImportError("Install densemem[langchain] with langchain-core==1.5.4") from None


class DenseMemRecallWarning(UserWarning):
    """Optional recall degradation; result retains the public response."""

    def __init__(self, result: RecallResult) -> None:
        super().__init__(
            "Dense-Mem recall returned an optional degradation; inspect warning.result"
        )
        self.result = copy.deepcopy(result)


class DenseMemRetriever(BaseRetriever):
    model_config = ConfigDict(extra="forbid", validate_assignment=True, hide_input_in_errors=True)

    k: int = Field(default=10, strict=True, ge=1, le=50)
    recall_options: dict[str, Any] = Field(default_factory=dict, repr=False)
    _client: DenseMemClient = PrivateAttr()

    def __init__(
        self,
        endpoint: str,
        *,
        token: str | None = None,
        token_provider: TokenProvider | None = None,
        timeout: float = 190.0,
        k: int = 10,
        recall_options: Mapping[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        client = DenseMemClient(
            endpoint, token=token, token_provider=token_provider, timeout=timeout
        )
        fields: dict[str, Any] = {
            **kwargs,
            "k": k,
            "recall_options": copy.deepcopy(dict(recall_options or {})),
        }
        super().__init__(**fields)
        self._client = client
        self._request("")

    def _request(
        self, query: str, invocation_options: Mapping[str, Any] | None = None
    ) -> RecallRequest:
        if invocation_options and invocation_options.keys() - {"verbose"}:
            raise ValueError("Invocation overrides are unsupported; use constructor recall_options")
        allowed = RecallRequest.__annotations__.keys() - {"query", "limit"}
        if self.recall_options.keys() - allowed:
            raise ValueError(
                "recall_options must contain only public options other than query/limit"
            )
        return cast(
            RecallRequest, {**copy.deepcopy(self.recall_options), "query": query, "limit": self.k}
        )

    async def _recall(self, request: RecallRequest) -> list[Document]:
        result = await self._client.recall_memory(request)
        return _documents(result, request["limit"])

    def _get_relevant_documents(
        self, query: str, *, run_manager: CallbackManagerForRetrieverRun, **kwargs: Any
    ) -> list[Document]:
        request = self._request(query, kwargs)
        return SyncDenseMemClient._run(lambda: self._recall(request))

    async def _aget_relevant_documents(
        self, query: str, *, run_manager: AsyncCallbackManagerForRetrieverRun, **kwargs: Any
    ) -> list[Document]:
        return await self._recall(self._request(query, kwargs))


def _documents(result: RecallResult, k: int) -> list[Document]:
    public = cast(dict[str, Any], result)
    try:
        if not isinstance(result["recall_id"], str) or not result["recall_id"]:
            raise ValueError
        context = {key: public[key] for key in RecallResult.__annotations__ if key != "results"}
        for key in (
            "related_relationships",
            "related_communities",
            "conflicts",
            "degradations",
            "suggested_actions",
        ):
            if not isinstance(context[key], list) or any(
                not isinstance(item, dict) for item in context[key]
            ):
                raise ValueError
        states = result["search_states"]
        if not isinstance(states, dict) or any(
            states[key] not in ("current", "not_required", "pending", "failed")
            for key in ("evidence", "relationships")
        ):
            raise ValueError
        for degradation in result["degradations"]:
            if not all(
                isinstance(degradation[key], str) for key in ("frontier", "code", "message")
            ):
                raise ValueError
            if any(
                type(degradation[key]) is not bool
                for key in ("optional", "required_failure")
                if key in degradation
            ):
                raise ValueError
        if not isinstance(result["results"], list) or len(result["results"]) > k:
            raise ValueError
        documents = []
        for evidence in result["results"]:
            if (
                not isinstance(evidence["evidence_id"], str)
                or not 0 < len(evidence["evidence_id"]) <= 128
                or not isinstance(evidence["context"], str)
                or len(evidence["context"]) > 2000
                or evidence["space_kind"] not in ("private", "team_shared")
                or type(evidence["equivalents_truncated"]) is not bool
                or not isinstance(evidence["equivalent_evidence_ids"], list)
                or any(not isinstance(value, str) for value in evidence["equivalent_evidence_ids"])
            ):
                raise ValueError
            public_evidence = cast(dict[str, Any], evidence)
            metadata = {
                key: public_evidence[key]
                for key in RecallEvidence.__annotations__
                if key != "context"
            }
            documents.append(
                Document(
                    id=evidence["evidence_id"],
                    page_content=evidence["context"],
                    metadata={"densemem": copy.deepcopy({**metadata, **context})},
                )
            )
    except (KeyError, TypeError, ValueError, AttributeError):
        raise DenseMemProtocolError("Dense-Mem returned a malformed recall response") from None
    if any(item.get("required_failure") for item in result["degradations"]):
        raise DenseMemOperationError(
            "Dense-Mem recall reported a required failure", result=cast(dict[str, Any], result)
        )
    if result["degradations"]:
        warnings.warn(DenseMemRecallWarning(result), stacklevel=1)
    return documents
