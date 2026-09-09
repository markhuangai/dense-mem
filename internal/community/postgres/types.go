package postgres

import (
	"context"
	"errors"
	"fmt"

	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
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

type Store struct {
	db  *gorm.DB
	rls storagepostgres.RLSHelper
}

func NewStore(db *gorm.DB, rls storagepostgres.RLSHelper) *Store {
	return &Store{db: db, rls: rls}
}

type semanticSpaceFence struct {
	ID         string
	Generation int64
}

func (r *Store) withTeamTx(ctx context.Context, teamID string, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("community: database is required")
	}
	if r.rls == nil {
		return errors.New("community: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}

func loadTeamSharedSpaceFence(ctx context.Context, tx *gorm.DB, teamID string) (semanticSpaceFence, error) {
	fence := semanticSpaceFence{}
	err := tx.WithContext(ctx).Raw(`
		SELECT id::text, generation
		FROM memory_spaces
		WHERE team_id = ?::uuid
		  AND kind = 'team_shared'
		  AND lifecycle_state = 'active'
	`, teamID).Row().Scan(&fence.ID, &fence.Generation)
	if err != nil {
		return semanticSpaceFence{}, fmt.Errorf("load team-shared memory space: %w", err)
	}
	return fence, nil
}

var _ CommunityRepository = (*Store)(nil)
