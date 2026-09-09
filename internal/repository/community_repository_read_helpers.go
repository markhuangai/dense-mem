package repository

import (
	"context"
)

func (r *SemanticRepositoryImpl) ListCommunities(ctx context.Context, input CommunityListInput) ([]CommunityRecord, error) {
	return r.communityOwner().ListCommunities(ctx, input)
}

func (r *SemanticRepositoryImpl) CountCurrentCommunities(ctx context.Context, teamID string) (int, error) {
	return r.communityOwner().CountCurrentCommunities(ctx, teamID)
}

func (r *SemanticRepositoryImpl) GetCommunity(ctx context.Context, input CommunityGetInput) (*CommunityRecord, error) {
	return r.communityOwner().GetCommunity(ctx, input)
}

func (r *SemanticRepositoryImpl) LatestCommunityRun(ctx context.Context, teamID string) (*CommunityRun, error) {
	return r.communityOwner().LatestCommunityRun(ctx, teamID)
}

func (r *SemanticRepositoryImpl) ListCurrentCommunityLineage(ctx context.Context, teamID string) ([]CommunityLineageRecord, error) {
	return r.communityOwner().ListCurrentCommunityLineage(ctx, teamID)
}

var _ interface {
	ListCommunities(context.Context, CommunityListInput) ([]CommunityRecord, error)
	CountCurrentCommunities(context.Context, string) (int, error)
	GetCommunity(context.Context, CommunityGetInput) (*CommunityRecord, error)
	LatestCommunityRun(context.Context, string) (*CommunityRun, error)
	ListCurrentCommunityLineage(context.Context, string) ([]CommunityLineageRecord, error)
} = (*SemanticRepositoryImpl)(nil)
