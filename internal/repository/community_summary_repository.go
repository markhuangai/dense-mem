package repository

import "context"

func (r *SemanticRepositoryImpl) RecordCommunitySummaryAttempt(ctx context.Context, input CommunitySummaryAttemptInput) error {
	return r.communityOwner().RecordCommunitySummaryAttempt(ctx, input)
}

var _ interface {
	RecordCommunitySummaryAttempt(context.Context, CommunitySummaryAttemptInput) error
} = (*SemanticRepositoryImpl)(nil)
