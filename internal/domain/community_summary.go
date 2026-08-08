package domain

// CommunitySummaryInput is bounded graph context supplied to a summary
// provider. It contains no credentials, prompts, or durable policy fields.
type CommunitySummaryInput struct {
	CommunityID      string                         `json:"community_id"`
	SummaryInputHash string                         `json:"summary_input_hash"`
	Relationships    []CommunitySummaryRelationship `json:"relationships"`
}

type CommunitySummaryRelationship struct {
	RelationshipID string                         `json:"relationship_id"`
	EvidenceIDs    []string                       `json:"evidence_ids"`
	SupportQuotes  []CommunitySummarySupportQuote `json:"support_quotes"`
	Subject        string                         `json:"subject"`
	Predicate      string                         `json:"predicate"`
	Object         string                         `json:"object"`
}

type CommunitySummarySupportQuote struct {
	EvidenceID string `json:"evidence_id"`
	Quote      string `json:"quote"`
}

type CommunitySummary struct {
	Summary                 string
	TopEntities             []string
	TopPredicates           []string
	AdmittedRelationshipIDs []string
	AdmittedEvidenceIDs     []string
	AdmittedSupportQuotes   []CommunitySummarySupportQuote
	PromptHash              string
	ResponseHash            string
	InputHash               string
	ProviderModel           string
}
