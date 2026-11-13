package trace

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"github.com/stretchr/testify/require"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestEvidenceOnlyUnitTraceDiagnosticsPreserveLimitsThroughProtection(t *testing.T) {
	team, owner, evidence := uuid.New(), uuid.New(), uuid.NewString()
	ctx := requestctx.WithActor(t.Context(), requestctx.Actor{TeamID: team, OwnerID: owner})
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	output, err := os.Create(path)
	require.NoError(t, err)
	defer output.Close()
	previous := os.Stdout
	os.Stdout = output
	logger := observability.NewWithProtector(slog.LevelDebug, observability.NewCredentialProtector("trace-credential-canary"))
	os.Stdout = previous
	store := &semanticTraceStoreStub{result: &tracecontract.EvidenceTraceResult{
		Evidence:  &tracecontract.TraceEvidenceFragment{FragmentID: evidence, TeamID: team.String()},
		Selection: map[string]any{"token": "exact admitted diagnostic text", "configured_credential": "trace-credential-canary"},
	}}
	_, err = NewSemantic(store, logger).Trace(ctx, "", TraceRequest{EvidenceID: evidence, MaxEvents: 7})
	require.NoError(t, err)
	require.NoError(t, output.Close())
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(body), "trace-credential-canary")
	var record map[string]any
	require.NoError(t, json.Unmarshal(body, &record))
	require.Equal(t, "DEBUG", record["level"])
	require.Equal(t, "completed", record["outcome"])
	limits, ok := record["limits"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(7), limits["MaxEvents"])
	require.Equal(t, evidence, limits["EvidenceID"])
	selection, ok := record["selection"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "exact admitted diagnostic text", selection["token"])
}

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
	require.Equal(t, 16384, store.input.MaxFragmentContentRunes)
	require.False(t, *store.input.IncludeEvidenceContent)
	require.False(t, *store.input.IncludeTransitions)
	require.Equal(t, []string{"occurrences_max_events"}, result.Semantic.Omissions)
	require.True(t, result.Semantic.Truncated)
}
