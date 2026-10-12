package serverapp

import (
	"context"
	"testing"

	"github.com/google/uuid"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"

	"github.com/stretchr/testify/require"
)

func TestBuildCommunityApplicationConstructsNativeStore(t *testing.T) {
	var service communityapp.Service = buildCommunityApplication(communityApplicationDependencies{})
	_, err := service.Status(context.Background(), uuid.NewString())
	require.ErrorContains(t, err, "community: repository and ontology configuration are required")
}
