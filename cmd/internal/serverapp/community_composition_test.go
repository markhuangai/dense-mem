package serverapp

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type communityCompositionSourceStub struct{}

func (communityCompositionSourceStub) CommunityDatabase() *gorm.DB             { return &gorm.DB{} }
func (communityCompositionSourceStub) CommunityRLS() storagepostgres.RLSHelper { return nil }

func TestBuildCommunityApplicationConstructsNativeStore(t *testing.T) {
	var service communityapp.Service = buildCommunityApplication(communityApplicationDependencies{Store: communityCompositionSourceStub{}})
	_, err := service.Status(context.Background(), uuid.NewString())
	require.ErrorContains(t, err, "community: rls helper is required")
}
