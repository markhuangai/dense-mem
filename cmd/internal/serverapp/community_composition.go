package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
)

type communityApplicationDependencies struct {
	Store     repository.CommunityRepository
	AppConfig communityservice.AppConfig
	Summary   communityservice.SummaryProvider
	Metrics   observability.DiscoverabilityMetrics
}

func buildCommunityApplication(deps communityApplicationDependencies) communityservice.Service {
	return communityservice.New(communityservice.Dependencies{
		Store:     deps.Store,
		AppConfig: deps.AppConfig,
		Summary:   deps.Summary,
		Metrics:   deps.Metrics,
	})
}
