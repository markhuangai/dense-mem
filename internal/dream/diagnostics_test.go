package dream

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

type diagnosticRepositoryStub struct {
	page            dreamcontract.DreamDiagnosticPage
	item            *dreamcontract.DreamDiagnosticCapture
	listIn          dreamcontract.DreamDiagnosticListInput
	recorded        []dreamcontract.DreamDiagnosticCaptureInput
	failRun         int
	failPhase       int
	failPhaseAlways bool
	listErr         error
	getErr          error
	purgeDeleted    int
	purgeErr        error
	purgeCalled     chan struct{}
	purgeSequence   []int
	phaseContextErr error
	phaseDeadline   time.Time
	phaseSuccesses  int
	runContextErr   error
	runDeadline     time.Time
	runSuccesses    int
	rejectCanceled  bool
}

func (s *diagnosticRepositoryStub) RecordDreamDiagnostic(ctx context.Context, input dreamcontract.DreamDiagnosticCaptureInput) error {
	s.phaseContextErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		s.phaseDeadline = deadline
	}
	s.recorded = append(s.recorded, input)
	if s.rejectCanceled && s.phaseContextErr != nil {
		return s.phaseContextErr
	}
	if s.failPhaseAlways {
		return context.DeadlineExceeded
	}
	if s.failPhase > 0 {
		s.failPhase--
		return context.DeadlineExceeded
	}
	s.phaseSuccesses++
	return nil
}

func (s *diagnosticRepositoryStub) RecordDreamRunDiagnostics(ctx context.Context, input dreamcontract.DreamDiagnosticCaptureInput) error {
	s.runContextErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		s.runDeadline = deadline
	}
	s.recorded = append(s.recorded, input)
	if s.rejectCanceled && s.runContextErr != nil {
		return s.runContextErr
	}
	if s.failRun > 0 {
		s.failRun--
		return context.DeadlineExceeded
	}
	s.runSuccesses++
	return nil
}

func (s *diagnosticRepositoryStub) ListDreamDiagnostics(_ context.Context, input dreamcontract.DreamDiagnosticListInput) (dreamcontract.DreamDiagnosticPage, error) {
	s.listIn = input
	if s.listErr != nil {
		return dreamcontract.DreamDiagnosticPage{}, s.listErr
	}
	return s.page, nil
}

func (s *diagnosticRepositoryStub) GetDreamDiagnostic(context.Context, string, string, string) (*dreamcontract.DreamDiagnosticCapture, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.item, nil
}

func (s *diagnosticRepositoryStub) PurgeExpiredDreamDiagnostics(context.Context, int) (int, error) {
	if s.purgeCalled != nil {
		select {
		case s.purgeCalled <- struct{}{}:
		default:
		}
	}
	if len(s.purgeSequence) > 0 {
		deleted := s.purgeSequence[0]
		s.purgeSequence = s.purgeSequence[1:]
		return deleted, s.purgeErr
	}
	return s.purgeDeleted, s.purgeErr
}

var _ dreamcontract.DreamDiagnosticRepository = (*diagnosticRepositoryStub)(nil)

func TestDiagnosticServiceListsTeamRunCaptures(t *testing.T) {
	teamID, runID := uuid.NewString(), uuid.NewString()
	created := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	repo := &diagnosticRepositoryStub{page: dreamcontract.DreamDiagnosticPage{
		Items: []dreamcontract.DreamDiagnosticCapture{{
			TeamID: teamID, RunID: runID, CaptureID: uuid.NewString(), Phase: "run",
			Outcome: "evaluated_zero", CaptureState: "not_captured", CreatedAt: created,
		}},
		NextCursor: "next",
	}}
	svc := NewDiagnosticService(repo)
	page, err := svc.List(context.Background(), teamID, runID, 25, "cursor")
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "evaluated_zero", page.Items[0].Outcome)
	require.Equal(t, "next", page.NextCursor)
	require.Equal(t, dreamcontract.DreamDiagnosticListInput{TeamID: teamID, RunID: runID, Limit: 25, Cursor: "cursor"}, repo.listIn)
}

func TestDiagnosticServiceListsHypothesisAndProjectsPayload(t *testing.T) {
	teamID, hypothesisID := uuid.NewString(), uuid.NewString()
	repo := &diagnosticRepositoryStub{page: dreamcontract.DreamDiagnosticPage{Items: []dreamcontract.DreamDiagnosticCapture{{
		TeamID: teamID, HypothesisID: hypothesisID, CaptureID: uuid.NewString(), Phase: "disposition",
		Outcome: "accepted", Details: map[string]any{"decision": "accept"}, Payload: []byte(`{"exchange":true}`),
	}}}}
	page, err := NewDiagnosticService(repo).ListForHypothesis(context.Background(), teamID, hypothesisID, 10, "next")
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, hypothesisID, page.Items[0].HypothesisID)
	require.Equal(t, true, page.Items[0].Payload["exchange"])
	require.Equal(t, map[string]any{"decision": "accept"}, page.Items[0].Details)
	require.Equal(t, dreamcontract.DreamDiagnosticListInput{TeamID: teamID, HypothesisID: hypothesisID, Limit: 10, Cursor: "next"}, repo.listIn)
}

func TestDiagnosticServiceValidatesInputsAndPropagatesErrors(t *testing.T) {
	validTeam, validRun, validCapture, validHypothesis := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	repoErr := errors.New("repository unavailable")
	repo := &diagnosticRepositoryStub{listErr: repoErr, getErr: repoErr}
	svc := NewDiagnosticService(repo)
	for name, call := range map[string]func() error{
		"list team": func() error { _, err := svc.List(context.Background(), "bad", validRun, 1, ""); return err },
		"list run":  func() error { _, err := svc.List(context.Background(), validTeam, "bad", 1, ""); return err },
		"hypothesis team": func() error {
			_, err := svc.ListForHypothesis(context.Background(), "bad", validHypothesis, 1, "")
			return err
		},
		"hypothesis id": func() error {
			_, err := svc.ListForHypothesis(context.Background(), validTeam, "bad", 1, "")
			return err
		},
		"get team":    func() error { _, err := svc.Get(context.Background(), "bad", validRun, validCapture); return err },
		"get run":     func() error { _, err := svc.Get(context.Background(), validTeam, "bad", validCapture); return err },
		"get capture": func() error { _, err := svc.Get(context.Background(), validTeam, validRun, "bad"); return err },
	} {
		t.Run(name, func(t *testing.T) { require.Error(t, call()) })
	}
	_, err := svc.List(context.Background(), validTeam, validRun, 1, "")
	require.ErrorIs(t, err, repoErr)
	_, err = svc.ListForHypothesis(context.Background(), validTeam, validHypothesis, 1, "")
	require.ErrorIs(t, err, repoErr)
	_, err = svc.Get(context.Background(), validTeam, validRun, validCapture)
	require.ErrorIs(t, err, repoErr)

	var nilSvc *diagnosticService
	_, err = nilSvc.List(context.Background(), validTeam, validRun, 1, "")
	require.Error(t, err)
	_, err = NewDiagnosticService(nil).ListForHypothesis(context.Background(), validTeam, validHypothesis, 1, "")
	require.Error(t, err)
	_, err = NewDiagnosticService(nil).Get(context.Background(), validTeam, validRun, validCapture)
	require.Error(t, err)
}

func TestDiagnosticServiceMapsNotFoundAndMalformedPayload(t *testing.T) {
	teamID, runID, captureID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	repo := &diagnosticRepositoryStub{item: &dreamcontract.DreamDiagnosticCapture{
		TeamID: teamID, RunID: runID, CaptureID: captureID, Payload: []byte("not-json"), Details: map[string]any{"phase": "provider"},
	}}
	item, err := NewDiagnosticService(repo).Get(context.Background(), teamID, runID, captureID)
	require.NoError(t, err)
	require.Nil(t, item.Payload)
	require.Equal(t, map[string]any{"phase": "provider"}, item.Details)
	repo.getErr = dreamcontract.ErrDreamDiagnosticNotFound
	_, err = NewDiagnosticService(repo).Get(context.Background(), teamID, runID, captureID)
	require.ErrorIs(t, err, dreamcontract.ErrDreamDiagnosticNotFound)
	repo.item = nil
	repo.getErr = nil
	item, err = NewDiagnosticService(repo).Get(context.Background(), teamID, runID, captureID)
	require.NoError(t, err)
	require.Nil(t, item)
	require.Nil(t, projectDreamDiagnostic(nil))
}

func TestDiagnosticServiceHidesExpiredDetails(t *testing.T) {
	teamID, runID := uuid.NewString(), uuid.NewString()
	repo := &diagnosticRepositoryStub{item: &dreamcontract.DreamDiagnosticCapture{
		TeamID: teamID, RunID: runID, CaptureID: uuid.NewString(), Phase: "run", Outcome: "completed",
		CaptureState: "expired", CaptureReason: "retention_expired", Details: map[string]any{"provider_proposals": 1},
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}}
	svc := NewDiagnosticService(repo)
	item, err := svc.Get(context.Background(), teamID, runID, repo.item.CaptureID)
	require.NoError(t, err)
	require.Equal(t, "expired", item.CaptureState)
	require.Equal(t, "retention_expired", item.CaptureReason)
	require.Nil(t, item.Details)
}

func TestRecordRunDiagnosticPersistsPhaseTraceAndCaptureFailureMarker(t *testing.T) {
	teamID, runID := uuid.NewString(), uuid.NewString()
	repo := &diagnosticRepositoryStub{failRun: 1}
	svc := &service{deps: Dependencies{
		Diagnostics: repo,
		Now:         func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) },
	}}
	result := &RunCycleResult{
		TeamID: teamID, RunID: runID, Status: "completed", Lane: "graph",
		CreatedDreams: 1, ProviderProposals: 1,
		diagnosticPhases:          []runDiagnosticPhase{{phase: "target", outcome: "selected", details: map[string]any{"path_refs": []string{"path_1"}}}},
		diagnosticPhasesTruncated: true,
	}
	svc.recordRunDiagnostic(context.Background(), result)
	require.Len(t, repo.recorded, 3, "the run summary is stored before phase persistence")
	require.Equal(t, "run", repo.recorded[0].Phase)
	require.Equal(t, "run", repo.recorded[1].Phase)
	require.Equal(t, "unavailable", repo.recorded[1].CaptureState)
	require.Equal(t, "diagnostic_capture_failed", repo.recorded[1].CaptureReason)
	require.Equal(t, true, repo.recorded[1].Details["phase_trace_truncated"])
	require.Equal(t, "target", repo.recorded[2].Phase)
	require.NotEmpty(t, repo.recorded[1].Details["phase_trace_expected"])
	require.Equal(t, 1, repo.runSuccesses, "run fallback stores exactly one run capture")
}

func TestRecordRunDiagnosticPreservesProviderCaptureReason(t *testing.T) {
	teamID, runID := uuid.NewString(), uuid.NewString()
	recorder := newDreamDiagnosticExchangeRecorder(observability.NewCredentialProtector())
	recorder.bytes = dreamDiagnosticRunPayloadLimit
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{ResponseBody: []byte(`{"ok":true}`)})
	payload := recorder.Payload()
	state, reason := recorder.State()
	repo := &diagnosticRepositoryStub{}
	svc := &service{deps: Dependencies{Diagnostics: repo}}
	result := &RunCycleResult{TeamID: teamID, RunID: runID, Status: "completed", providerPayload: payload, providerCaptureState: state, providerCaptureReason: reason}
	svc.recordRunDiagnostic(context.Background(), result)
	require.Equal(t, "truncated", repo.recorded[0].CaptureState)
	require.Equal(t, "run_payload_budget_exceeded", repo.recorded[0].CaptureReason)
}

func TestRecordRunDiagnosticWritesEmptyTraceWithoutTruncation(t *testing.T) {
	repo := &diagnosticRepositoryStub{}
	svc := &service{deps: Dependencies{Diagnostics: repo}}
	result := &RunCycleResult{
		TeamID: "11111111-1111-4111-8111-111111111111", RunID: "22222222-2222-4222-8222-222222222222",
		Status: "completed",
	}

	svc.recordRunDiagnostic(context.Background(), result)

	require.Len(t, repo.recorded, 1)
	require.Equal(t, "run", repo.recorded[0].Phase)
	require.Empty(t, repo.recorded[0].Details["phase_trace_expected"])
	require.NotContains(t, repo.recorded[0].Details, "phase_trace_truncated")
	require.Equal(t, 1, repo.runSuccesses)
}

func TestRecordRunDiagnosticRespectsEarlierCallerDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	repo := &diagnosticRepositoryStub{}
	svc := &service{deps: Dependencies{Diagnostics: repo}}
	result := &RunCycleResult{
		TeamID: "11111111-1111-4111-8111-111111111111", RunID: "22222222-2222-4222-8222-222222222222",
		Status: "completed", diagnosticPhases: []runDiagnosticPhase{{phase: "target", outcome: "selected"}},
	}

	svc.recordRunDiagnostic(ctx, result)

	require.Len(t, repo.recorded, 2)
	require.Equal(t, "run", repo.recorded[0].Phase)
	require.Equal(t, "target", repo.recorded[1].Phase)
	require.Equal(t, []runDiagnosticPhaseExpectation{{Phase: "target", Count: 1}}, repo.recorded[0].Details["phase_trace_expected"])
	require.Equal(t, deadline.UTC().Format(time.RFC3339Nano), repo.recorded[0].Details["phase_trace_deadline_at"])
	require.True(t, repo.runDeadline.Equal(deadline), "run capture retains the caller deadline")
	require.True(t, repo.phaseDeadline.Equal(deadline), "phase capture inherits the earlier caller deadline")
	require.Equal(t, 1, repo.phaseSuccesses)
}

func TestRecordRunDiagnosticHonorsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := &diagnosticRepositoryStub{rejectCanceled: true}
	logger := &diagnosticTestLogger{}
	svc := &service{deps: Dependencies{Diagnostics: repo, Logger: logger}}
	result := &RunCycleResult{
		TeamID: "11111111-1111-4111-8111-111111111111", RunID: "22222222-2222-4222-8222-222222222222",
		Status: "completed", diagnosticPhases: []runDiagnosticPhase{{phase: "target", outcome: "selected"}},
	}

	svc.recordRunDiagnostic(ctx, result)

	require.Len(t, repo.recorded, 2, "both summary attempts stay within the canceled caller context")
	for _, capture := range repo.recorded {
		require.Equal(t, "run", capture.Phase)
	}
	require.ErrorIs(t, repo.runContextErr, context.Canceled)
	require.Zero(t, repo.runSuccesses)
	require.Equal(t, 1, logger.errors)
}

func TestDiagnosticRelationshipResultsOmitCallerReferences(t *testing.T) {
	projected, truncated := diagnosticRelationshipResults([]rememberapp.SubmissionRelationshipResult{{
		RelationshipRef: "caller-secret-token",
		Disposition:     "stored",
		Splits: []rememberapp.SubmissionRelationshipSplit{{
			SplitIndex: 0, RelationshipID: uuid.NewString(), RelationshipVersion: 2, Status: "active",
		}},
	}})
	require.Len(t, projected, 1)
	require.False(t, truncated)
	require.NotContains(t, projected[0], "ref")
	require.Equal(t, "stored", projected[0]["disposition"])
}

func TestDiagnosticRelationshipResultsStayBounded(t *testing.T) {
	results := make([]rememberapp.SubmissionRelationshipResult, 200)
	for index := range results {
		results[index].Disposition = "stored"
		results[index].Splits = make([]rememberapp.SubmissionRelationshipSplit, 32)
	}

	projected, truncated := diagnosticRelationshipResults(results)

	require.Len(t, projected, 24)
	require.Len(t, projected[0]["splits"], 8)
	require.True(t, truncated)
}

func TestRecordRunDiagnosticSkipsPhasesWhenSummaryIsUnavailable(t *testing.T) {
	repo := &diagnosticRepositoryStub{failRun: 2}
	logger := &diagnosticTestLogger{}
	svc := &service{deps: Dependencies{Diagnostics: repo, Logger: logger}}
	phases := make([]runDiagnosticPhase, 256)
	for index := range phases {
		phases[index] = runDiagnosticPhase{phase: "target", outcome: "selected"}
	}
	result := &RunCycleResult{
		TeamID: "11111111-1111-4111-8111-111111111111", RunID: "22222222-2222-4222-8222-222222222222",
		Status: "completed", diagnosticPhases: phases, diagnosticPhasesTruncated: true,
	}
	svc.recordRunDiagnostic(context.Background(), result)
	require.Len(t, repo.recorded, 2)
	require.Equal(t, "run", repo.recorded[0].Phase)
	require.Equal(t, "run", repo.recorded[1].Phase)
	require.Equal(t, true, repo.recorded[1].Details["phase_trace_truncated"])
	require.Equal(t, 1, len(repo.recorded[1].Details["phase_trace_expected"].([]runDiagnosticPhaseExpectation)))
	require.Zero(t, repo.phaseSuccesses)
	require.Zero(t, repo.runSuccesses)
	require.Equal(t, 1, logger.errors)
}

type diagnosticTestLogger struct{ errors int }

func (l *diagnosticTestLogger) Info(string, ...observability.LogAttr)                   {}
func (l *diagnosticTestLogger) Error(string, error, ...observability.LogAttr)           { l.errors++ }
func (l *diagnosticTestLogger) Warn(string, ...observability.LogAttr)                   {}
func (l *diagnosticTestLogger) Debug(string, ...observability.LogAttr)                  {}
func (l *diagnosticTestLogger) With(...observability.LogAttr) observability.LogProvider { return l }

func TestDiagnosticPhaseHelpersAndRecorderBranches(t *testing.T) {
	result := &RunCycleResult{}
	appendRunDiagnosticPhase(nil, "phase", "ok", "", nil)
	appendRunDiagnosticPhase(result, "", "ok", "", nil)
	appendRunDiagnosticPhase(result, "phase", "ok", "", nil)
	appendRunDiagnosticHypothesisPhase(nil, "phase", "ok", "", uuid.NewString(), nil)
	appendRunDiagnosticHypothesisPhase(result, "phase", "ok", "", "", nil)
	appendRunDiagnosticHypothesisPhase(result, "proposal", "accepted", "", uuid.NewString(), map[string]any{"x": 1})
	for len(result.diagnosticPhases) < 256 {
		appendRunDiagnosticPhase(result, "full", "ok", "", nil)
	}
	appendRunDiagnosticPhase(result, "ignored", "ok", "", nil)
	appendRunDiagnosticHypothesisPhase(result, "ignored", "ok", "", uuid.NewString(), nil)
	require.Len(t, result.diagnosticPhases, 256)
	require.True(t, result.diagnosticPhasesTruncated)

	teamID, runID := uuid.NewString(), uuid.NewString()
	repo := &diagnosticRepositoryStub{}
	svc := &service{deps: Dependencies{Diagnostics: repo, Logger: &diagnosticTestLogger{}}, now: func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) }}
	result = &RunCycleResult{TeamID: teamID, RunID: runID, Status: "completed", diagnosticPhases: result.diagnosticPhases, diagnosticPhasesTruncated: true}
	svc.recordRunDiagnostic(context.Background(), result)
	require.Len(t, repo.recorded, 257)
	require.Equal(t, "run", repo.recorded[0].Phase)
	require.Equal(t, true, repo.recorded[0].Details["phase_trace_truncated"])
	require.Equal(t, "phase", repo.recorded[1].Phase)
	require.Equal(t, "full", repo.recorded[256].Phase)
	repo.recorded = nil

	result = &RunCycleResult{TeamID: teamID, RunID: runID, Status: "completed", Lane: "graph", diagnosticPhases: nil}
	result.CreatedDreams = 0
	result.ProviderProposals = 0
	result.Error = "provider failed"
	svc.recordRunDiagnostic(context.Background(), result)
	require.Equal(t, "unavailable", repo.recorded[0].CaptureState)
	require.Equal(t, "run_completed_with_error", repo.recorded[0].CaptureReason)

	repo.recorded = nil
	recorder := newDreamDiagnosticExchangeRecorder(observability.NewCredentialProtector())
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{Component: "dream", Model: "fixture", ResponseBody: []byte(`{"ok":true}`)})
	result = &RunCycleResult{TeamID: teamID, RunID: runID, Status: "completed", Lane: "graph", diagnosticPhases: []runDiagnosticPhase{{phase: "target", outcome: "selected"}}}
	svc.recordRunDiagnostic(withDreamDiagnosticRecorder(context.Background(), recorder), result)
	require.Equal(t, "run", repo.recorded[0].Phase)
	require.Equal(t, "evaluated_zero", repo.recorded[0].Outcome)
	require.Equal(t, "captured", repo.recorded[0].CaptureState)
	require.NotEmpty(t, repo.recorded[0].Payload)
	require.Equal(t, "target", repo.recorded[1].Phase)

	repo.recorded = nil
	repo.failPhase = 1
	result = &RunCycleResult{TeamID: teamID, RunID: runID, RunDate: "2026-09-21", Status: "completed", diagnosticPhases: []runDiagnosticPhase{{phase: "provider", outcome: "failed"}}}
	svc.recordRunDiagnostic(context.Background(), result)
	require.Len(t, repo.recorded, 3)
	require.Equal(t, "run", repo.recorded[0].Phase)
	require.Equal(t, "provider", repo.recorded[1].Phase)
	require.Equal(t, "unavailable", repo.recorded[2].CaptureState)

	repo.recorded = nil
	repo.failPhaseAlways = true
	svc.recordHypothesisDiagnostic(context.Background(), &dreamcontract.HypothesisRecord{TeamID: teamID, HypothesisID: uuid.NewString(), CycleRunID: runID}, "confirmation", "accepted", "", nil)
	require.Len(t, repo.recorded, 2)
	svc.recordHypothesisDiagnostic(context.Background(), &dreamcontract.HypothesisRecord{TeamID: teamID, HypothesisID: uuid.NewString(), CycleRunID: runID}, "confirmation", "rejected", "provider failed", nil)
	require.Equal(t, "unavailable", repo.recorded[2].CaptureState)
	svc.recordHypothesisDiagnostic(context.Background(), nil, "confirmation", "accepted", "", nil)
	svc.recordHypothesisDiagnostic(context.Background(), &dreamcontract.HypothesisRecord{TeamID: teamID, HypothesisID: uuid.NewString()}, "confirmation", "accepted", "", nil)
}

func TestDiagnosticPurgerCancellationAndDefaultInterval(t *testing.T) {
	RunDiagnosticPurger(context.Background(), nil, time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RunDiagnosticPurger(ctx, &diagnosticRepositoryStub{}, 0, nil)

	called := make(chan struct{}, 1)
	repo := &diagnosticRepositoryStub{purgeCalled: called, purgeDeleted: 1}
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunDiagnosticPurger(ctx, repo, time.Millisecond, slog.Default())
		close(done)
	}()
	select {
	case <-called:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("purger did not tick")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("purger did not stop")
	}

	called = make(chan struct{}, 1)
	repo = &diagnosticRepositoryStub{purgeCalled: called, purgeErr: errors.New("purge failed")}
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() {
		RunDiagnosticPurger(ctx, repo, time.Millisecond, slog.Default())
		close(done)
	}()
	select {
	case <-called:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("purger did not report failure")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("purger failure path did not stop")
	}
}

func TestDiagnosticPurgerDrainsFullBatches(t *testing.T) {
	called := make(chan struct{}, 3)
	repo := &diagnosticRepositoryStub{purgeCalled: called, purgeSequence: []int{100, 100, 3}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunDiagnosticPurger(ctx, repo, time.Millisecond, nil)
		close(done)
	}()
	for range 3 {
		select {
		case <-called:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("purger did not drain all full batches")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("purger did not stop")
	}
}
