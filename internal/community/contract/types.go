package contract

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

const (
	CommunityAlgorithmKind    = "louvain"
	CommunityAlgorithmVersion = "v2"
	CommunityProfileVersion   = "postgres-v2"
)

var (
	ErrCommunityRunAlreadyClaimed = errors.New("community run already claimed")
	ErrCommunityNotFound          = errors.New("community not found")
	ErrCommunitySourceStale       = errors.New("community source is stale")
)

type CommunityRepository interface {
	ClaimCommunityRun(ctx context.Context, input CommunityRunClaimInput) (*CommunityRun, error)
	RenewCommunityRunLease(ctx context.Context, input CommunityRunLeaseInput) error
	CompleteCommunityRun(ctx context.Context, input CommunityRunCompleteInput) error
	ListCommunityInputs(ctx context.Context, input CommunityInputListInput) ([]CommunityInput, error)
	PublishCommunitySnapshot(ctx context.Context, input CommunitySnapshotPublishInput) error
	RefreshCommunityStaleness(ctx context.Context, input CommunityStalenessInput) (int, error)
	ListCommunities(ctx context.Context, input CommunityListInput) ([]CommunityRecord, error)
	CountCurrentCommunities(ctx context.Context, teamID string) (int, error)
	GetCommunity(ctx context.Context, input CommunityGetInput) (*CommunityRecord, error)
	RecallCommunityDiscovery(ctx context.Context, input CommunityDiscoveryInput) ([]CommunityDiscoveryPath, error)
	LatestCommunityRun(ctx context.Context, teamID string) (*CommunityRun, error)
	ListCurrentCommunityLineage(ctx context.Context, teamID string) ([]CommunityLineageRecord, error)
	ListCommunitySemanticGroups(ctx context.Context, input CommunityCoverageInput) ([]string, error)
	RecallCommunities(ctx context.Context, input CommunityRecallInput) ([]CommunityRecallRecord, error)
	RecordCommunitySummaryAttempt(ctx context.Context, input CommunitySummaryAttemptInput) error
}

type CommunityRunClaimInput struct {
	TeamID            string
	WindowKey         string
	LeaseUntil        time.Time
	AlgorithmKind     string
	AlgorithmVersion  string
	ProfileVersion    string
	ConfigurationHash string
	SourceFingerprint string
	MaxNodes          int
	MaxEdges          int
}

type CommunityRunLeaseInput struct {
	TeamID     string
	RunID      string
	LeaseUntil time.Time
}

type CommunityRunCompleteInput struct {
	TeamID         string
	RunID          string
	Status         string
	NodeCount      int
	EdgeCount      int
	CommunityCount int
	Error          string
}

type CommunityRun struct {
	TeamID            string
	RunID             string
	WindowKey         string
	Status            string
	AlgorithmKind     string
	AlgorithmVersion  string
	ProfileVersion    string
	ConfigurationHash string
	SourceFingerprint string
	NodeCount         int
	EdgeCount         int
	CommunityCount    int
	MaxNodes          int
	MaxEdges          int
	Error             string
	StartedAt         time.Time
	CompletedAt       *time.Time
	Claimed           bool
}

type CommunityInputListInput struct {
	TeamID string
	Limit  int
}

type CommunityInput struct {
	RelationshipID   string
	OwnerProfileID   string
	Version          int
	SubjectEntityID  string
	SubjectName      string
	PredicateKey     string
	PredicateVersion int
	ObjectEntityID   string
	ObjectName       string
	ObjectValueID    string
	ObjectValueType  string
	ObjectValue      string
	SemanticGroupKey string
	EvidenceIDs      []string
	EvidenceQuotes   []domain.CommunitySummarySupportQuote
}

type CommunitySnapshotPublishInput struct {
	TeamID            string
	RunID             string
	AlgorithmKind     string
	AlgorithmVersion  string
	ProfileVersion    string
	ConfigurationHash string
	SourceFingerprint string
	SourceSnapshot    []map[string]any
	NodeCount         int
	EdgeCount         int
	Communities       []CommunityPublishRecord
}

type CommunityPublishRecord struct {
	CommunityID          string
	LogicalCommunityID   string
	Ordinal              int
	Summary              string
	SummaryVersion       string
	MemberCount          int
	SourceCount          int
	TopEntities          []string
	TopPredicates        []string
	SourceFingerprint    string
	SummaryInputHash     string
	SummaryProviderModel string
	SummaryPromptHash    string
	SummaryResponseHash  string
	Memberships          []CommunityMembershipInput
	Sources              []CommunitySourceInput
}

type CommunityMembershipInput struct {
	EntityID        string
	Rank            int
	MembershipScore float64
	SourceCount     int
}

type CommunitySourceInput struct {
	RelationshipID      string
	OwnerProfileID      string
	RelationshipVersion int
	SourceRank          int
	SemanticGroupKey    string
	SourceStateHash     string
}

type CommunityLineageRecord struct {
	CommunityID          string
	LogicalCommunityID   string
	GroupKeys            []string
	SummaryInputHash     string
	Summary              string
	SummaryVersion       string
	SummaryProviderModel string
	SummaryPromptHash    string
	SummaryResponseHash  string
}

type CommunityRecallInput struct {
	TeamID               string
	Query                string
	ValidAt              *time.Time
	KnownAt              *time.Time
	ReturnedEvidenceIDs  []string
	KnownEvidenceIDs     []string
	KnownRelationshipIDs []string
	SeedRelationshipIDs  []string
	ExpandFromEntityIDs  []string
	ExcludedGroupKeys    []string
	CoveredGroupKeys     []string
	Limit                int
	RelationshipLimit    int
}

type CommunityCoverageInput struct {
	TeamID          string
	EvidenceIDs     []string
	RelationshipIDs []string
}

type CommunityRecallTopEntity struct {
	EntityID string
	Name     string
}

type CommunityRecallRecord struct {
	CommunityID            string
	LogicalCommunityID     string
	Rank                   int
	Summary                string
	TopEntities            []CommunityRecallTopEntity
	TopPredicates          []string
	EntityCount            int
	RelationshipCount      int
	Relationships          []RecallRelationshipHit
	RelationshipsTruncated bool
}

type RecallRelationshipHit = recallcontract.RecallRelationshipHit

type CommunitySummaryAttemptInput struct {
	TeamID                  string
	RunID                   string
	CommunityID             string
	Attempt                 int
	ProviderModel           string
	PromptHash              string
	ResponseHash            string
	InputHash               string
	AdmittedRelationshipIDs []string
	AdmittedEvidenceIDs     []string
	AdmittedSupportQuotes   []domain.CommunitySummarySupportQuote
	ResponseSummary         string
	Valid                   bool
	ErrorCode               string
}

type CommunityStalenessInput struct {
	TeamID string
	Limit  int
}

type CommunityListInput struct {
	TeamID string
	Status string
	Limit  int
}

type CommunityGetInput struct {
	TeamID      string
	CommunityID string
}

type CommunityDiscoveryInput struct {
	TeamID               string
	Query                string
	ValidAt              *time.Time
	KnownAt              *time.Time
	KnownRelationshipIDs []string
	ExpandFromEntityIDs  []string
	Limit                int
}

type CommunityDiscoveryPath struct {
	CommunityID   string
	Relationship  CommunityDiscoveryRelationship
	EvidenceIDs   []string
	SourceRank    int
	CommunityRank int
}

type CommunityDiscoveryRelationship struct {
	RelationshipID  string
	SubjectEntityID string
	SubjectName     string
	PredicateKey    string
	ObjectEntityID  string
	ObjectName      string
	Polarity        string
}

type CommunityRecord struct {
	TeamID             string
	CommunityID        string
	LogicalCommunityID string
	RunID              string
	Ordinal            int
	Status             string
	Summary            string
	SummaryVersion     string
	MemberCount        int
	SourceCount        int
	TopEntities        []string
	TopPredicates      []string
	SourceFingerprint  string
	StaleReason        string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	SupersededAt       *time.Time
	GroupKeys          []string
}
