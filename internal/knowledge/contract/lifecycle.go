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
	DecisionID           string   `json:"-"`
	ProcessingState      string   `json:"processing_state"`
	RetractedEvidenceIDs []string `json:"retracted_evidence_ids"`
	Existing             bool     `json:"-"`
}
