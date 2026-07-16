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
	MemoryPlacementFragmentOnly        MemoryPlacementCategory = "fragment_only"
	MemoryPlacementCandidateClaim      MemoryPlacementCategory = "candidate_claim"
	MemoryPlacementValidatedClaim      MemoryPlacementCategory = "validated_claim"
	MemoryPlacementPromotedFact        MemoryPlacementCategory = "promoted_fact"
	MemoryPlacementNeedsEvidence       MemoryPlacementCategory = "needs_more_evidence"
	MemoryPlacementRejectedFalse       MemoryPlacementCategory = "rejected_false"
	MemoryPlacementDisputeAccepted     MemoryPlacementCategory = "accepted_promoted"
	MemoryPlacementDisputeRejected     MemoryPlacementCategory = "rejected_explained"
	MemoryPlacementEvidenceProcessed   MemoryPlacementCategory = "evidence_processed"
	MemoryPlacementEvidenceQuarantined MemoryPlacementCategory = "evidence_quarantined"
	MemoryPlacementProcessingFailed    MemoryPlacementCategory = "processing_failed"
)

type MemoryEvidence struct {
	Index          int            `json:"index"`
	Content        string         `json:"content"`
	SourceType     string         `json:"source_type,omitempty"`
	Source         string         `json:"source,omitempty"`
	Authority      string         `json:"authority,omitempty"`
	SourceGroup    string         `json:"source_group,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Labels         []string       `json:"labels,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type MemoryPlacementRun struct {
	IngestID          string                   `json:"ingest_id"`
	ProfileID         string                   `json:"team_id,omitempty"`
	OwnerProfileID    string                   `json:"owner_profile_id,omitempty"`
	Status            MemoryPlacementRunStatus `json:"status"`
	CheckAfterSeconds int                      `json:"check_after_seconds"`
	StatusTool        string                   `json:"status_tool"`
	Attempts          int                      `json:"attempts"`
	Evidence          []MemoryEvidence         `json:"evidence,omitempty"`
	Items             []MemoryPlacementItem    `json:"items,omitempty"`
	Error             string                   `json:"error,omitempty"`
	AvailableAt       time.Time                `json:"available_at"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
	StartedAt         *time.Time               `json:"started_at,omitempty"`
	CompletedAt       *time.Time               `json:"completed_at,omitempty"`
}

type MemoryPlacementItem struct {
	ItemID               string                               `json:"item_id"`
	IngestID             string                               `json:"ingest_id,omitempty"`
	ProfileID            string                               `json:"team_id,omitempty"`
	OwnerProfileID       string                               `json:"owner_profile_id,omitempty"`
	EvidenceIndex        int                                  `json:"evidence_index"`
	FragmentID           string                               `json:"fragment_id,omitempty"`
	Category             MemoryPlacementCategory              `json:"category"`
	Status               string                               `json:"status"`
	Reason               string                               `json:"reason,omitempty"`
	Error                string                               `json:"error,omitempty"`
	ClaimID              string                               `json:"claim_id,omitempty"`
	FactID               string                               `json:"fact_id,omitempty"`
	RelationshipOutcomes []MemoryPlacementRelationshipOutcome `json:"relationship_outcomes,omitempty"`
	CreatedAt            time.Time                            `json:"created_at"`
	UpdatedAt            time.Time                            `json:"updated_at"`
}

type MemoryPlacementRelationshipOutcome struct {
	ProposalID         string                     `json:"proposal_id,omitempty"`
	ObservationID      string                     `json:"observation_id,omitempty"`
	RelationshipID     string                     `json:"relationship_id,omitempty"`
	OwnerProfileID     string                     `json:"owner_profile_id,omitempty"`
	Tier               SemanticRelationshipTier   `json:"tier,omitempty"`
	RelationshipStatus SemanticRelationshipStatus `json:"relationship_status,omitempty"`
	Category           string                     `json:"category"`
	Reason             string                     `json:"reason,omitempty"`
	ReviewTask         *MemoryPlacementReviewTask `json:"review_task,omitempty"`
}

type MemoryPlacementReviewTask struct {
	Type    string   `json:"type"`
	Message string   `json:"message"`
	Options []string `json:"options,omitempty"`
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
