package domain

import "time"

type MemoryPlacementRunStatus string

const (
	MemoryPlacementQueued         MemoryPlacementRunStatus = "queued"
	MemoryPlacementProcessing     MemoryPlacementRunStatus = "processing"
	MemoryPlacementAwaitingReview MemoryPlacementRunStatus = "awaiting_review"
	MemoryPlacementCompleted      MemoryPlacementRunStatus = "completed"
	MemoryPlacementFailed         MemoryPlacementRunStatus = "failed"
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
	MemoryPlacementAssertionCandidate  MemoryPlacementCategory = "assertion_candidate"
	MemoryPlacementAssertionValidated  MemoryPlacementCategory = "assertion_validated"
	MemoryPlacementAssertionFact       MemoryPlacementCategory = "assertion_fact"
	MemoryPlacementAssertionReview     MemoryPlacementCategory = "assertion_needs_review"
	MemoryPlacementAssertionQuarantine MemoryPlacementCategory = "assertion_quarantined"
	MemoryPlacementAssertionRejected   MemoryPlacementCategory = "assertion_rejected"
)

type MemoryEvidence struct {
	Index            int            `json:"index"`
	Content          string         `json:"content"`
	SourceType       string         `json:"source_type,omitempty"`
	Source           string         `json:"source,omitempty"`
	Authority        string         `json:"authority,omitempty"`
	TrustedAuthority bool           `json:"trusted_authority,omitempty"`
	SourceGroup      string         `json:"source_group,omitempty"`
	IdempotencyKey   string         `json:"idempotency_key,omitempty"`
	Labels           []string       `json:"labels,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type MemoryPlacementRun struct {
	IngestID          string                   `json:"ingest_id"`
	ProfileID         string                   `json:"team_id,omitempty"`
	ActorProfileID    string                   `json:"actor_profile_id,omitempty"`
	ActorRole         string                   `json:"actor_role,omitempty"`
	Status            MemoryPlacementRunStatus `json:"status"`
	CheckAfterSeconds int                      `json:"check_after_seconds"`
	StatusTool        string                   `json:"status_tool"`
	PipelineVersion   string                   `json:"pipeline_version,omitempty"`
	Evidence          []MemoryEvidence         `json:"evidence,omitempty"`
	Proposal          MemoryProposal           `json:"proposal"`
	Items             []MemoryPlacementItem    `json:"items,omitempty"`
	ReviewTasks       []MemoryReviewTask       `json:"review_tasks,omitempty"`
	Security          MemorySecurityAssessment `json:"security"`
	MigrationRefs     []LegacyMemoryRef        `json:"migration_refs,omitempty"`
	RequiresAck       bool                     `json:"requires_acknowledgement"`
	Error             string                   `json:"error,omitempty"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
	StartedAt         *time.Time               `json:"started_at,omitempty"`
	CompletedAt       *time.Time               `json:"completed_at,omitempty"`
	AcknowledgedAt    *time.Time               `json:"acknowledged_at,omitempty"`
}

type MemoryPlacementItem struct {
	ItemID               string                     `json:"item_id"`
	IngestID             string                     `json:"ingest_id,omitempty"`
	ProfileID            string                     `json:"team_id,omitempty"`
	EvidenceIndex        int                        `json:"evidence_index"`
	EvidenceIndexes      []int                      `json:"evidence_indexes,omitempty"`
	FragmentID           string                     `json:"fragment_id,omitempty"`
	FragmentIDs          []string                   `json:"fragment_ids,omitempty"`
	Category             MemoryPlacementCategory    `json:"category"`
	Status               string                     `json:"status"`
	Reason               string                     `json:"reason,omitempty"`
	Error                string                     `json:"error,omitempty"`
	ClaimID              string                     `json:"claim_id,omitempty"`
	FactID               string                     `json:"fact_id,omitempty"`
	AssertionID          string                     `json:"assertion_id,omitempty"`
	RelationshipType     string                     `json:"relationship_type,omitempty"`
	Tier                 AssertionTier              `json:"tier,omitempty"`
	AssertionStatus      AssertionStatus            `json:"assertion_status,omitempty"`
	PolicyFamily         AssertionPolicyFamily      `json:"policy_family,omitempty"`
	VerifierVerdict      string                     `json:"verifier_verdict,omitempty"`
	VerifierConfidence   float64                    `json:"verifier_confidence,omitempty"`
	ReviewTaskID         string                     `json:"review_task_id,omitempty"`
	ProposedRelationship MemoryRelationshipProposal `json:"proposed_relationship"`
	ReviewedRelationship MemoryRelationshipProposal `json:"reviewed_relationship"`
	SecuritySignals      []string                   `json:"security_signals,omitempty"`
	CreatedAt            time.Time                  `json:"created_at"`
	UpdatedAt            time.Time                  `json:"updated_at"`
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
