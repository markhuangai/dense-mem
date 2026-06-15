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
//   - "1"   = active Fact (highest authority)
//   - "1.5" = validated Claim
//   - "2"   = SourceFragment (raw evidence)
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
	// SemanticRank is 1-based rank from the semantic branch; 0 if absent.
	SemanticRank int `json:"semantic_rank"`
	// KeywordRank is 1-based rank from the keyword branch; 0 if absent.
	KeywordRank int `json:"keyword_rank"`
	// FinalScore is the Reciprocal Rank Fusion score (fragment hits only).
	FinalScore float64 `json:"final_score"`
}

// RecallResponse wraps the ranked list of recall hits.
type RecallResponse struct {
	Data []RecallHitResponse `json:"data"`
}
