package serverapp

import (
	"context"
	"testing"

	"github.com/google/uuid"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestBuildCommunityApplicationConstructsNativeStore(t *testing.T) {
	var service communityapp.Service = buildCommunityApplication(communityApplicationDependencies{})
	_, err := service.Status(context.Background(), uuid.NewString())
	require.ErrorContains(t, err, "community: repository is required")
}

func TestCommunitySummaryProviderTagsStructuredCallForTelemetry(t *testing.T) {
	called := false
	provider := communitySummaryProvider{
		model: "community-model",
		complete: func(ctx context.Context, _ string, _ string, _ map[string]any, _ string, _ any) (string, error) {
			called = observability.HasAIOperation(ctx)
			return `{"summary":"summary","top_entities":[],"top_predicates":[],"admitted_relationship_ids":["relationship-1"],"admitted_evidence_ids":[],"admitted_support_quotes":[]}`, nil
		},
	}
	_, err := provider.SummarizeCommunity(context.Background(), domain.CommunitySummaryInput{
		CommunityID: "community-1",
		Relationships: []domain.CommunitySummaryRelationship{{
			RelationshipID: "relationship-1",
		}},
	})
	require.NoError(t, err)
	require.True(t, called)
}
