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


class RelationshipSplit(TypedDict):
    relationship_id: str
    relationship_version: int
    split_index: int
    status: str


class RelationshipResult(TypedDict):
    ref: str
    disposition: Literal["stored", "not_stored"]
    splits: list[RelationshipSplit]
    reason: NotRequired[str]
    message: NotRequired[str]
    remediation: NotRequired[str]


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
    relationship_results: list[RelationshipResult]
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


class RecallEvidence(TypedDict):
    evidence_id: str
    context: str
    space_kind: Literal["private", "team_shared"]
    equivalent_evidence_ids: list[str]
    equivalents_truncated: bool


class RecallResult(TypedDict):
    recall_id: str
    results: list[RecallEvidence]
    related_relationships: list[dict[str, Any]]
    related_communities: list[dict[str, Any]]
    conflicts: list[dict[str, Any]]
    search_states: dict[str, SearchState]
    degradations: list[dict[str, Any]]
    suggested_actions: list[dict[str, Any]]


class TraceResult(TypedDict):
    relationship: dict[str, Any] | None
    observations: list[dict[str, Any]]
    evidence_supports: list[dict[str, Any]]
    evidence_support_decision_events: list[dict[str, Any]]
    evidence: list[dict[str, Any]]
    evidence_lifecycle_events: list[dict[str, Any]]
    verification_events: list[dict[str, Any]]
    transitions: list[dict[str, Any]]
    conflicts: list[dict[str, Any]]
    cross_profile_references: list[dict[str, Any]]
    identity_corrections: list[dict[str, Any]]
    supersession_lineage: list[dict[str, Any]]
    semantic_nodes: list[dict[str, Any]]
    semantic_edges: list[dict[str, Any]]
    visited_entity_ids: list[str]
    stopped_reason: str | None
