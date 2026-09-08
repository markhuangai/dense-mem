package serverapp

import (
	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"
	communitypostgres "github.com/markhuangai/dense-mem/internal/community/postgres"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/observability"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

type communityApplicationDependencies struct {
	Store interface {
		CommunityDatabase() *gorm.DB
		CommunityRLS() storagepostgres.RLSHelper
	}
	AppConfig communityapp.AppConfig
	Summary   communityapp.SummaryProvider
	Metrics   observability.DiscoverabilityMetrics
}

func buildCommunityApplication(deps communityApplicationDependencies) communityapp.Service {
	var store communitycontract.CommunityRepository
	if deps.Store != nil {
		store = communitypostgres.NewStore(deps.Store.CommunityDatabase(), deps.Store.CommunityRLS())
	}
	return communityapp.New(communityapp.Dependencies{
		Store:     store,
		AppConfig: deps.AppConfig,
		Summary:   deps.Summary,
		Metrics:   deps.Metrics,
	})
}
