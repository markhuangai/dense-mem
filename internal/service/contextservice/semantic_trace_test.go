package contextservice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type semanticTraceStoreStub struct {
	result *repository.RelationshipTraceResult
	input  repository.TraceRelationshipInput
	err    error
}

func (s *semanticTraceStoreStub) TraceRelationship(_ context.Context, input repository.TraceRelationshipInput) (*repository.RelationshipTraceResult, error) {
	s.input = input
	return s.result, s.err
}

func TestSemanticTraceMapsMissingRelationship(t *testing.T) {
	teamID := uuid.New()
	store := &semanticTraceStoreStub{err: repository.ErrTraceRelationshipNotFound}
	service := NewSemantic(store)
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID:  teamID,
		OwnerID: uuid.New(),
	})

	_, err := service.Trace(ctx, "", TraceRequest{RelationshipID: uuid.NewString()})
	require.ErrorIs(t, err, ErrTraceRelationshipNotFound)
	require.Equal(t, "not_found: relationship not found", err.Error())
}

func TestSemanticTracePreservesRepositoryFailures(t *testing.T) {
	teamID := uuid.New()
	databaseErr := errors.New("database unavailable")
	store := &semanticTraceStoreStub{err: databaseErr}
	service := NewSemantic(store)
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID:  teamID,
		OwnerID: uuid.New(),
	})

	_, err := service.Trace(ctx, "", TraceRequest{RelationshipID: uuid.NewString()})
	require.ErrorIs(t, err, databaseErr)
	require.NotErrorIs(t, err, ErrTraceRelationshipNotFound)
	require.Equal(t, "trace: database unavailable", err.Error())
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
