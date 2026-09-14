package serverapp

import (
	"context"

	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"
	communityprovider "github.com/markhuangai/dense-mem/internal/community/provider"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type communitySummaryProvider struct {
	model    string
	complete communityprovider.CompleteFunc
}

func (p communitySummaryProvider) ModelName() string { return p.model }

func (p communitySummaryProvider) SummarizeCommunity(ctx context.Context, input domain.CommunitySummaryInput) (domain.CommunitySummary, error) {
	ctx = observability.WithAIOperation(ctx, observability.AIOperationCommunitySummary, len(input.Relationships))
	return communityprovider.SummarizeCommunity(ctx, p.model, input, p.complete)
}

type communityApplicationDependencies struct {
	Store     communitycontract.CommunityRepository
	AppConfig communityapp.AppConfig
	Summary   communityapp.SummaryProvider
	Metrics   observability.DiscoverabilityMetrics
}

func buildCommunityApplication(deps communityApplicationDependencies) communityapp.Service {
	return communityapp.New(communityapp.Dependencies{
		Store:     deps.Store,
		AppConfig: deps.AppConfig,
		Summary:   deps.Summary,
		Metrics:   deps.Metrics,
	})
}
