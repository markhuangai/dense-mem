package topics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

type equivalenceUnitRepo struct {
	neighborhoods []contract.EquivalenceNeighborhood
}

func (r *equivalenceUnitRepo) ReadEquivalenceNeighborhoods(context.Context, contract.MaintenanceClaim) ([]contract.EquivalenceNeighborhood, error) {
	return r.neighborhoods, nil
}
func (r *equivalenceUnitRepo) ReadEquivalence(_ context.Context, teamID string, sources []contract.SourceHandle) (contract.OrganizationContext, error) {
	snaps := make([]contract.SourceSnapshot, 0, len(sources))
	for _, src := range sources {
		snaps = append(snaps, contract.SourceSnapshot{
			SourceHandle: src, TeamID: teamID, SpaceID: uuid.NewString(), Generation: 1,
			OwnerID: uuid.NewString(), Eligible: true, State: map[string]string{"content": "Fact " + src.ID},
		})
	}
	return contract.OrganizationContext{Revision: 1, Sources: snaps}, nil
}
func (r *equivalenceUnitRepo) FindOrganization(context.Context, string, contract.OrganizationInput, string) (contract.OrganizationResult, bool, error) {
	return contract.OrganizationResult{}, false, nil
}
func (r *equivalenceUnitRepo) ReadOrganization(context.Context, string, []contract.SourceHandle) (contract.OrganizationContext, error) {
	return contract.OrganizationContext{}, nil
}
func (r *equivalenceUnitRepo) ReadDefinitionHeads(context.Context, string, contract.Record) ([]contract.RecordView, error) {
	return nil, nil
}
func (r *equivalenceUnitRepo) CommitOrganization(_ context.Context, _ string, receipt contract.OrganizationReceipt, _ contract.Publication) (contract.OrganizationResult, error) {
	result := receipt.Result
	result.Current = true
	return result, nil
}

type equivalenceMaintenanceRepo struct {
	contract.MaintenanceRepository
	completedResult contract.OrganizationResult
	completedCode   string
	recordedCode    string
}

func (r *equivalenceMaintenanceRepo) AdmitMaintenanceAttempt(context.Context, contract.MaintenanceClaim, string, int, int, int, time.Time) error {
	return nil
}
func (r *equivalenceMaintenanceRepo) ReconcileMaintenanceAttempt(context.Context, contract.MaintenanceClaim, string, contract.AssessmentAttempt) error {
	return nil
}
func (r *equivalenceMaintenanceRepo) CompleteMaintenanceBatch(_ context.Context, _ contract.MaintenanceClaim, result contract.OrganizationResult, failure string, _ time.Time) error {
	r.completedResult = result
	r.completedCode = failure
	return nil
}
func (r *equivalenceMaintenanceRepo) RecordMaintenanceFailure(_ context.Context, _ string, code string, _ time.Time) error {
	r.recordedCode = code
	return nil
}

type concurrentEquivalenceTransport struct {
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	fail        error
}

func (t *concurrentEquivalenceTransport) Complete(ctx context.Context, _ modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	cur := t.inFlight.Add(1)
	for {
		prev := t.maxInFlight.Load()
		if cur <= prev || t.maxInFlight.CompareAndSwap(prev, cur) {
			break
		}
	}
	defer t.inFlight.Add(-1)
	time.Sleep(40 * time.Millisecond)
	if recorder := modelprovider.ExchangeRecorderFromContext(ctx); recorder != nil {
		recorder.RecordProviderExchange(ctx, modelprovider.ProviderExchange{
			Component: "equivalence", Model: "fixture-model", StatusCode: 503, Outcome: "provider_error",
			RequestBody: []byte(`{"schema":"evidence_equivalence_v1"}`), ResponseBody: []byte(`{"error":"upstream timeout"}`),
		})
	}
	return modelprovider.StructuredResult{}, t.fail
}

type captureLogSink struct {
	mu      sync.Mutex
	records []observability.LogRecord
}

func (s *captureLogSink) WriteLog(_ context.Context, record observability.LogRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func TestExecuteEquivalenceParallelizesLogsCleanlyAndCapturesFailureDiagnostics(t *testing.T) {
	const secret = "equiv-op-secret"
	const neighborhoodCount = 8
	teamID, runID, batchID, spaceID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	sources := make([]contract.SourceHandle, neighborhoodCount)
	neighborhoods := make([]contract.EquivalenceNeighborhood, neighborhoodCount)
	for i := range sources {
		seed := contract.SourceHandle{Kind: contract.EvidenceSource, ID: uuid.NewString(), Version: 1}
		peer := contract.SourceHandle{Kind: contract.EvidenceSource, ID: uuid.NewString(), Version: 1}
		sources[i] = seed
		neighborhoods[i] = contract.EquivalenceNeighborhood{Seed: seed, Sources: []contract.SourceHandle{seed, peer}}
	}

	var console bytes.Buffer
	sink := &captureLogSink{}
	protector := observability.NewCredentialProtector(secret)
	root := observability.NewConsoleWithProtector(slog.LevelInfo, protector, &console)
	require.NoError(t, root.AttachSink(sink))

	orgRepo := &equivalenceUnitRepo{neighborhoods: neighborhoods}
	maintRepo := &equivalenceMaintenanceRepo{}
	transport := &concurrentEquivalenceTransport{
		fail: &modelprovider.ProviderError{Provider: "openai", StatusCode: 503, FailureClass: modelprovider.ProviderFailureClassTimeout, Message: "timeout " + secret},
	}

	svc := NewMaintenanceService(MaintenanceDependencies{
		Repository:      maintRepo,
		DefaultModel:    "fixture-model",
		ProviderTimeout: 5 * time.Second,
		Logger:          root.ContextSlog(),
		Diagnostics:     MaintenanceDiagnostics(sink, protector, root.ContextSlog()),
		Organizer: func(model string, contextWindowTokens int64, accounting assessment.AttemptAccounting) *Service {
			provider := assessment.NewProviderWithAccounting(transport, model, assessor.DefaultSemanticAssessmentLimits(), accounting, int(contextWindowTokens))
			return NewService(orgRepo, provider)
		},
	})

	claim := contract.MaintenanceClaim{
		ID: batchID, Kind: "equivalence", RunID: runID, WindowID: uuid.NewString(),
		TeamID: teamID, SpaceID: spaceID, Generation: 1, LeaseUntil: time.Now().UTC().Add(time.Minute),
		Sources: sources,
	}
	err := svc.executeBatch(context.Background(), maintenanceWork{
		claim:  claim,
		policy: domain.TopicMaintenanceConfig{Model: "fixture-model", ContextWindowTokens: 300000},
	})
	require.Error(t, err)
	require.Equal(t, int32(4), transport.maxInFlight.Load(), "equivalence fallback must use four concurrent assessment slots")
	require.NotContains(t, console.String(), secret)
	require.NotContains(t, console.String(), "diagnostic_unavailable_reason")

	var sawBatchCompleted, sawFailureCapture bool
	for _, record := range sink.records {
		require.NotContains(t, record.Attrs, "diagnostic_unavailable_reason", "record %s must not hit CredentialProtectionUnsupported", record.Message)
		encoded, marshalErr := json.Marshal(record.Attrs)
		require.NoError(t, marshalErr)
		require.NotContains(t, string(encoded), secret)
		switch record.Message {
		case "topic_equivalence.batch_completed":
			sawBatchCompleted = true
			outcomes, ok := record.Attrs["outcomes"].([]any)
			require.True(t, ok, "outcomes must be structured JSON array, got %T", record.Attrs["outcomes"])
			require.Len(t, outcomes, neighborhoodCount)
		case "topic_failure_capture":
			sawFailureCapture = true
			require.Equal(t, "equivalence", record.Attrs["stage"])
			require.Equal(t, runID, record.Attrs["reference_id"])
			require.GreaterOrEqual(t, record.Attrs["exchange_count"].(float64), float64(neighborhoodCount))
			require.NotEmpty(t, record.Attrs["error"])
		}
	}
	require.True(t, sawBatchCompleted)
	require.True(t, sawFailureCapture)
}

type failingCommunityRepo struct {
	contract.CommunityRepository
}

func (failingCommunityRepo) CompleteCommunity(context.Context, contract.MaintenanceClaim, contract.CommunityInput, contract.CommunityOutcome, string, time.Time) error {
	return nil
}

type failingConsolidationRepo struct {
	contract.TopicConsolidationRepository
}

func (failingConsolidationRepo) FinishTopicConsolidation(context.Context, contract.MaintenanceClaim, bool) error {
	return nil
}

type errorStructuredTransport struct{ err error }

func (t errorStructuredTransport) Complete(ctx context.Context, _ modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	if recorder := modelprovider.ExchangeRecorderFromContext(ctx); recorder != nil {
		recorder.RecordProviderExchange(ctx, modelprovider.ProviderExchange{
			Component: "structured", Model: "fixture-model", StatusCode: 500, Outcome: "provider_error",
			RequestBody: []byte(`{"test":true}`), ResponseBody: []byte(`{"error":"boom"}`),
		})
	}
	return modelprovider.StructuredResult{}, t.err
}

func TestCommunityAndConsolidationRecordFailureDiagnostics(t *testing.T) {
	teamID, runID := uuid.NewString(), uuid.NewString()
	protector := observability.NewCredentialProtector()
	sink := &captureLogSink{}
	maintRepo := &equivalenceMaintenanceRepo{}
	providerErr := errors.New("provider failure")

	svc := NewMaintenanceService(MaintenanceDependencies{
		Repository:      maintRepo,
		Communities:     failingCommunityRepo{},
		Consolidation:   failingConsolidationRepo{},
		DefaultModel:    "fixture-model",
		ProviderTimeout: 5 * time.Second,
		Placement:       CommunityAssessor{Transport: errorStructuredTransport{err: providerErr}, Limits: assessor.DefaultSemanticAssessmentLimits()},
		Diagnostics:     MaintenanceDiagnostics(sink, protector, nil),
	})

	communityClaim := contract.MaintenanceClaim{ID: uuid.NewString(), Kind: "community", RunID: runID, TeamID: teamID, LeaseUntil: time.Now().UTC().Add(time.Minute)}
	communityErr := svc.executeBatch(context.Background(), maintenanceWork{
		claim:     communityClaim,
		policy:    domain.TopicMaintenanceConfig{Model: "fixture-model", ContextWindowTokens: 300000},
		community: &contract.CommunityInput{Topic: contract.CommunityCandidate{ID: uuid.NewString(), Version: 1, Name: "A"}, Candidates: []contract.CommunityCandidate{{ID: uuid.NewString(), Version: 1, Name: "B"}}},
	})
	require.Error(t, communityErr)

	consolidationClaim := contract.MaintenanceClaim{ID: uuid.NewString(), Kind: "consolidation", RunID: runID, TeamID: teamID, LeaseUntil: time.Now().UTC().Add(time.Minute)}
	consolidationErr := svc.executeBatch(context.Background(), maintenanceWork{
		claim:  consolidationClaim,
		policy: domain.TopicMaintenanceConfig{Model: "fixture-model", ContextWindowTokens: 300000},
		pairs:  []contract.TopicConsolidationPair{{Left: contract.CommunityCandidate{ID: uuid.NewString(), Version: 1, Name: "A"}, Right: contract.CommunityCandidate{ID: uuid.NewString(), Version: 1, Name: "B"}}},
	})
	require.Error(t, consolidationErr)

	stages := map[string]bool{}
	for _, record := range sink.records {
		if record.Message == "topic_failure_capture" {
			stage, _ := record.Attrs["stage"].(string)
			stages[stage] = true
			require.Equal(t, runID, record.Attrs["reference_id"])
			require.GreaterOrEqual(t, record.Attrs["exchange_count"].(float64), float64(1))
			require.NotEmpty(t, record.Attrs["error"])
		}
	}
	require.True(t, stages["community_placement"], "community placement failure must write topic_failure_capture")
	require.True(t, stages["topic_consolidation"], "topic consolidation failure must write topic_failure_capture")
}
