package serverapp

import (
	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"

	communityapp "github.com/markhuangai/dense-mem/internal/community/service"

	"github.com/markhuangai/dense-mem/internal/observability"
)

type communityApplicationDependencies struct {
	Store     communitycontract.CommunityRepository
	AppConfig communityapp.AppConfig

	Metrics observability.DiscoverabilityMetrics
}

func buildCommunityApplication(deps communityApplicationDependencies) communityapp.Service {
	return communityapp.New(communityapp.Dependencies{
		Store:     deps.Store,
		AppConfig: deps.AppConfig,
		Metrics:   deps.Metrics,
	})
}
