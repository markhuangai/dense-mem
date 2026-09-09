package repository

import "context"

func (r *SemanticRepositoryImpl) RecallCommunityDiscovery(ctx context.Context, input CommunityDiscoveryInput) ([]CommunityDiscoveryPath, error) {
	return r.communityOwner().RecallCommunityDiscovery(ctx, input)
}

var _ interface {
	RecallCommunityDiscovery(context.Context, CommunityDiscoveryInput) ([]CommunityDiscoveryPath, error)
} = (*SemanticRepositoryImpl)(nil)
