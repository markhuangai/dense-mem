package contract

import (
	"context"
	"errors"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	ontologycontract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	"time"
)

var ErrTopicUnavailable = errors.New("topic_unavailable")
var ErrInvalidInput = errors.New("recall: invalid input")

const MaxRecallCandidateCount = 200

type Service interface {
	Recall(context.Context, Request) (*RecallResult, error)
}
type Repository interface {
	ReadEvidenceCandidates(context.Context, RecallEvidenceInput, *searchcontract.ActiveSearchContract, int) (*RecallCandidateBatch, error)
	HydrateEvidence(context.Context, RecallEvidenceInput, *searchcontract.ActiveSearchContract, []string) (map[string]RecallEvidenceHit, error)
	LoadRecallConflicts(context.Context, RecallEvidenceInput, []RecallEvidenceHit) (*RecallConflicts, error)
}
type SearchRepository interface {
	GetActiveSearchContract(context.Context) (*searchcontract.ActiveSearchContract, error)
	Repository
}
type EvidenceSnapshotRepository interface {
	WithEvidenceSnapshot(context.Context, string, func(SearchRepository) error) error
}
type OrganizedSearchRepository interface {
	HydrateOrganizedEvidence(context.Context, RecallEvidenceInput, *searchcontract.ActiveSearchContract, []string) (*EvidenceHydration, error)
}
type TopicDiscoveryRepository interface {
	RelatedEvidenceTopics(context.Context, RecallEvidenceInput, []string, int) (RelatedTopicResult, error)
}
type RecallConflicts struct{ Evidence []EvidenceConflictCaseRecord }
type RelatedTopic struct {
	TopicID                 string `json:"topic_id"`
	Name                    string `json:"name"`
	Description             string `json:"description"`
	EligibleEvidenceCount   int    `json:"eligible_evidence_count"`
	Rank                    int    `json:"rank"`
	MatchingResultCount     int    `json:"-"`
	AdditionalEvidenceCount int    `json:"-"`
}
type RelatedTopicResult struct {
	Topics      []RelatedTopic
	Truncated   bool
	Diagnostics map[string]any
}

type RecallCandidateBatch struct {
	TextHits                []searchcontract.SearchHit
	VectorHits              []searchcontract.SearchHit
	ExpansionHits           []searchcontract.SearchHit
	OntologyHits            []searchcontract.SearchHit
	SearchState             string
	OrganizationDegradation string
}

type EvidenceHydration struct {
	Hits                    map[string]RecallEvidenceHit
	Groups                  []ontologycontract.RecallEvidenceGroup
	OrganizationDegradation string
}

type RecallEvidenceInput struct {
	TopicIDs            []string
	TopicEvidenceIDs    []string
	OrganizationEnabled bool
	OrganizationSources []ontologycontract.SourceHandle
	TeamID              string
	Query               string
	QueryEmbedding      []float32
	Limit               int
	ValidAt             *time.Time
	KnownAt             *time.Time
	KnownEvidenceIDs    []string
	SpaceID             string
	SpaceKind           string
}

type RecallEvidenceResult struct {
	DecisionStages    []modelprovider.DecisionStage
	Degradations      []RecallDegradationResult
	TeamID            string
	SearchState       string
	Results           []RecallEvidenceHit
	EvidenceConflicts []EvidenceConflictCaseRecord
}

type RecallEvidenceHit struct {
	ContextTruncated      bool
	EquivalentEvidenceIDs []string
	EquivalentsTruncated  bool
	TeamID                string
	EvidenceID            string
	Context               string
	Source                string
	SourceType            string
	CreatedAt             time.Time
	Rank                  int
	Score                 float64
	SearchState           string
	SpaceKind             string
}

type RecallResult struct {
	DecisionStages  []modelprovider.DecisionStage `json:"-"`
	RelatedTopics   []RelatedTopic                `json:"related_topics"`
	TopicsTruncated bool                          `json:"topics_truncated"`
	RecallID        string                        `json:"recall_id"`
	Results         []RecallResultItem            `json:"results"`
	Conflicts       []RecallConflictSummary       `json:"conflicts"`

	SearchStates     RecallSearchStates        `json:"search_states"`
	Degradations     []RecallDegradationResult `json:"degradations"`
	SuggestedActions []RecallSuggestedAction   `json:"suggested_actions"`

	Degradation *RecallDegradationResult `json:"-"`
	SearchState string                   `json:"-"`
}

type RecallSuggestedAction struct {
	Tool          string `json:"tool"`
	Guidance      string `json:"guidance"`
	RecallEventID string `json:"recall_event_id,omitempty"`
}

type RecallResultItem struct {
	ContextTruncated      bool       `json:"context_truncated,omitempty"`
	EquivalentEvidenceIDs []string   `json:"equivalent_evidence_ids"`
	EquivalentsTruncated  bool       `json:"equivalents_truncated"`
	EvidenceID            string     `json:"evidence_id"`
	Rank                  int        `json:"rank"`
	Context               string     `json:"context,omitempty"`
	Source                string     `json:"source,omitempty"`
	SourceType            string     `json:"source_type,omitempty"`
	CreatedAt             *time.Time `json:"created_at,omitempty"`
	SpaceKind             string     `json:"space_kind,omitempty"`
}

type RecallConflictPosition struct {
	PositionID   string `json:"position_id"`
	Disposition  string `json:"disposition"`
	EvidenceID   string `json:"evidence_id,omitempty"`
	OccurrenceID string `json:"occurrence_id,omitempty"`
	Quote        string `json:"quote,omitempty"`
	SpanStart    int    `json:"span_start"`
	SpanEnd      int    `json:"span_end"`
	Authority    string `json:"authority,omitempty"`
	Submitted    bool   `json:"submitted,omitempty"`
}

type RecallConflictSummary struct {
	ConflictID          string                   `json:"conflict_id"`
	Version             int                      `json:"version"`
	Kind                string                   `json:"kind"`
	Status              string                   `json:"status"`
	PreferredPositionID string                   `json:"preferred_position_id,omitempty"`
	Positions           []RecallConflictPosition `json:"positions"`
	PositionsTruncated  bool                     `json:"positions_truncated"`
}

type RecallDegradationResult struct {
	Frontier        string `json:"frontier,omitempty"`
	RequiredFailure bool   `json:"required_failure,omitempty"`
	Optional        bool   `json:"optional,omitempty"`
	Code            string `json:"code"`
	Message         string `json:"message"`
}

type RecallSearchStates struct {
	Evidence string `json:"evidence"`
}
