package contract

import (
	"context"
	"encoding/json"
	"time"

	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
)

// Service is the recall application boundary. It returns a complete result
// or an error; callers do not need a second status contract.
type Service interface {
	Recall(context.Context, Request) (*RecallResult, error)
}

// Repository is the storage read port used by recall application services.
type Repository interface {
	RecallEvidence(context.Context, RecallEvidenceInput) (*RecallEvidenceResult, error)
	RecallRelationships(context.Context, RecallRelationshipsInput) (*RecallRelationshipsResult, error)
}

// SearchRepository is the narrow storage port required by the recall service.
// It intentionally does not force recall callers to implement unrelated
// full-text or vector search methods.
type SearchRepository interface {
	GetActiveSearchContract(context.Context) (*searchcontract.ActiveSearchContract, error)
	Repository
}

type RecallEvidenceInput struct {
	TeamID               string
	Query                string
	QueryEmbedding       []float32
	Limit                int
	ValidAt              *time.Time
	KnownAt              *time.Time
	KnownEvidenceIDs     []string
	KnownRelationshipIDs []string
	ExpandFromEntityIDs  []string
	SpaceID              string
	SpaceKind            string
}

type RecallRelationshipsInput struct {
	TeamID               string
	Query                string
	QueryEmbedding       []float32
	Limit                int
	ValidAt              *time.Time
	KnownAt              *time.Time
	KnownEvidenceIDs     []string
	KnownRelationshipIDs []string
	ExpandFromEntityIDs  []string
	ExcludedGroupKeys    []string
	SpaceID              string
	SpaceKind            string
}

type RecallEvidenceResult struct {
	TeamID            string
	SearchState       string
	Results           []RecallEvidenceHit
	Conflicts         []tracecontract.RelationshipConflictCaseRecord
	EvidenceConflicts []EvidenceConflictCaseRecord
}

type RecallRelationshipsResult struct {
	TeamID        string
	SearchState   string
	VectorOmitted bool
	Results       []RecallRelationshipHit
}

type RecallEvidenceHit struct {
	TeamID          string
	EvidenceID      string
	RelationshipIDs []string
	Context         string
	Source          string
	SourceType      string
	CreatedAt       time.Time
	Rank            int
	Score           float64
	SearchState     string
	SpaceKind       string
}

type RecallRelationshipHit struct {
	TeamID                    string
	RelationshipID            string
	SemanticGroupKey          string
	SubjectEntityID           string
	SubjectName               string
	PredicateKey              string
	ObjectEntityID            string
	ObjectValueID             string
	ObjectName                string
	ObjectValueType           string
	ObjectValue               string
	Polarity                  string
	ScopeKey                  string
	ValidFrom                 *time.Time
	Score                     float64
	Rank                      int
	SearchState               string
	SupportCount              int
	SourceGroupCount          int
	EvidenceIDs               []string
	EquivalentRelationshipIDs []string
	CreatedAt                 time.Time
	SpaceKind                 string
}

type RecallResult struct {
	RecallID             string                       `json:"recall_id"`
	Results              []RecallResultItem           `json:"results"`
	Conflicts            []RecallConflictSummary      `json:"conflicts"`
	RelatedRelationships []RelatedRelationshipSummary `json:"related_relationships"`
	RelatedCommunities   []RecallDiscoveryPath        `json:"related_communities"`
	RelatedHypotheses    []RelatedHypothesisSummary   `json:"related_hypotheses"`
	SearchStates         RecallSearchStates           `json:"search_states"`
	Degradations         []RecallDegradationResult    `json:"degradations"`
	SuggestedActions     []RecallSuggestedAction      `json:"suggested_actions"`

	DiscoveryPaths    []RecallDiscoveryPath    `json:"-"`
	DiscoveryGuidance string                   `json:"-"`
	Degradation       *RecallDegradationResult `json:"-"`
	SearchState       string                   `json:"-"`
}

type RecallSuggestedAction struct {
	Tool          string   `json:"tool"`
	Guidance      string   `json:"guidance"`
	RecallEventID string   `json:"recall_event_id,omitempty"`
	HypothesisIDs []string `json:"hypothesis_ids,omitempty"`
}

type RecallResultItem struct {
	EvidenceID      string     `json:"evidence_id"`
	RelationshipIDs []string   `json:"relationship_ids,omitempty"`
	Rank            int        `json:"rank"`
	Context         string     `json:"context,omitempty"`
	Source          string     `json:"source,omitempty"`
	SourceType      string     `json:"source_type,omitempty"`
	CreatedAt       *time.Time `json:"created_at,omitempty"`
	SpaceKind       string     `json:"space_kind,omitempty"`
}

type RecallDiscoveryPath struct {
	Relationships          []RecallRelationshipHandle   `json:"relationships"`
	EvidenceIDs            []string                     `json:"evidence_ids"`
	CommunityID            string                       `json:"community_id,omitempty"`
	LogicalCommunityID     string                       `json:"logical_community_id,omitempty"`
	Rank                   int                          `json:"rank,omitempty"`
	Summary                string                       `json:"summary,omitempty"`
	TopEntities            []EntityHandle               `json:"top_entities,omitempty"`
	TopPredicates          []string                     `json:"top_predicates,omitempty"`
	EntityCount            int                          `json:"entity_count,omitempty"`
	RelationshipCount      int                          `json:"relationship_count,omitempty"`
	CommunityRelationships []RelatedRelationshipSummary `json:"-"`
	RelationshipsTruncated bool                         `json:"relationships_truncated,omitempty"`
}

// MarshalJSON keeps the internal fused path representation out of the public
// result while preserving the existing community and relationship shapes.
func (p RecallDiscoveryPath) MarshalJSON() ([]byte, error) {
	if p.CommunityID != "" {
		return json.Marshal(struct {
			CommunityID            string                       `json:"community_id"`
			LogicalCommunityID     string                       `json:"logical_community_id"`
			Rank                   int                          `json:"rank"`
			Summary                string                       `json:"summary"`
			TopEntities            []EntityHandle               `json:"top_entities"`
			TopPredicates          []string                     `json:"top_predicates"`
			EntityCount            int                          `json:"entity_count"`
			RelationshipCount      int                          `json:"relationship_count"`
			Relationships          []RelatedRelationshipSummary `json:"relationships"`
			RelationshipsTruncated bool                         `json:"relationships_truncated"`
		}{p.CommunityID, p.LogicalCommunityID, p.Rank, p.Summary, p.TopEntities, p.TopPredicates, p.EntityCount, p.RelationshipCount, p.CommunityRelationships, p.RelationshipsTruncated})
	}
	return json.Marshal(struct {
		Relationships []RecallRelationshipHandle `json:"relationships"`
		EvidenceIDs   []string                   `json:"evidence_ids"`
	}{p.Relationships, p.EvidenceIDs})
}

type RecallCommunity struct {
	CommunityID            string                       `json:"community_id"`
	LogicalCommunityID     string                       `json:"logical_community_id"`
	Rank                   int                          `json:"rank"`
	Summary                string                       `json:"summary"`
	TopEntities            []EntityHandle               `json:"top_entities"`
	TopPredicates          []string                     `json:"top_predicates"`
	EntityCount            int                          `json:"entity_count"`
	RelationshipCount      int                          `json:"relationship_count"`
	Relationships          []RelatedRelationshipSummary `json:"relationships"`
	RelationshipsTruncated bool                         `json:"relationships_truncated"`
}

type RecallConflictPosition struct {
	PositionID          string                    `json:"position_id"`
	Disposition         string                    `json:"disposition"`
	EvidenceID          string                    `json:"evidence_id,omitempty"`
	OccurrenceID        string                    `json:"occurrence_id,omitempty"`
	Quote               string                    `json:"quote,omitempty"`
	SpanStart           int                       `json:"span_start"`
	SpanEnd             int                       `json:"span_end"`
	Authority           string                    `json:"authority,omitempty"`
	Submitted           bool                      `json:"submitted,omitempty"`
	SupporterCount      int                       `json:"supporter_count"`
	SupportersTruncated bool                      `json:"supporters_truncated"`
	Supporters          []RecallConflictSupporter `json:"supporters"`
	RelationshipIDs     []string                  `json:"relationship_ids"`
	OwnerProfileIDs     []string                  `json:"owner_profile_ids"`
	ResultEvidenceIDs   []string                  `json:"result_evidence_ids"`
}

type RecallConflictSummary struct {
	ConflictID          string                   `json:"conflict_id"`
	Version             int                      `json:"version"`
	Kind                string                   `json:"kind"`
	Status              string                   `json:"status"`
	Question            string                   `json:"question"`
	ReviewDueAt         *time.Time               `json:"review_due_at"`
	EffectiveAt         *time.Time               `json:"effective_at"`
	EffectiveTimeBasis  string                   `json:"effective_time_basis,omitempty"`
	PreferredPositionID string                   `json:"preferred_position_id,omitempty"`
	Positions           []RecallConflictPosition `json:"positions"`
	PositionsTruncated  bool                     `json:"positions_truncated"`
}

// MarshalJSON keeps the historical relationship-conflict shape while
// exposing exact occurrence spans only for evidence conflicts.
func (s RecallConflictSummary) MarshalJSON() ([]byte, error) {
	if s.Kind == "evidence_conflict" {
		positions := make([]evidenceConflictPositionJSON, 0, len(s.Positions))
		for _, position := range s.Positions {
			positions = append(positions, evidenceConflictPositionJSON{
				PositionID: position.PositionID, Disposition: position.Disposition,
				EvidenceID: position.EvidenceID, OccurrenceID: position.OccurrenceID,
				Quote: position.Quote, SpanStart: position.SpanStart, SpanEnd: position.SpanEnd,
				Authority: position.Authority, Submitted: position.Submitted,
			})
		}
		var preferred *string
		if s.PreferredPositionID != "" {
			value := s.PreferredPositionID
			preferred = &value
		}
		return json.Marshal(evidenceConflictSummaryJSON{
			ConflictID: s.ConflictID, Version: s.Version, Kind: s.Kind, Status: s.Status,
			PreferredPositionID: preferred, Positions: positions, PositionsTruncated: s.PositionsTruncated,
		})
	}
	positions := make([]relationshipConflictPositionJSON, 0, len(s.Positions))
	for _, position := range s.Positions {
		positions = append(positions, relationshipConflictPositionJSON{
			PositionID: position.PositionID, Disposition: position.Disposition,
			SupporterCount: position.SupporterCount, SupportersTruncated: position.SupportersTruncated,
			Supporters: position.Supporters, RelationshipIDs: position.RelationshipIDs,
			OwnerProfileIDs: position.OwnerProfileIDs, ResultEvidenceIDs: position.ResultEvidenceIDs,
		})
	}
	return json.Marshal(relationshipConflictSummaryJSON{
		ConflictID: s.ConflictID, Version: s.Version, Kind: s.Kind, Status: s.Status,
		Question: s.Question, ReviewDueAt: s.ReviewDueAt, EffectiveAt: s.EffectiveAt,
		EffectiveTimeBasis: s.EffectiveTimeBasis, PreferredPositionID: s.PreferredPositionID,
		Positions: positions, PositionsTruncated: s.PositionsTruncated,
	})
}

type relationshipConflictSummaryJSON struct {
	ConflictID          string                             `json:"conflict_id"`
	Version             int                                `json:"version"`
	Kind                string                             `json:"kind"`
	Status              string                             `json:"status"`
	Question            string                             `json:"question"`
	ReviewDueAt         *time.Time                         `json:"review_due_at"`
	EffectiveAt         *time.Time                         `json:"effective_at"`
	EffectiveTimeBasis  string                             `json:"effective_time_basis,omitempty"`
	PreferredPositionID string                             `json:"preferred_position_id,omitempty"`
	Positions           []relationshipConflictPositionJSON `json:"positions"`
	PositionsTruncated  bool                               `json:"positions_truncated"`
}

type relationshipConflictPositionJSON struct {
	PositionID          string                    `json:"position_id"`
	Disposition         string                    `json:"disposition"`
	SupporterCount      int                       `json:"supporter_count"`
	SupportersTruncated bool                      `json:"supporters_truncated"`
	Supporters          []RecallConflictSupporter `json:"supporters"`
	RelationshipIDs     []string                  `json:"relationship_ids"`
	OwnerProfileIDs     []string                  `json:"owner_profile_ids"`
	ResultEvidenceIDs   []string                  `json:"result_evidence_ids"`
}

type evidenceConflictSummaryJSON struct {
	ConflictID          string                         `json:"conflict_id"`
	Version             int                            `json:"version"`
	Kind                string                         `json:"kind"`
	Status              string                         `json:"status"`
	PreferredPositionID *string                        `json:"preferred_position_id"`
	Positions           []evidenceConflictPositionJSON `json:"positions"`
	PositionsTruncated  bool                           `json:"positions_truncated"`
}

type evidenceConflictPositionJSON struct {
	PositionID   string `json:"position_id"`
	Disposition  string `json:"disposition"`
	EvidenceID   string `json:"evidence_id"`
	OccurrenceID string `json:"occurrence_id"`
	Quote        string `json:"quote"`
	SpanStart    int    `json:"span_start"`
	SpanEnd      int    `json:"span_end"`
	Authority    string `json:"authority"`
	Submitted    bool   `json:"submitted"`
}

type RecallConflictSupporter struct {
	ProfileID          string    `json:"profile_id"`
	ProfileName        string    `json:"profile_name"`
	StrongestAuthority string    `json:"strongest_authority"`
	EvidenceID         string    `json:"evidence_id"`
	AcceptedAt         time.Time `json:"accepted_at"`
}

type RecallRelationshipHandle struct {
	RelationshipID string         `json:"relationship_id"`
	Subject        EntityHandle   `json:"subject"`
	Predicate      string         `json:"predicate"`
	Object         SemanticObject `json:"object"`
	Polarity       string         `json:"polarity"`
}

type RelatedRelationshipSummary struct {
	RelationshipID            string         `json:"relationship_id"`
	EquivalentRelationshipIDs []string       `json:"equivalent_relationship_ids"`
	SemanticGroupKey          string         `json:"-"`
	Subject                   EntityHandle   `json:"subject"`
	Predicate                 string         `json:"predicate"`
	Object                    SemanticObject `json:"object"`
	Polarity                  string         `json:"polarity"`
	EvidenceIDs               []string       `json:"evidence_ids"`
	SearchState               string         `json:"search_state,omitempty"`
	SpaceKind                 string         `json:"space_kind,omitempty"`
}

type EntityHandle struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
}

type SemanticObject struct {
	EntityID string `json:"entity_id,omitempty"`
	ValueID  string `json:"value_id,omitempty"`
	Name     string `json:"name,omitempty"`
	Type     string `json:"type,omitempty"`
	Value    any    `json:"value,omitempty"`
	Display  string `json:"display,omitempty"`
	Unit     string `json:"unit,omitempty"`
}

type RelatedHypothesisSummary struct {
	HypothesisID          string    `json:"hypothesis_id"`
	SubjectEntityID       string    `json:"subject_entity_id"`
	PredicateKey          string    `json:"predicate_key"`
	ObjectEntityID        string    `json:"object_entity_id,omitempty"`
	ObjectValueID         string    `json:"object_value_id,omitempty"`
	Statement             string    `json:"statement"`
	Status                string    `json:"status"`
	SourceRelationshipIDs []string  `json:"source_relationship_ids"`
	SourceEvidenceIDs     []string  `json:"source_evidence_ids"`
	Lane                  string    `json:"lane"`
	GeneratorKind         string    `json:"generator_kind"`
	GeneratorVersion      string    `json:"generator_version"`
	CreatedAt             time.Time `json:"created_at"`
}

type RecallDegradationResult struct {
	Frontier        string `json:"frontier,omitempty"`
	RequiredFailure bool   `json:"required_failure,omitempty"`
	Optional        bool   `json:"optional,omitempty"`
	Code            string `json:"code"`
	Message         string `json:"message"`
}

type RecallSearchStates struct {
	Evidence      string `json:"evidence"`
	Relationships string `json:"relationships"`
}
