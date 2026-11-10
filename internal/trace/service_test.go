package trace

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"github.com/stretchr/testify/require"
	"testing"
)

type semanticTraceStoreStub struct {
	result *tracecontract.EvidenceTraceResult
	input  tracecontract.Input
	err    error
	calls  int
}

func (s *semanticTraceStoreStub) TraceEvidence(_ context.Context, input tracecontract.Input) (*tracecontract.EvidenceTraceResult, error) {
	s.input = input
	s.calls++
	return s.result, s.err
}

func TestEvidenceOnlyUnitTraceRequiresAuthenticatedEvidenceIdentity(t *testing.T) {
	store := &semanticTraceStoreStub{}
	service := NewSemantic(store)
	_, err := service.Trace(context.Background(), "ignored", TraceRequest{EvidenceID: uuid.NewString()})
	require.ErrorIs(t, err, ErrTraceAuthContext)
	require.Zero(t, store.calls)
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: uuid.New(), OwnerID: uuid.New()})
	for _, id := range []string{"", uuid.Nil.String(), "invalid"} {
		_, err := service.Trace(ctx, "ignored", TraceRequest{EvidenceID: id})
		require.ErrorIs(t, err, tracecontract.ErrEvidenceIDInvalid)
	}
	require.Zero(t, store.calls)
}

func TestEvidenceOnlyUnitTracePreservesFailureAndTeamBoundaries(t *testing.T) {
	team := uuid.New()
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: team, OwnerID: uuid.New()})
	evidenceID := uuid.NewString()
	for _, cause := range []error{tracecontract.ErrEvidenceNotFound, errors.New("database unavailable")} {
		store := &semanticTraceStoreStub{err: cause}
		_, err := NewSemantic(store).Trace(ctx, "caller-team", TraceRequest{EvidenceID: evidenceID})
		require.ErrorIs(t, err, cause)
		require.Equal(t, team.String(), store.input.TeamID)
	}
	store := &semanticTraceStoreStub{}
	_, err := NewSemantic(store).Trace(ctx, "ignored", TraceRequest{EvidenceID: evidenceID})
	require.ErrorIs(t, err, ErrTraceEvidenceNotFound)
	store.result = &tracecontract.EvidenceTraceResult{Evidence: &tracecontract.TraceEvidenceFragment{FragmentID: evidenceID, TeamID: uuid.NewString()}}
	_, err = NewSemantic(store).Trace(ctx, "ignored", TraceRequest{EvidenceID: evidenceID})
	require.ErrorIs(t, err, ErrTraceRepositoryTeamMismatch)
}

func TestEvidenceOnlyUnitTracePreservesLimitsAndOmissions(t *testing.T) {
	team := uuid.New()
	evidenceID := uuid.NewString()
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: team, OwnerID: uuid.New()})
	store := &semanticTraceStoreStub{result: &tracecontract.EvidenceTraceResult{Evidence: &tracecontract.TraceEvidenceFragment{FragmentID: evidenceID, TeamID: team.String()}, Omissions: []string{"occurrences_max_events"}, Truncated: true}}
	result, err := NewSemantic(store).Trace(ctx, "ignored", TraceRequest{EvidenceID: evidenceID, IncludeEvidenceContent: new(false), IncludeTransitions: new(false), MaxEvents: 999, MaxFragmentContentRunes: 99999})
	require.NoError(t, err)
	require.Same(t, store.result, result.Semantic)
	require.Equal(t, 500, store.input.MaxEvents)
	require.Equal(t, 8000, store.input.MaxFragmentContentRunes)
	require.False(t, *store.input.IncludeEvidenceContent)
	require.False(t, *store.input.IncludeTransitions)
	require.Equal(t, []string{"occurrences_max_events"}, result.Semantic.Omissions)
	require.True(t, result.Semantic.Truncated)
}
