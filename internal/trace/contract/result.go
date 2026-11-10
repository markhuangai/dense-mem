package contract

import "time"

type EvidenceTraceResult struct {
	Evidence        *TraceEvidenceFragment        `json:"evidence"`
	Occurrences     []TraceEvidenceFragment       `json:"occurrences"`
	SourceRevisions []TraceSourceRevision         `json:"source_revisions"`
	Decisions       []TraceEvidenceDecision       `json:"decisions"`
	Lifecycle       []TraceEvidenceLifecycleEvent `json:"lifecycle"`
	SearchDocuments []TraceSearchDocument         `json:"search_documents"`
	Omissions       []string                      `json:"omissions"`
	Truncated       bool                          `json:"truncated"`
	Selection       map[string]any                `json:"-"`
}

type TraceSourceRevision struct {
	SourceID             string         `json:"source_id"`
	SourceRevisionID     string         `json:"source_revision_id"`
	OwnerProfileID       string         `json:"owner_profile_id"`
	SourceKey            string         `json:"source_key"`
	RevisionToken        string         `json:"revision_token"`
	SupersedesRevisionID string         `json:"supersedes_revision_id,omitempty"`
	CurrentRevisionID    string         `json:"current_revision_id"`
	ContentHash          string         `json:"content_hash"`
	Envelope             map[string]any `json:"envelope"`
	CreatedAt            time.Time      `json:"created_at"`
}

type TraceEvidenceDecision struct {
	DecisionID   string    `json:"decision_id"`
	IngestID     string    `json:"ingest_id"`
	OccurrenceID string    `json:"occurrence_id,omitempty"`
	Kind         string    `json:"kind"`
	Decision     string    `json:"decision"`
	Reason       string    `json:"reason,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type SessionProvenance struct {
	Framework  string  `json:"framework"`
	AppName    string  `json:"app_name"`
	UserID     string  `json:"user_id"`
	SessionID  string  `json:"session_id"`
	EventID    string  `json:"event_id"`
	EventIndex int     `json:"event_index"`
	SpanStart  int     `json:"span_start"`
	SpanEnd    int     `json:"span_end"`
	OccurredAt *string `json:"occurred_at,omitempty"`
}

type TraceEvidenceFragment struct {
	TeamID                   string             `json:"-"`
	SpaceID                  string             `json:"-"`
	SpaceGeneration          int64              `json:"-"`
	OccurrenceOwnerProfileID string             `json:"occurrence_owner_profile_id,omitempty"`
	ContentOmitted           bool               `json:"content_omitted,omitempty"`
	Session                  *SessionProvenance `json:"session,omitempty"`
	FragmentID               string             `json:"evidence_id,omitempty"`
	OccurrenceID             string             `json:"occurrence_id,omitempty"`
	IngestID                 string             `json:"ingest_id,omitempty"`
	OwnerProfileID           string             `json:"owner_profile_id,omitempty"`
	SourceID                 string             `json:"source_id,omitempty"`
	SourceRevisionID         string             `json:"source_revision_id,omitempty"`
	SourceKey                string             `json:"source_key,omitempty"`
	SourceKind               string             `json:"source_kind,omitempty"`
	RevisionToken            string             `json:"revision_token,omitempty"`
	CurrentRevisionID        string             `json:"current_revision_id,omitempty"`
	EvidenceIndex            int                `json:"evidence_index,omitempty"`
	Content                  string             `json:"content,omitempty"`
	ContentHash              string             `json:"content_hash,omitempty"`
	ContentTruncated         bool               `json:"content_truncated,omitempty"`
	SourceType               string             `json:"source_type,omitempty"`
	Authority                string             `json:"authority,omitempty"`
	SourceRef                string             `json:"source_ref,omitempty"`
	Labels                   []string           `json:"labels,omitempty"`
	Metadata                 map[string]any     `json:"metadata,omitempty"`
	CreatedAt                time.Time          `json:"created_at,omitempty"`
}

type TraceEvidenceLifecycleEvent struct {
	LifecycleEventID      string    `json:"lifecycle_event_id,omitempty"`
	LifecycleOperationID  string    `json:"lifecycle_operation_id,omitempty"`
	TargetFragmentID      string    `json:"target_evidence_id,omitempty"`
	ReplacementFragmentID string    `json:"replacement_evidence_id,omitempty"`
	OwnerProfileID        string    `json:"owner_profile_id,omitempty"`
	Action                string    `json:"action,omitempty"`
	Reason                string    `json:"reason,omitempty"`
	CreatedAt             time.Time `json:"created_at,omitempty"`
}

type TraceSearchDocument struct {
	SearchDocumentID    string    `json:"search_document_id,omitempty"`
	OwnerProfileID      string    `json:"owner_profile_id,omitempty"`
	SourceKind          string    `json:"source_kind,omitempty"`
	SourceID            string    `json:"source_id,omitempty"`
	SourceVersion       int64     `json:"source_version,omitempty"`
	DocumentVersion     int64     `json:"document_version,omitempty"`
	EmbeddingContractID string    `json:"embedding_contract_id,omitempty"`
	EmbeddingDimensions int       `json:"embedding_dimensions,omitempty"`
	SearchState         string    `json:"search_state,omitempty"`
	DocumentHash        string    `json:"document_hash,omitempty"`
	CreatedAt           time.Time `json:"created_at,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}
