package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSemanticGraphFacadeValidatesBeforeConfiguration(t *testing.T) {
	ctx := context.Background()
	repo := &SemanticRepositoryImpl{}

	_, err := repo.SemanticGraph(ctx, SemanticGraphQuery{TeamID: "not-a-uuid"})
	require.ErrorContains(t, err, "team_id is required")

	_, err = repo.SemanticGraphNodeDetail(ctx, SemanticGraphNodeDetailInput{TeamID: "not-a-uuid"})
	require.ErrorContains(t, err, "team_id is required")
}

func TestSemanticGraphFacadePreservesRLSConfigurationError(t *testing.T) {
	ctx := context.Background()
	repo := &SemanticRepositoryImpl{db: &gorm.DB{}}
	teamID := uuid.NewString()

	_, err := repo.SemanticGraph(ctx, SemanticGraphQuery{TeamID: teamID})
	require.EqualError(t, err, "semantic graph: semantic: rls helper is required")

	_, err = repo.SemanticGraphNodeDetail(ctx, SemanticGraphNodeDetailInput{
		TeamID:   teamID,
		NodeType: "entity",
		NodeID:   uuid.NewString(),
	})
	require.EqualError(t, err, "semantic graph node: semantic: rls helper is required")
}
