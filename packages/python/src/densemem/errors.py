import asyncio
from typing import Any


class DenseMemError(Exception):
    """A bounded client failure; result retains any server-returned receipt."""

    def __init__(
        self,
        message: str,
        *,
        result: dict[str, Any] | None = None,
        status_code: int | None = None,
        rpc_code: int | None = None,
    ) -> None:
        super().__init__(message)
        self.result = result
        self.status_code = status_code
        self.rpc_code = rpc_code
        fields = result or {}
        errors = fields.get("errors")
        primary = errors[0] if isinstance(errors, list) and errors else fields
        primary = primary if isinstance(primary, dict) else {}
        for name in ("code", "reason_code", "next_action", "remediation"):
            value = primary.get(name)
            setattr(self, name, value if isinstance(value, str) else None)
        self.code: str | None
        self.reason_code: str | None
        self.next_action: str | None
        self.remediation: str | None
        self.retryable = primary.get("retryable") is True
        self.submission_id = self._identifier(fields, "submission_id")
        self.correlation_id = self._identifier(fields, "correlation_id")

    @staticmethod
    def _identifier(fields: dict[str, Any], key: str) -> str | None:
        value = fields.get(key)
        return value if isinstance(value, str) and 0 < len(value) <= 128 else None


class DenseMemAuthenticationError(DenseMemError):
    """Credential acquisition or HTTP authentication/authorization failed."""


class DenseMemTransportError(DenseMemError):
    """Transport failed; a write without a receipt has an unknown outcome."""


class DenseMemProtocolError(DenseMemError):
    """MCP returned a protocol error or an invalid response."""


class DenseMemOperationError(DenseMemError):
    """The server returned an actionable error or failed terminal receipt."""


class DenseMemTimeoutError(DenseMemTransportError):
    """The client or server deadline elapsed."""


class DenseMemCancelledError(asyncio.CancelledError):
    """Caller cancellation, without an invented server receipt."""

    def __init__(self, message: str, *, result: dict[str, Any] | None = None) -> None:
        super().__init__(message)
        self.result = result
        self.submission_id = DenseMemError._identifier(result or {}, "submission_id")
        self.correlation_id = DenseMemError._identifier(result or {}, "correlation_id")
