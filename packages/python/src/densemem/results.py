from typing import Any, Literal, NotRequired, TypedDict

SearchState = Literal["current", "not_required", "pending", "failed"]


class PublicError(TypedDict):
    code: str
    message: str
    retryable: bool
    next_action: str
    remediation: str
    reason_code: NotRequired[str]
    correlation_id: NotRequired[str]
    details: NotRequired[dict[str, Any]]
    retry_after_seconds: NotRequired[int]


class EvidenceResult(TypedDict):
    evidence_index: int
    disposition: Literal["stored", "not_stored"]
    superseded_evidence_ids: list[str]
    search_state: SearchState
    evidence_id: NotRequired[str]
    content_hash: NotRequired[str]
    reason: NotRequired[str]
    message: NotRequired[str]
    remediation: NotRequired[str]
    error: NotRequired[PublicError | None]


class TerminalResult(TypedDict):
    contract_version: str
    submission_id: str
    submission_kind: str
    correlation_id: str
    processing_state: Literal["completed", "failed"]
    search_state: SearchState
    errors: list[PublicError]
    warnings: NotRequired[list[str]]


class RememberResult(TerminalResult):
    space_kind: Literal["private", "team_shared"]
    evidence: list[EvidenceResult]


class SessionEventResult(TypedDict):
    event_id: str
    disposition: Literal["accepted", "duplicate"]
    processing_state: Literal["completed", "failed"]
    evidence_ids: list[str]


class SessionResult(TerminalResult):
    accepted_event_count: int
    duplicate_event_count: int
    events: list[SessionEventResult]
    memory_outcome: Literal["stored", "no_memory", "failed", "retired_contract"]
    retired_payload: NotRequired[bool]
    original_contract_version: NotRequired[str]


class RecallEvidence(TypedDict):
    evidence_id: str
    context: str
    space_kind: Literal["private", "team_shared"]
    equivalent_evidence_ids: list[str]
    equivalents_truncated: bool


class RelatedTopic(TypedDict):
    topic_id: str
    name: str
    description: str
    eligible_evidence_count: int
    rank: int


class RecallResult(TypedDict):
    recall_id: str
    results: list[RecallEvidence]
    related_topics: list[RelatedTopic]
    topics_truncated: bool
    conflicts: list[dict[str, Any]]
    search_states: dict[str, SearchState]
    degradations: list[dict[str, Any]]
    suggested_actions: list[dict[str, Any]]


class TraceResult(TypedDict):
    evidence: dict[str, Any] | None
    occurrences: list[dict[str, Any]]
    source_revisions: list[dict[str, Any]]
    decisions: list[dict[str, Any]]
    lifecycle: list[dict[str, Any]]
    search_documents: list[dict[str, Any]]
    omissions: list[str]
    truncated: bool


class ExportResult(TypedDict):
    artifact_json: str
    content_sha256: str
    filename: str
    counts: dict[str, int]
    omissions: list[dict[str, str]]
