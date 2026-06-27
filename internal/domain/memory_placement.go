package domain

import "time"

type MemoryPlacementRunStatus string

const (
	MemoryPlacementQueued     MemoryPlacementRunStatus = "queued"
	MemoryPlacementProcessing MemoryPlacementRunStatus = "processing"
	MemoryPlacementCompleted  MemoryPlacementRunStatus = "completed"
	MemoryPlacementFailed     MemoryPlacementRunStatus = "failed"
)

type MemoryPlacementCategory string

const (
	MemoryPlacementFragmentOnly    MemoryPlacementCategory = "fragment_only"
	MemoryPlacementCandidateClaim  MemoryPlacementCategory = "candidate_claim"
	MemoryPlacementValidatedClaim  MemoryPlacementCategory = "validated_claim"
	MemoryPlacementPromotedFact    MemoryPlacementCategory = "promoted_fact"
	MemoryPlacementNeedsEvidence   MemoryPlacementCategory = "needs_more_evidence"
	MemoryPlacementRejectedFalse   MemoryPlacementCategory = "rejected_false"
	MemoryPlacementDisputeAccepted MemoryPlacementCategory = "accepted_promoted"
	MemoryPlacementDisputeRejected MemoryPlacementCategory = "rejected_explained"
)

type MemoryEvidence struct {
	Index          int            `json:"index"`
	Content        string         `json:"content"`
	Source         string         `json:"source,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Labels         []string       `json:"labels,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type MemoryPlacementRun struct {
	IngestID          string                   `json:"ingest_id"`
	ProfileID         string                   `json:"team_id,omitempty"`
	Status            MemoryPlacementRunStatus `json:"status"`
	CheckAfterSeconds int                      `json:"check_after_seconds"`
	StatusTool        string                   `json:"status_tool"`
	Evidence          []MemoryEvidence         `json:"evidence,omitempty"`
	Items             []MemoryPlacementItem    `json:"items,omitempty"`
	Error             string                   `json:"error,omitempty"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
	StartedAt         *time.Time               `json:"started_at,omitempty"`
	CompletedAt       *time.Time               `json:"completed_at,omitempty"`
}

type MemoryPlacementItem struct {
	ItemID        string                  `json:"item_id"`
	IngestID      string                  `json:"ingest_id,omitempty"`
	ProfileID     string                  `json:"team_id,omitempty"`
	EvidenceIndex int                     `json:"evidence_index"`
	FragmentID    string                  `json:"fragment_id,omitempty"`
	Category      MemoryPlacementCategory `json:"category"`
	Status        string                  `json:"status"`
	Reason        string                  `json:"reason,omitempty"`
	Error         string                  `json:"error,omitempty"`
	ClaimID       string                  `json:"claim_id,omitempty"`
	FactID        string                  `json:"fact_id,omitempty"`
	CreatedAt     time.Time               `json:"created_at"`
	UpdatedAt     time.Time               `json:"updated_at"`
}

type MemoryDisputeStatus string

const (
	MemoryDisputeOpen              MemoryDisputeStatus = "open"
	MemoryDisputeProcessing        MemoryDisputeStatus = "processing"
	MemoryDisputeAcceptedPromoted  MemoryDisputeStatus = "accepted_promoted"
	MemoryDisputeRejectedExplained MemoryDisputeStatus = "rejected_explained"
)

type MemoryDisputeTurn struct {
	At       time.Time        `json:"at"`
	Role     string           `json:"role"`
	Message  string           `json:"message,omitempty"`
	Evidence []MemoryEvidence `json:"evidence,omitempty"`
}

type MemoryDisputeSession struct {
	DisputeID       string              `json:"dispute_id"`
	ProfileID       string              `json:"team_id,omitempty"`
	IngestID        string              `json:"ingest_id"`
	PlacementItemID string              `json:"placement_item_id,omitempty"`
	Status          MemoryDisputeStatus `json:"status"`
	Turns           []MemoryDisputeTurn `json:"turns,omitempty"`
	FinalReason     string              `json:"final_reason,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
	CompletedAt     *time.Time          `json:"completed_at,omitempty"`
}
