from typing import Any, Literal, NotRequired, TypedDict


class Evidence(TypedDict):
    content: str
    source_type: NotRequired[Literal["conversation", "document", "observation", "manual"]]
    source: NotRequired[str]
    authority: NotRequired[Literal["authoritative", "primary", "secondary", "inferred", "unknown"]]
    source_group: NotRequired[str]
    source_key: NotRequired[str]
    source_revision: NotRequired[str]
    previous_source_revision: NotRequired[str]
    supersedes_evidence_ids: NotRequired[list[str]]
    labels: NotRequired[list[str]]
    metadata: NotRequired[dict[str, Any]]
    force_insert: NotRequired[bool]


class RememberRequest(TypedDict):
    target_space: Literal["private", "team_shared"]
    idempotency_key: str
    evidence: list[Evidence]


class SessionEvent(TypedDict):
    event_id: str
    text: str
    occurred_at: NotRequired[str]


class SessionRequest(TypedDict):
    idempotency_key: str
    framework: str
    app_name: str
    user_id: str
    session_id: str
    events: list[SessionEvent]


class RecallRequest(TypedDict):
    query: NotRequired[str]
    limit: NotRequired[int]
    topic_ids: NotRequired[list[str]]
    topic_limit: NotRequired[int]
    known_at: NotRequired[str | None]
    valid_at: NotRequired[str | None]
    known_evidence_ids: NotRequired[list[str]]


class TraceRequest(TypedDict):
    evidence_id: str
    include_evidence_content: NotRequired[bool]
    include_transitions: NotRequired[bool]
    max_events: NotRequired[int]
    max_fragment_content_runes: NotRequired[int]


class ExportRequest(TypedDict):
    name: str
    evidence_ids: list[str]
    description: NotRequired[str]
    include_evidence: NotRequired[bool]
