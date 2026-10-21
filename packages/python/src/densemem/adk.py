"""Google ADK 2.7.0 integration, installed with densemem[adk]."""

import asyncio
import copy
import hashlib
import json
import math
import threading
from collections import deque
from collections.abc import AsyncIterator, Mapping, Sequence
from concurrent.futures import Future
from contextlib import asynccontextmanager
from datetime import UTC, datetime
from functools import partial
from typing import Any, Protocol, cast

from google.adk.events import Event
from google.adk.memory.base_memory_service import BaseMemoryService, SearchMemoryResponse
from google.adk.memory.memory_entry import MemoryEntry
from google.adk.sessions import Session
from google.genai import types
from pydantic import Field

from .client import DenseMemClient
from .errors import (
    DenseMemAuthenticationError,
    DenseMemCancelledError,
    DenseMemError,
    DenseMemProtocolError,
    DenseMemTimeoutError,
)
from .requests import SessionEvent, SessionRequest
from .results import SessionResult

_CONTENT_FIELDS = {
    "text",
    "inline_data",
    "file_data",
    "function_call",
    "function_response",
    "executable_code",
    "code_execution_result",
    "tool_call",
    "tool_response",
}
_PART_FIELDS = _CONTENT_FIELDS | {
    "thought",
    "thought_signature",
    "video_metadata",
    "media_resolution",
    "part_metadata",
}


class CredentialProvider(Protocol):
    async def __call__(self, app_name: str, user_id: str) -> str: ...


class DenseMemSearchMemoryResponse(SearchMemoryResponse):
    custom_metadata: dict[str, Any] = Field(default_factory=dict)


class DenseMemMemoryService(BaseMemoryService):
    def __init__(
        self,
        endpoint: str,
        *,
        token: str | None = None,
        bound_user_id: str | None = None,
        credential_provider: CredentialProvider | None = None,
        timeout: float = 190.0,
        max_concurrency: int = 4,
    ) -> None:
        if (token is None) == (credential_provider is None):
            raise ValueError("provide exactly one of token or credential_provider")
        if token is not None:
            if not isinstance(bound_user_id, str) or not bound_user_id.strip():
                raise ValueError("static credentials require bound_user_id")
        elif bound_user_id is not None:
            raise ValueError("bound_user_id is only supported with a static token")
        if credential_provider is not None and not callable(credential_provider):
            raise ValueError("credential_provider must be an async callable")
        if not math.isfinite(timeout) or timeout <= 0:
            raise ValueError("timeout must be positive and finite")
        if isinstance(max_concurrency, bool) or not isinstance(max_concurrency, int):
            raise ValueError("max_concurrency must be a positive integer")
        if max_concurrency <= 0:
            raise ValueError("max_concurrency must be a positive integer")
        self._endpoint = endpoint
        self._timeout = timeout
        self._bound_user_id = bound_user_id
        self._provider = credential_provider
        self._static_client = (
            DenseMemClient(endpoint, token=token, timeout=timeout) if token is not None else None
        )
        self._available_slots = max_concurrency
        self._slot_waiters: deque[Future[None]] = deque()
        self._slots_lock = threading.Lock()

    async def _acquire_slot(self) -> None:
        with self._slots_lock:
            if self._available_slots:
                self._available_slots -= 1
                return
            waiter: Future[None] = Future()
            self._slot_waiters.append(waiter)
        try:
            await asyncio.wrap_future(waiter)
        except asyncio.CancelledError:
            with self._slots_lock:
                cancelled = waiter.cancel()
                if cancelled and waiter in self._slot_waiters:
                    self._slot_waiters.remove(waiter)
            if not cancelled:
                self._release_slot()
            raise

    def _release_slot(self) -> None:
        with self._slots_lock:
            while self._slot_waiters:
                waiter = self._slot_waiters.popleft()
                if waiter.set_running_or_notify_cancel():
                    waiter.set_result(None)
                    return
            self._available_slots += 1

    def _client(self, app_name: str, user_id: str) -> DenseMemClient:
        if self._static_client is not None:
            return self._static_client
        assert self._provider is not None
        return DenseMemClient(
            self._endpoint,
            token_provider=partial(self._provider, app_name, user_id),
            timeout=self._timeout,
        )

    def _check_user(self, app_name: str, user_id: str) -> None:
        if not isinstance(app_name, str) or not app_name.strip():
            raise ValueError("app_name must be nonempty")
        if not isinstance(user_id, str) or not user_id.strip():
            raise ValueError("user_id must be nonempty")
        if self._bound_user_id is not None and user_id != self._bound_user_id:
            raise DenseMemAuthenticationError("ADK user does not match bound_user_id")

    @asynccontextmanager
    async def _operation(self) -> AsyncIterator[None]:
        deadline = asyncio.timeout(self._timeout)
        try:
            async with deadline:
                await self._acquire_slot()
                try:
                    yield
                finally:
                    self._release_slot()
        except DenseMemCancelledError as error:
            if deadline.expired():
                raise DenseMemTimeoutError(
                    "ADK memory operation timed out", result=error.result
                ) from None
            raise
        except TimeoutError:
            raise DenseMemTimeoutError(
                "ADK memory operation timed out; receipt is unknown"
            ) from None
        except asyncio.CancelledError:
            raise DenseMemCancelledError(
                "ADK memory operation cancelled; receipt is unknown"
            ) from None

    async def add_session_to_memory(self, session: Session) -> None:
        await self.add_events_to_memory(
            app_name=session.app_name,
            user_id=session.user_id,
            session_id=session.id,
            events=session.events,
        )

    async def add_events_to_memory(
        self,
        *,
        app_name: str,
        user_id: str,
        events: Sequence[Event],
        session_id: str | None = None,
        custom_metadata: Mapping[str, object] | None = None,
    ) -> None:
        self._check_user(app_name, user_id)
        if not isinstance(session_id, str) or not session_id.strip():
            raise ValueError("session_id is required for immutable ADK event provenance")
        if custom_metadata:
            raise ValueError("ADK custom_metadata is unsupported by session ingestion")
        requests = _session_requests(app_name, user_id, session_id, events)
        completed: list[SessionResult] = []
        current: SessionRequest | None = None
        try:
            async with self._operation():
                for request in requests:
                    current = request
                    result = await self._client(app_name, user_id).ingest_session(request)
                    if result.get("processing_state") != "completed" or result.get(
                        "search_state"
                    ) not in ("current", "not_required"):
                        raise DenseMemProtocolError(
                            "Session ingestion did not return a searchable terminal outcome",
                            result=cast(dict[str, Any], result),
                        )
                    completed.append(result)
                    current = None
        except (DenseMemError, DenseMemCancelledError) as error:
            error.completed_results = copy.deepcopy(completed)
            error.failed_request = copy.deepcopy(current)
            raise

    async def add_memory(
        self,
        *,
        app_name: str,
        user_id: str,
        memories: Sequence[MemoryEntry],
        custom_metadata: Mapping[str, object] | None = None,
    ) -> None:
        raise NotImplementedError(
            "Direct memory writes are unsupported; "
            "use add_events_to_memory or add_session_to_memory"
        )

    async def search_memory(
        self,
        *,
        app_name: str,
        user_id: str,
        query: str,
    ) -> DenseMemSearchMemoryResponse:
        self._check_user(app_name, user_id)
        async with self._operation():
            result = await self._client(app_name, user_id).recall_memory({"query": query})
        return DenseMemSearchMemoryResponse(
            memories=[
                MemoryEntry(
                    id=evidence["evidence_id"],
                    content=types.Content(parts=[types.Part(text=evidence["context"])]),
                    custom_metadata={"densemem": copy.deepcopy(evidence)},
                )
                for evidence in result["results"]
            ],
            custom_metadata={"densemem": copy.deepcopy(result)},
        )


def _session_requests(
    app_name: str,
    user_id: str,
    session_id: str,
    events: Sequence[Event],
) -> list[SessionRequest]:
    mapped: list[SessionEvent] = []
    for event in events:
        if not isinstance(event, Event):
            raise ValueError("events must contain ADK Event instances")
        if event.author != "user" or event.partial or event.interrupted:
            continue
        content = event.content
        if content is None or content.role not in (None, "user"):
            continue
        texts = []
        for part in content.parts or []:
            values = part.model_dump(exclude_none=True)
            if part.model_extra or values.keys() - _PART_FIELDS:
                raise ValueError("unsupported ADK content part shape")
            if part.thought or part.thought_signature:
                continue
            kinds = values.keys() & _CONTENT_FIELDS
            if len(kinds) > 1:
                raise ValueError("ADK parts must contain one content kind")
            if part.text is not None:
                texts.append(part.text)
        text = "".join(texts)
        if not text.strip():
            continue
        occurred_at = (
            datetime.fromtimestamp(event.timestamp, UTC).isoformat().replace("+00:00", "Z")
        )
        mapped.append({"event_id": event.id, "text": text, "occurred_at": occurred_at})
    requests: list[SessionRequest] = []
    for start in range(0, len(mapped), 20):
        batch = mapped[start : start + 20]
        identity = ["google-adk", app_name, user_id, session_id, [e["event_id"] for e in batch]]
        digest = hashlib.sha256(json.dumps(identity, ensure_ascii=False).encode()).hexdigest()
        requests.append(
            {
                "idempotency_key": "google-adk-v1:" + digest,
                "framework": "google-adk",
                "app_name": app_name,
                "user_id": user_id,
                "session_id": session_id,
                "events": batch,
            }
        )
    return requests
