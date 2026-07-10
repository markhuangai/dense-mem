package domain

import "time"

type MemoryEntityProposal struct {
	Ref     string   `json:"ref"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Aliases []string `json:"aliases,omitempty"`
}

type MemoryValueProposal struct {
	Type    ValueType `json:"type"`
	Value   string    `json:"value"`
	Display string    `json:"display,omitempty"`
	Unit    string    `json:"unit,omitempty"`
}

type MemoryEvidenceRef struct {
	EvidenceIndex int `json:"evidence_index"`
	Start         int `json:"start"`
	End           int `json:"end"`
}

type MemoryRelationshipProposal struct {
	ProposalID    string                `json:"proposal_id"`
	SubjectRef    string                `json:"subject_ref"`
	Predicate     string                `json:"predicate"`
	ObjectRef     string                `json:"object_ref,omitempty"`
	ObjectValue   *MemoryValueProposal  `json:"object_value,omitempty"`
	PolicyFamily  AssertionPolicyFamily `json:"policy_family"`
	Polarity      ClaimPolarity         `json:"polarity"`
	Modality      ClaimModality         `json:"modality"`
	ValidFrom     *time.Time            `json:"valid_from,omitempty"`
	ValidTo       *time.Time            `json:"valid_to,omitempty"`
	Evidence      []MemoryEvidenceRef   `json:"evidence"`
	ClientComment string                `json:"client_comment,omitempty"`
}

type MemoryProposal struct {
	Entities      []MemoryEntityProposal       `json:"entities"`
	Relationships []MemoryRelationshipProposal `json:"relationships"`
}

type MemoryReviewTask struct {
	TaskID          string                      `json:"task_id"`
	PlacementItemID string                      `json:"placement_item_id"`
	Type            string                      `json:"type"`
	Question        string                      `json:"question"`
	Options         []string                    `json:"options,omitempty"`
	Proposal        *MemoryRelationshipProposal `json:"proposal,omitempty"`
}

type MemorySecurityAssessment struct {
	Quarantined bool     `json:"quarantined"`
	Signals     []string `json:"signals,omitempty"`
}

type LegacyMemoryRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type AssertionTransitionEvent struct {
	EventID     string          `json:"event_id"`
	ProfileID   string          `json:"team_id"`
	IngestID    string          `json:"ingest_id,omitempty"`
	ItemID      string          `json:"placement_item_id,omitempty"`
	AssertionID string          `json:"assertion_id,omitempty"`
	EventType   string          `json:"event_type"`
	FromTier    AssertionTier   `json:"from_tier,omitempty"`
	ToTier      AssertionTier   `json:"to_tier,omitempty"`
	FromStatus  AssertionStatus `json:"from_status,omitempty"`
	ToStatus    AssertionStatus `json:"to_status,omitempty"`
	ReasonCode  string          `json:"reason_code"`
	Source      string          `json:"source"`
	OccurredAt  time.Time       `json:"occurred_at"`
}
