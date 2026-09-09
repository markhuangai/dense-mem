package repository

import "context"

func (r *SemanticRepositoryImpl) RecallCommunities(ctx context.Context, input CommunityRecallInput) ([]CommunityRecallRecord, error) {
	return r.communityOwner().RecallCommunities(ctx, input)
}

var _ interface {
	RecallCommunities(context.Context, CommunityRecallInput) ([]CommunityRecallRecord, error)
} = (*SemanticRepositoryImpl)(nil)
