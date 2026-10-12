from typing import Any, Literal, NotRequired, TypedDict

EntityKind = Literal[
    "person", "organization", "project", "product", "place", "document", "concept", "other"
]
ValueType = Literal["string", "number", "boolean", "date", "date_time"]


class EntityProposal(TypedDict, total=False):
    name: str
    entity_kind: EntityKind
    known_entity_id: str | None


class ValueProposal(TypedDict):
    type: ValueType
    value: str | int | float | bool
    display: NotRequired[str]
    unit: NotRequired[str]


class ObjectProposal(TypedDict, total=False):
    entity: EntityProposal
    value: ValueProposal


class PredicateProposal(TypedDict, total=False):
    proposed_key: str
    known_predicate_key: str


class CorrectionTarget(TypedDict):
    relationship_id: str
    expected_version: int


class ConflictContext(TypedDict):
    conflict_id: str
    expected_version: int


class RelationshipProposal(TypedDict):
    ref: str
    subject: EntityProposal
    predicate: PredicateProposal
    object: ObjectProposal
    polarity: Literal["+", "-"]
    evidence_indices: list[int]
    known_evidence_ids: NotRequired[list[str]]
    valid_from: NotRequired[str | None]
    valid_to: NotRequired[str | None]
    correction_target: NotRequired[CorrectionTarget]
    conflict_context: NotRequired[ConflictContext]
    client_comment: NotRequired[str | None]


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
    idempotency_key: str
    evidence: list[Evidence]
    relationships: list[RelationshipProposal]


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
    query: str
    limit: NotRequired[int]
    relationship_limit: NotRequired[int]
    community_limit: NotRequired[int]
    community_relationship_limit: NotRequired[int]
    known_at: NotRequired[str | None]
    valid_at: NotRequired[str | None]
    known_evidence_ids: NotRequired[list[str]]
    known_relationship_ids: NotRequired[list[str]]
    expand_from_entity_ids: NotRequired[list[str]]


class TraceRequest(TypedDict):
    relationship_id: str
    include_evidence_content: NotRequired[bool]
    include_verification: NotRequired[bool]
    include_transitions: NotRequired[bool]
    max_depth: NotRequired[int]
    max_edges: NotRequired[int]
    predicate_keys: NotRequired[list[str]]
    topic: NotRequired[str | None]
    min_relevance: NotRequired[float | None]
