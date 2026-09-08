package contract

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
