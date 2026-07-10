package dto

import "time"

// RecallRequest represents query parameters for GET /api/v1/recall.
// The query parameter is the natural-language query; limit caps returned hits.
type RecallRequest struct {
	Query           string     `query:"query" validate:"required,max=512"`
	Limit           int        `query:"limit" validate:"min=0,max=50"`
	ValidAt         *time.Time `query:"valid_at"`
	KnownAt         *time.Time `query:"known_at"`
	IncludeEvidence bool       `query:"include_evidence"`
	UseCommunities  bool       `query:"use_communities"`
}

// RecallHitResponse is one ranked result returned by the recall endpoint.
//
// Tier indicates which knowledge-pipeline level produced this hit:
//   - "0.75", "1.25", "1.75" = V2 fact, validated-claim, and candidate assertions
//   - "1", "1.5", "2" = legacy fact, claim, and source-fragment hits
//
// SemanticRank, KeywordRank, and FinalScore are preserved for backward
// compatibility with clients that read the recall_memory tool output.
type RecallHitResponse struct {
	// Tier classifies the knowledge-pipeline level of this hit.
	Tier string `json:"tier,omitempty"`
	// Score is the normalised relevance score for this hit after tier weighting.
	Score float64 `json:"score,omitempty"`
	// Fragment is populated for tier-2 (SourceFragment) hits.
	Fragment *FragmentResponse `json:"fragment,omitempty"`
	// Claim is populated for tier-1.5 (validated Claim) hits.
	Claim *ClaimResponse `json:"claim,omitempty"`
	// Fact is populated for tier-1 (active Fact) hits.
	Fact *FactResponse `json:"fact,omitempty"`
	// Assertion is populated for semantic-edge V2 hits.
	Assertion *SemanticAssertionResponse `json:"assertion,omitempty"`
	// Paths contains the typed relationship and endpoint nodes for an assertion hit.
	Paths []SemanticPathResponse `json:"paths,omitempty"`
	// Frontier lists bounded next-hop relationships the caller may choose to traverse.
	Frontier []SemanticFrontierHintResponse `json:"frontier,omitempty"`
	// SemanticRank is 1-based rank from the semantic branch; 0 if absent.
	SemanticRank int `json:"semantic_rank"`
	// KeywordRank is 1-based rank from the keyword branch; 0 if absent.
	KeywordRank int `json:"keyword_rank"`
	// FinalScore is the Reciprocal Rank Fusion score (fragment hits only).
	FinalScore float64 `json:"final_score"`
}

type SemanticAssertionResponse struct {
	AssertionID       string                         `json:"assertion_id"`
	TeamID            string                         `json:"team_id"`
	OwnerProfileID    string                         `json:"owner_profile_id,omitempty"`
	SubjectEntityID   string                         `json:"subject_entity_id"`
	PredicateKey      string                         `json:"predicate_key"`
	RelationshipType  string                         `json:"relationship_type"`
	ObjectEntityID    string                         `json:"object_entity_id,omitempty"`
	ObjectValue       *SemanticTypedValueResponse    `json:"object_value,omitempty"`
	Tier              string                         `json:"tier"`
	Status            string                         `json:"status"`
	PolicyFamily      string                         `json:"policy_family"`
	Polarity          string                         `json:"polarity"`
	Modality          string                         `json:"modality"`
	ValidFrom         *time.Time                     `json:"valid_from,omitempty"`
	ValidTo           *time.Time                     `json:"valid_to,omitempty"`
	RecordedAt        time.Time                      `json:"recorded_at"`
	RecordedTo        *time.Time                     `json:"recorded_to,omitempty"`
	ExtractConf       float64                        `json:"extract_conf"`
	ResolutionConf    float64                        `json:"resolution_conf"`
	SourceQuality     float64                        `json:"source_quality"`
	SupportCount      int                            `json:"support_count"`
	SourceGroupCount  int                            `json:"source_group_count"`
	Evidence          []SemanticEvidenceSpanResponse `json:"evidence"`
	EmbeddingModel    string                         `json:"embedding_model,omitempty"`
	ExtractionModel   string                         `json:"extraction_model,omitempty"`
	ExtractionVersion string                         `json:"extraction_version,omitempty"`
	VerifierModel     string                         `json:"verifier_model,omitempty"`
	PipelineRunID     string                         `json:"pipeline_run_id,omitempty"`
	ProjectionVersion string                         `json:"projection_version"`
	CreatedAt         time.Time                      `json:"created_at"`
	UpdatedAt         time.Time                      `json:"updated_at"`
}

type SemanticTypedValueResponse struct {
	ValueID   string `json:"value_id"`
	ValueType string `json:"value_type"`
	Value     string `json:"value"`
	Display   string `json:"display,omitempty"`
	Unit      string `json:"unit,omitempty"`
}

type SemanticEvidenceSpanResponse struct {
	FragmentID  string `json:"fragment_id"`
	Start       int    `json:"start"`
	End         int    `json:"end"`
	SourceGroup string `json:"source_group"`
}

type SemanticNodeResponse struct {
	Key  string `json:"key"`
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

type SemanticEdgeResponse struct {
	AssertionID  string     `json:"assertion_id"`
	Source       string     `json:"source"`
	Target       string     `json:"target"`
	Relationship string     `json:"relationship"`
	Predicate    string     `json:"predicate"`
	Tier         string     `json:"tier"`
	Status       string     `json:"status"`
	Polarity     string     `json:"polarity"`
	ValidFrom    *time.Time `json:"valid_from,omitempty"`
	ValidTo      *time.Time `json:"valid_to,omitempty"`
	EvidenceIDs  []string   `json:"evidence_ids,omitempty"`
}

type SemanticPathResponse struct {
	Nodes []SemanticNodeResponse `json:"nodes"`
	Edges []SemanticEdgeResponse `json:"edges"`
}

type SemanticFrontierHintResponse struct {
	FromEntityID string               `json:"from_entity_id"`
	Direction    string               `json:"direction"`
	Relationship string               `json:"relationship"`
	AssertionID  string               `json:"assertion_id"`
	Neighbor     SemanticNodeResponse `json:"neighbor"`
	Tier         string               `json:"tier"`
}

// RecallResponse wraps the ranked list of recall hits.
type RecallResponse struct {
	Data []RecallHitResponse `json:"data"`
}
