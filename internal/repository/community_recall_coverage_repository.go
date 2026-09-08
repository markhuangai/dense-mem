package repository

import "context"

func (r *SemanticRepositoryImpl) ListCommunitySemanticGroups(ctx context.Context, input CommunityCoverageInput) ([]string, error) {
	return r.communityOwner().ListCommunitySemanticGroups(ctx, input)
}

var _ interface {
	ListCommunitySemanticGroups(context.Context, CommunityCoverageInput) ([]string, error)
} = (*SemanticRepositoryImpl)(nil)
