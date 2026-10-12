import asyncio
import copy
import json
import logging
import math
import traceback
from collections.abc import Callable, Coroutine
from contextvars import ContextVar
from typing import Any, Protocol, TypeVar, cast
from urllib.parse import urlsplit

import httpx
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client
from mcp.shared.exceptions import McpError
from mcp.types import InitializeResult, JSONRPCMessage, ListToolsResult
from pydantic import ValidationError

from .errors import (
    DenseMemAuthenticationError,
    DenseMemCancelledError,
    DenseMemError,
    DenseMemOperationError,
    DenseMemProtocolError,
    DenseMemTimeoutError,
    DenseMemTransportError,
)
from .requests import RecallRequest, RememberRequest, SessionRequest, TraceRequest
from .results import RecallResult, RememberResult, SessionResult, TraceResult

_operation_token: ContextVar[str] = ContextVar("densemem_operation_token", default="")


class _CredentialFilter(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        token = _operation_token.get()
        if token:
            record.msg = record.getMessage().replace(token, "[REDACTED]")
            record.args = ()
            if record.exc_info and record.exc_info[1] is not None:
                record.exc_text = "".join(traceback.format_exception(record.exc_info[1])).replace(
                    token, "[REDACTED]"
                )
                record.exc_info = None
        return True


logging.getLogger("mcp.client.streamable_http").addFilter(_CredentialFilter())


class TokenProvider(Protocol):
    async def __call__(self) -> str: ...


class DenseMemClient:
    def __init__(
        self,
        endpoint: str,
        *,
        token: str | None = None,
        token_provider: TokenProvider | None = None,
        timeout: float = 190.0,
    ) -> None:
        url = urlsplit(endpoint)
        if (
            url.scheme not in ("http", "https")
            or not url.hostname
            or url.username is not None
            or url.password is not None
            or url.query
            or url.fragment
        ):
            raise ValueError(
                "endpoint must be an HTTP(S) URL without URL credentials or query/fragment"
            )
        if (token is None) == (token_provider is None):
            raise ValueError("provide exactly one of token or token_provider")
        if token is not None and not _valid_token(token):
            raise ValueError("token must be a nonempty bearer credential without line breaks")
        if token_provider is not None and not callable(token_provider):
            raise ValueError("token_provider must be an async callable")
        if not math.isfinite(timeout) or timeout <= 0:
            raise ValueError("timeout must be positive and finite")
        self.endpoint = endpoint
        self.timeout = timeout
        self._token = token
        self._token_provider = token_provider

    async def initialize(self) -> InitializeResult:
        return cast(InitializeResult, await self._execute("initialize"))

    async def list_tools(self) -> ListToolsResult:
        return cast(ListToolsResult, await self._execute("list_tools"))

    async def remember(self, request: RememberRequest) -> RememberResult:
        return cast(RememberResult, await self._execute("remember", cast(dict[str, Any], request)))

    async def ingest_session(self, request: SessionRequest) -> SessionResult:
        return cast(
            SessionResult, await self._execute("ingest_session", cast(dict[str, Any], request))
        )

    async def recall_memory(self, request: RecallRequest) -> RecallResult:
        return cast(
            RecallResult, await self._execute("recall_memory", cast(dict[str, Any], request))
        )

    async def trace_memory(self, request: TraceRequest) -> TraceResult:
        return cast(TraceResult, await self._execute("trace_memory", cast(dict[str, Any], request)))

    async def _execute(self, method: str, request: dict[str, Any] | None = None) -> Any:
        payload = copy.deepcopy(request)
        token = ""
        token_context = _operation_token.set("")
        response_error: httpx.HTTPStatusError | None = None

        async def inspect_response(response: httpx.Response) -> None:
            nonlocal response_error
            try:
                await _inspect_response(response)
            except httpx.HTTPStatusError as exc:
                # The SDK can swallow notification HTTP failures during transport cleanup.
                if response_error is None:
                    response_error = exc
                raise

        try:
            async with asyncio.timeout(self.timeout):
                token = await self._credential()
                _operation_token.set(token)
                async with httpx.AsyncClient(
                    headers={"Authorization": f"Bearer {token}"},
                    timeout=httpx.Timeout(self.timeout, connect=min(10.0, self.timeout)),
                    follow_redirects=False,
                    event_hooks={"response": [inspect_response]},
                ) as http:
                    async with streamable_http_client(self.endpoint, http_client=http) as streams:
                        async with ClientSession(streams[0], streams[1]) as session:
                            initialized = await session.initialize()
                            if method == "initialize":
                                return initialized
                            if method == "list_tools":
                                return await session.list_tools()
                            response = await session.call_tool(method, payload)
                            result = response.structuredContent
                            if not isinstance(result, dict):
                                raise DenseMemProtocolError(
                                    "Dense-Mem did not return structured content"
                                )
                            if response.isError or result.get("processing_state") == "failed":
                                errors = result.get("errors")
                                fields = (
                                    errors[0] if isinstance(errors, list) and errors else result
                                )
                                code = fields.get("code") if isinstance(fields, dict) else None
                                if code == "request_cancelled":
                                    raise DenseMemCancelledError(
                                        _public_message(result, token), result=result
                                    )
                                error_type = (
                                    DenseMemTimeoutError
                                    if code == "request_timeout"
                                    else DenseMemOperationError
                                )
                                raise error_type(_public_message(result, token), result=result)
                            return result
        except DenseMemCancelledError:
            raise
        except asyncio.CancelledError:
            raise DenseMemCancelledError(
                "Dense-Mem operation cancelled; receipt is unknown"
            ) from None
        except Exception as exc:
            error = _translate_error(exc, token)
            if response_error is not None and error.result is None:
                error = _translate_error(response_error, token)
            raise error from None
        finally:
            _operation_token.reset(token_context)

    async def _credential(self) -> str:
        if self._token is not None:
            return self._token
        try:
            assert self._token_provider is not None
            token = await self._token_provider()
        except Exception:
            raise DenseMemAuthenticationError("Token provider failed") from None
        if not _valid_token(token):
            raise DenseMemAuthenticationError(
                "Token provider did not return a valid bearer credential"
            )
        return token


def _valid_token(token: str) -> bool:
    return isinstance(token, str) and bool(token) and "\r" not in token and "\n" not in token


def _public_message(result: dict[str, Any], token: str) -> str:
    errors = result.get("errors")
    fields = errors[0] if isinstance(errors, list) and errors else result
    message = fields.get("message") if isinstance(fields, dict) else None
    if not isinstance(message, str):
        return "Dense-Mem operation failed"
    return (message.replace(token, "[REDACTED]") if token else message)[:512]


async def _inspect_response(response: httpx.Response) -> None:
    if not response.is_error:
        if response.status_code == 200 and response.headers.get("content-type", "").startswith(
            "application/json"
        ):
            try:
                JSONRPCMessage.model_validate_json(await response.aread())
            except ValidationError:
                raise DenseMemProtocolError("Dense-Mem returned an invalid MCP response") from None
        return
    body = bytearray()
    async for chunk in response.aiter_bytes():
        body.extend(chunk[: 16385 - len(body)])
        if len(body) > 16384:
            break
    fields: dict[str, Any] = {}
    if len(body) <= 16384:
        try:
            parsed = json.loads(body)
        except (ValueError, UnicodeDecodeError):
            parsed = None
        if isinstance(parsed, dict):
            fields = parsed
    correlation = response.headers.get("X-Correlation-ID")
    if correlation and "correlation_id" not in fields:
        fields["correlation_id"] = correlation
    response.extensions["densemem_error"] = fields
    response.raise_for_status()


def _translate_error(exc: Exception, token: str) -> DenseMemError:
    if isinstance(exc, ExceptionGroup):
        return _translate_error(exc.exceptions[0], token)
    if isinstance(exc, DenseMemError):
        return exc
    if isinstance(exc, (TimeoutError, httpx.TimeoutException)):
        return DenseMemTimeoutError("Dense-Mem operation timed out; receipt is unknown")
    if isinstance(exc, httpx.HTTPStatusError):
        status = exc.response.status_code
        cls = DenseMemAuthenticationError if status in (401, 403) else DenseMemTransportError
        fields = exc.response.extensions.get("densemem_error") or None
        return cls(
            _public_message(fields, token)
            if fields
            else f"Dense-Mem HTTP request failed ({status})",
            status_code=status,
            result=fields,
        )
    if isinstance(exc, httpx.TransportError):
        return DenseMemTransportError("Dense-Mem transport failed; receipt is unknown")
    if isinstance(exc, McpError):
        result = exc.error.data if isinstance(exc.error.data, dict) else None
        protocol_cls = DenseMemTimeoutError if exc.error.code == 408 else DenseMemProtocolError
        return protocol_cls(
            _public_message(result, token) if result else "Dense-Mem MCP request failed",
            result=result,
            rpc_code=exc.error.code,
        )
    return DenseMemProtocolError("Dense-Mem returned an invalid MCP response")


T = TypeVar("T")


class SyncDenseMemClient:
    def __init__(
        self,
        endpoint: str,
        *,
        token: str | None = None,
        token_provider: TokenProvider | None = None,
        timeout: float = 190.0,
    ) -> None:
        self._async = DenseMemClient(
            endpoint, token=token, token_provider=token_provider, timeout=timeout
        )

    @staticmethod
    def _run(operation: Callable[[], Coroutine[Any, Any, T]]) -> T:
        try:
            asyncio.get_running_loop()
        except RuntimeError:
            return asyncio.run(operation())
        raise RuntimeError("SyncDenseMemClient cannot run inside an event loop; use DenseMemClient")

    def initialize(self) -> InitializeResult:
        return self._run(self._async.initialize)

    def list_tools(self) -> ListToolsResult:
        return self._run(self._async.list_tools)

    def remember(self, request: RememberRequest) -> RememberResult:
        return self._run(lambda: self._async.remember(request))

    def ingest_session(self, request: SessionRequest) -> SessionResult:
        return self._run(lambda: self._async.ingest_session(request))

    def recall_memory(self, request: RecallRequest) -> RecallResult:
        return self._run(lambda: self._async.recall_memory(request))

    def trace_memory(self, request: TraceRequest) -> TraceResult:
        return self._run(lambda: self._async.trace_memory(request))
