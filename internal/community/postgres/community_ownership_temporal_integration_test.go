//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	recallservice "github.com/markhuangai/dense-mem/internal/recall"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	"github.com/stretchr/testify/require"
)

type communityOwnershipConfig struct{}

func (communityOwnershipConfig) CommunityDetectionRuntimeConfig(context.Context) (domain.CommunityDetectionRuntimeConfig, error) {
	return domain.CommunityDetectionRuntimeConfig{Enabled: true}, nil
}

func TestCommunityOwnershipTemporalRecallPreservesDirectRelationship(t *testing.T) {
	f := newCommunityOwnershipFixture(t)
	ctx := context.Background()
	insertSearchTestContract(t, f.adminDB, f.rls, "community-temporal", 3, "exact", "")
	projection := knowledgepostgres.NewStore(f.appDB, f.rls, knowledgepostgres.ConflictRuntimeConfig{})
	_, err := projection.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: f.teamID, OwnerProfileID: f.ownerA, SourceKind: "relationship", SourceID: f.sources[0].RelationshipID,
		SourceVersion: int64(f.sources[0].RelationshipVersion), DocumentText: "community temporal needle",
	})
	require.NoError(t, err)
	shared, err := privacypostgres.NewMemorySpaceRepository(f.appDB, f.rls).GetTeamShared(ctx, uuid.MustParse(f.teamID))
	require.NoError(t, err)
	actorCtx := requestctx.WithActor(ctx, requestctx.Actor{TeamID: uuid.MustParse(f.teamID), OwnerID: uuid.MustParse(f.ownerA), AuthMethod: "api_key",
		AllowedSpaces: []domain.MemorySpaceAccess{{ID: shared.ID, Kind: domain.MemorySpaceTeamShared}}})
	service := recallservice.NewRecallService(recallservice.RecallDependencies{
		Search: recallpostgres.NewStore(f.appDB, f.rls, searchpostgres.NewStore(f.appDB, f.rls), nil, nil), Communities: f.store, CommunityConfig: communityOwnershipConfig{},
	})
	limit := 5
	future := time.Now().UTC().Add(time.Hour)
	for _, kind := range []string{"valid_at", "known_at"} {
		request := recallservice.RecallRequest{Query: "community temporal needle", RelationshipLimit: &limit, CommunityLimit: &limit}
		if kind == "valid_at" {
			request.ValidAt = &future
		} else {
			request.KnownAt = &future
		}
		result, err := service.Recall(actorCtx, request)
		require.NoError(t, err)
		require.Empty(t, result.RelatedCommunities)
		require.Len(t, result.RelatedRelationships, 1)
		require.Equal(t, f.sources[0].RelationshipID, result.RelatedRelationships[0].RelationshipID)
		found := false
		for _, degradation := range result.Degradations {
			if degradation.Code == "community_temporal_not_supported" {
				found = true
				require.True(t, degradation.Optional)
			}
		}
		require.True(t, found, "temporal Community degradation must remain visible")
		t.Logf("case=temporal_%s direct=relationship-00 communities=[] degradation=community_temporal_not_supported", kind)
	}
}
