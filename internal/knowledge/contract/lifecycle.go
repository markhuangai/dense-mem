package contract

import "github.com/markhuangai/dense-mem/internal/domain"

type RetractEvidenceInput struct {
	TeamID         string
	OwnerProfileID string
	EvidenceIDs    []string
	Reason         string
	IdempotencyKey string
	RequestHash    string
}

type EvidenceLifecycleResult struct {
	DecisionID                      string   `json:"-"`
	ProcessingState                 string   `json:"processing_state"`
	RetractedEvidenceIDs            []string `json:"retracted_evidence_ids"`
	AffectedRelationshipCount       int      `json:"affected_relationship_count"`
	PendingRelationshipCount        int      `json:"pending_relationship_count"`
	RetainedActiveRelationshipCount int      `json:"retained_active_relationship_count"`
	Existing                        bool     `json:"-"`
}

func StatusForEffectiveSupport(currentStatus string, supportCount int) string {
	if !relationshipStatusAllowsSupportRecompute(currentStatus) {
		return currentStatus
	}
	if supportCount == 0 {
		return string(domain.RelationshipStatusPendingEvidence)
	}
	return string(domain.RelationshipStatusActive)
}

func relationshipStatusAllowsSupportRecompute(status string) bool {
	switch status {
	case string(domain.RelationshipStatusActive), string(domain.RelationshipStatusPendingEvidence):
		return true
	default:
		return false
	}
}

func RelationshipEligibleForCorrection(record *RelationshipRecord) bool {
	return record != nil && record.IdentityAliasOfID == "" &&
		record.Status == string(domain.RelationshipStatusActive) && record.SupportCount != 0
}

func RelationshipEligibleForConflictPlacement(record *RelationshipRecord) bool {
	return record != nil && record.Status == string(domain.RelationshipStatusActive) &&
		record.SupportCount > 0 && record.RelationshipKind == string(domain.RelationshipKindState) &&
		record.CurrentCardinality == string(domain.CurrentCardinalityOne)
}
