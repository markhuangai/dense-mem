package contract

import (
	"github.com/markhuangai/dense-mem/internal/domain"
	"time"
)

type ResultKind string

// SynchronousRememberCommitResult is the terminal, replayable result produced by
// the one request-owned Remember transaction. Receipt is already safe to
// persist and replay; callers must not reconstruct it from placement rows.
type SynchronousRememberCommitResult struct {
	IngestID        string
	AssessmentID    string
	Outcome         string
	Receipt         *RememberReceipt
	SearchDocuments []SearchDocumentResult
}

type RememberReceipt struct {
	IngestID            string                     `json:"-"`
	SpaceKind           domain.MemorySpaceKind     `json:"space_kind,omitempty"`
	ContractVersion     string                     `json:"contract_version"`
	SubmissionID        string                     `json:"submission_id"`
	SubmissionKind      string                     `json:"submission_kind"`
	ProcessingState     string                     `json:"processing_state"`
	SearchState         string                     `json:"search_state"`
	CorrelationID       string                     `json:"correlation_id,omitempty"`
	Evidence            []SubmissionEvidenceStatus `json:"evidence"`
	Errors              []SubmissionStatusError    `json:"errors"`
	Warnings            []string                   `json:"warnings,omitempty"`
	QuarantineExpiresAt *time.Time                 `json:"-"`
	Kind                ResultKind                 `json:"-"`
	Terminal            *RememberReceipt           `json:"-"`
}

type SubmissionEvidenceStatus struct {
	PartitionIndex        *int                   `json:"partition_index,omitempty"`
	SourceItemID          string                 `json:"source_item_id,omitempty"`
	SpanStart             *int                   `json:"span_start,omitempty"`
	SpanEnd               *int                   `json:"span_end,omitempty"`
	OccurrenceID          string                 `json:"occurrence_id,omitempty"`
	Disposition           string                 `json:"disposition"`
	EvidenceID            string                 `json:"evidence_id,omitempty"`
	ContentHash           string                 `json:"content_hash,omitempty"`
	EvidenceIndex         int                    `json:"evidence_index"`
	SupersededEvidenceIDs []string               `json:"superseded_evidence_ids"`
	SearchState           string                 `json:"search_state"`
	Reason                string                 `json:"reason,omitempty"`
	Message               string                 `json:"message,omitempty"`
	Remediation           string                 `json:"remediation,omitempty"`
	Error                 *SubmissionStatusError `json:"error,omitempty"`
}

type SubmissionStatusError struct {
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	Retryable   bool           `json:"retryable"`
	NextAction  string         `json:"next_action"`
	Remediation string         `json:"remediation"`
	ReasonCode  string         `json:"reason_code,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}
