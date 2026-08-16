package contextservice

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type semanticTraceStoreStub struct {
	result *repository.RelationshipTraceResult
	input  repository.TraceRelationshipInput
}

func (s *semanticTraceStoreStub) TraceRelationship(_ context.Context, input repository.TraceRelationshipInput) (*repository.RelationshipTraceResult, error) {
	s.input = input
	return s.result, nil
}

func TestSemanticTraceRejectsMismatchedConflictTeam(t *testing.T) {
	teamID := uuid.New()
	store := &semanticTraceStoreStub{result: &repository.RelationshipTraceResult{
		Conflicts: []repository.RelationshipConflictCaseRecord{{
			TeamID:     uuid.NewString(),
			ConflictID: uuid.NewString(),
		}},
	}}
	service := NewSemantic(store)
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID:  teamID,
		OwnerID: uuid.New(),
	})

	_, err := service.Trace(ctx, "", TraceRequest{RelationshipID: uuid.NewString()})
	require.ErrorIs(t, err, ErrTraceRepositoryTeamMismatch)
	require.Equal(t, teamID.String(), store.input.TeamID)
}

func TestSemanticTraceAllowsMatchingConflictTeam(t *testing.T) {
	teamID := uuid.New()
	conflictID := uuid.NewString()
	store := &semanticTraceStoreStub{result: &repository.RelationshipTraceResult{
		Conflicts: []repository.RelationshipConflictCaseRecord{{
			TeamID:     teamID.String(),
			ConflictID: conflictID,
		}},
	}}
	service := NewSemantic(store)
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID:  teamID,
		OwnerID: uuid.New(),
	})

	result, err := service.Trace(ctx, "", TraceRequest{RelationshipID: uuid.NewString()})
	require.NoError(t, err)
	require.Len(t, result.Semantic.Conflicts, 1)
	require.Equal(t, conflictID, result.Semantic.Conflicts[0].ConflictID)
}
