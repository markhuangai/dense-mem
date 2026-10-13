package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type (
	CommunityRepository            = communitycontract.CommunityRepository
	CommunityRunClaimInput         = communitycontract.CommunityRunClaimInput
	CommunityRunLeaseInput         = communitycontract.CommunityRunLeaseInput
	CommunityRunCompleteInput      = communitycontract.CommunityRunCompleteInput
	CommunityRun                   = communitycontract.CommunityRun
	CommunityInputListInput        = communitycontract.CommunityInputListInput
	CommunityInput                 = communitycontract.CommunityInput
	CommunitySnapshotPublishInput  = communitycontract.CommunitySnapshotPublishInput
	CommunityPublishRecord         = communitycontract.CommunityPublishRecord
	CommunityMembershipInput       = communitycontract.CommunityMembershipInput
	CommunitySourceInput           = communitycontract.CommunitySourceInput
	CommunityLineageRecord         = communitycontract.CommunityLineageRecord
	CommunityRecallInput           = communitycontract.CommunityRecallInput
	CommunityCoverageInput         = communitycontract.CommunityCoverageInput
	CommunityRecallTopEntity       = communitycontract.CommunityRecallTopEntity
	CommunityRecallRecord          = communitycontract.CommunityRecallRecord
	RecallRelationshipHit          = communitycontract.RecallRelationshipHit
	CommunitySummaryAttemptInput   = communitycontract.CommunitySummaryAttemptInput
	CommunityStalenessInput        = communitycontract.CommunityStalenessInput
	CommunityListInput             = communitycontract.CommunityListInput
	CommunityGetInput              = communitycontract.CommunityGetInput
	CommunityDiscoveryInput        = communitycontract.CommunityDiscoveryInput
	CommunityDiscoveryPath         = communitycontract.CommunityDiscoveryPath
	CommunityDiscoveryRelationship = communitycontract.CommunityDiscoveryRelationship
	CommunityRecord                = communitycontract.CommunityRecord
)

const (
	CommunityAlgorithmKind    = communitycontract.CommunityAlgorithmKind
	CommunityAlgorithmVersion = communitycontract.CommunityAlgorithmVersion
	CommunityProfileVersion   = communitycontract.CommunityProfileVersion
)

var (
	ErrCommunityRunAlreadyClaimed = communitycontract.ErrCommunityRunAlreadyClaimed
	ErrCommunityNotFound          = communitycontract.ErrCommunityNotFound
	ErrCommunitySourceStale       = communitycontract.ErrCommunitySourceStale
)

type AppConfig interface {
	OntologyMaintenanceRuntimeConfig(context.Context) (domain.OntologyMaintenanceConfig, error)
}

type Dependencies struct {
	Store     CommunityRepository
	AppConfig AppConfig

	Metrics observability.DiscoverabilityMetrics
	Now     func() time.Time
}

type Service interface {
	RunProjectionTurn(context.Context) (bool, error)
	Status(context.Context, string) (*StatusResult, error)
}

type RunResult struct {
	RunID             string    `json:"run_id"`
	TeamID            string    `json:"team_id"`
	WindowKey         string    `json:"window_key"`
	Status            string    `json:"status"`
	NodeCount         int       `json:"node_count"`
	EdgeCount         int       `json:"edge_count"`
	CommunityCount    int       `json:"community_count"`
	SourceFingerprint string    `json:"source_fingerprint,omitempty"`
	ProviderModel     string    `json:"provider_model,omitempty"`
	ProviderAttempts  int       `json:"provider_attempts,omitempty"`
	Error             string    `json:"error,omitempty"`
	StartedAt         time.Time `json:"started_at,omitempty"`
	CompletedAt       time.Time `json:"completed_at,omitempty"`
}

type StatusResult struct {
	LegacyProcessingRetired bool                                       `json:"legacy_processing_retired"`
	EffectiveConfig         domain.OntologyMaintenanceConfig           `json:"effective_config"`
	LatestRun               *RunResult                                 `json:"latest_run,omitempty"`
	CurrentCommunityCount   int                                        `json:"current_community_count"`
	ProjectionCoverage      *communitycontract.TopicProjectionCoverage `json:"projection_coverage,omitempty"`
}

func uuidString(value string) uuid.UUID {
	parsed, _ := uuid.Parse(value)
	return parsed
}
