package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

type synchronousAssessmentSessionStub struct{ id string }

func (s *synchronousAssessmentSessionStub) SessionID() string { return s.id }

type synchronousAssessmentProviderStub struct {
	model           string
	response        func(assessor.SemanticAssessmentRequest, int) assessor.SemanticAssessmentResponse
	err             error
	errorTurn       assessor.SemanticAssessmentTurn
	repairErr       error
	repairErrorTurn assessor.SemanticAssessmentTurn
	assessTurn      int
	repairTurn      int
	calls           int
	repairCalls     int
	session         *synchronousAssessmentSessionStub
	beforeCall      func(context.Context)
	repairErrors    []assessor.SemanticValidationError
	assessRequest   assessor.SemanticAssessmentRequest
	repairRequest   assessor.SemanticAssessmentRequest
}

func (s *synchronousAssessmentProviderStub) Assess(ctx context.Context, request assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	s.calls++
	s.assessRequest = request
	if s.beforeCall != nil {
		s.beforeCall(ctx)
	}
	if s.err != nil {
		return nil, s.errorTurn, s.err
	}
	if s.session == nil {
		s.session = &synchronousAssessmentSessionStub{id: "assessment-session"}
	}
	turn := s.calls
	if s.assessTurn > 0 {
		turn = s.assessTurn
	}
	response := s.response(request, turn)
	return s.session, assessor.SemanticAssessmentTurn{Response: response, Turn: turn}, nil
}

func (s *synchronousAssessmentProviderStub) Repair(ctx context.Context, session assessor.SemanticAssessmentSession, request assessor.SemanticAssessmentRepairRequest) (assessor.SemanticAssessmentTurn, error) {
	s.repairCalls++
	s.repairErrors = append([]assessor.SemanticValidationError(nil), request.ValidationErrors...)
	s.repairRequest = request.Request
	if s.beforeCall != nil {
		s.beforeCall(ctx)
	}
	if session == nil || session.SessionID() != "assessment-session" {
		return assessor.SemanticAssessmentTurn{}, errors.New("unexpected assessment session")
	}
	if s.repairErr != nil {
		return s.repairErrorTurn, s.repairErr
	}
	turn := s.calls + s.repairCalls
	if s.repairTurn > 0 {
		turn = s.repairTurn
	}
	response := s.response(request.Request, turn)
	return assessor.SemanticAssessmentTurn{Response: response, Turn: turn}, nil
}

func (s *synchronousAssessmentProviderStub) ModelName() string {
	if strings.TrimSpace(s.model) == "" {
		return "synchronous-assessment-model"
	}
	return s.model
}

func TestSynchronousAssessmentBuildsRequestAndCommitInput(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		return validSynchronousAssessmentResponse(request)
	}

	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Equal(t, 1, fixture.provider.calls)
	require.Equal(t, "synchronous-remember:"+fixture.input.Scope.IngestID, prepared.Request.RequestID)
	require.Len(t, prepared.Request.Evidence, 2)
	require.NotEmpty(t, prepared.Assessment.ResponseHash)
	require.Equal(t, domain.ContractVersion, prepared.Assessment.AssessorContractVersion)
	require.Equal(t, fixture.provider.ModelName(), prepared.Assessment.Model)
	require.Equal(t, 1, prepared.Assessment.ProviderTurns)

	commit, err := BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{
		TeamID: fixture.input.Scope.TeamID, OwnerProfileID: fixture.input.Scope.OwnerProfileID,
		IngestID: fixture.input.Scope.IngestID, IdempotencyKey: "synchronous-commit",
		RequestHash: "sha256:request", SourceSummary: "document://assessment",
		Evidence: []repository.EvidenceInput{{Content: fixture.input.Snapshot.Evidence[0].Content}}, Assessment: prepared,
	})
	require.NoError(t, err)
	require.Equal(t, fixture.input.Scope.TeamID, commit.TeamID)
	require.Equal(t, fixture.input.Scope.OwnerProfileID, commit.OwnerProfileID)
	require.Equal(t, fixture.input.Scope.IngestID, commit.IngestID)
	require.Equal(t, prepared.Assessment.AssessmentID, commit.AssessmentID)
	require.Len(t, commit.Commit.Items, 2)
	require.Equal(t, 1, commit.ProviderTurns)

	securityResults, err := BuildSynchronousRememberEvidenceSecurityResults(prepared)
	require.NoError(t, err)
	require.Len(t, securityResults, 2)
}

func TestAssessSynchronousRememberRepairsCanonicalDuplicateConflictCitations(t *testing.T) {
	teamID, ownerID, ingestID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	content := "the same submitted claim"
	first := repository.EvidenceFragment{FragmentID: uuid.NewString(), EvidenceIndex: 0, Content: content}
	second := repository.EvidenceFragment{FragmentID: uuid.NewString(), EvidenceIndex: 1, Content: content}
	input := SynchronousAssessmentInput{
		Scope: RememberAssessmentScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID},
		Snapshot: RememberAssessmentSnapshot{
			Scope:    RememberAssessmentScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID},
			Evidence: []repository.EvidenceFragment{first, second},
			Items: []RememberAssessmentItem{
				{ItemID: uuid.NewString(), Fragment: first, DuplicateAssessmentRequired: true, ExactReuseEligible: true},
				{ItemID: uuid.NewString(), Fragment: second, DuplicateAssessmentRequired: true, ExactReuseEligible: true},
			},
		},
	}
	provider := &synchronousAssessmentProviderStub{response: func(request assessor.SemanticAssessmentRequest, call int) assessor.SemanticAssessmentResponse {
		response := assessor.SemanticAssessmentResponse{
			RequestID:                  request.RequestID,
			EvidenceEquivalenceResults: []assessor.SemanticAssessmentEvidenceEquivalenceResult{},
			EvidenceConflictResults:    []assessor.SemanticAssessmentEvidenceConflictResult{},
		}
		for _, evidence := range request.Evidence {
			response.EvidenceSecurityResults = append(response.EvidenceSecurityResults, assessor.SemanticAssessmentEvidenceSecurityResult{
				EvidenceID: evidence.EvidenceID, Decision: "pass", Signals: []assessor.SemanticAssessmentSecuritySignal{},
			})
		}
		if call == 1 {
			firstEvidence := request.Evidence[0]
			secondEvidence := request.Evidence[1]
			firstStart, _ := assessor.SemanticAssessmentBoundaryRef(firstEvidence, 0)
			firstEnd, _ := assessor.SemanticAssessmentBoundaryRef(firstEvidence, len([]rune(content)))
			secondStart, _ := assessor.SemanticAssessmentBoundaryRef(secondEvidence, 0)
			secondEnd, _ := assessor.SemanticAssessmentBoundaryRef(secondEvidence, len([]rune(content)))
			response.EvidenceConflictResults = []assessor.SemanticAssessmentEvidenceConflictResult{{Positions: []assessor.SemanticAssessmentEvidenceConflictPosition{
				{EvidenceID: firstEvidence.EvidenceID, StartRef: firstStart, EndRef: firstEnd},
				{EvidenceID: secondEvidence.EvidenceID, StartRef: secondStart, EndRef: secondEnd},
			}}}
		}
		return response
	}}
	deps := SynchronousAssessmentDependencies{
		Provider: provider,
		Limits:   assessor.DefaultSemanticAssessmentLimits(),
	}

	prepared, err := AssessSynchronousRemember(context.Background(), deps, input)
	require.NoError(t, err)
	require.Equal(t, 1, provider.calls)
	require.Equal(t, 1, provider.repairCalls)
	require.Equal(t, 2, prepared.Response.ProviderTurns)
	require.Empty(t, prepared.Response.EvidenceConflictResults)
}

func TestSubmissionAssessmentConflictCanonicalizationCoversCanonicalSources(t *testing.T) {
	candidateID := "candidate-fragment"
	plan := submissionAssessmentPlan{
		Items: []submissionAssessmentItem{
			{EvidenceID: "evidence:0", Fragment: repository.EvidenceFragment{FragmentID: "submitted-exact", Content: "exact"}},
			{EvidenceID: "evidence:1", Fragment: repository.EvidenceFragment{FragmentID: "submitted-batch", Content: "batch"}, DuplicateAssessmentRequired: true},
			{EvidenceID: "evidence:2", Fragment: repository.EvidenceFragment{FragmentID: "submitted-plain", Content: "plain"}},
		},
		exactDuplicateByEvidenceID: map[string]repository.RememberDuplicateResolution{
			"evidence:0": {CandidateFragmentID: "canonical-exact"},
		},
		knownEvidenceByID: map[string]repository.SubmissionAssessmentKnownEvidence{
			"known":       {FragmentID: "canonical-known"},
			"known-empty": {},
		},
		duplicateCandidatesByEvidenceID: map[string]repository.RememberDuplicateCandidateGroup{
			"evidence:1": {Candidates: []repository.RememberDuplicateCandidate{{FragmentID: candidateID}}},
		},
	}
	response := assessor.SemanticAssessmentResponse{
		EvidenceEquivalenceResults: []assessor.SemanticAssessmentEvidenceEquivalenceResult{
			{EvidenceID: "evidence:1", Action: "reuse", CandidateEvidenceID: &candidateID},
			{EvidenceID: "evidence:2", Action: "new"},
			{EvidenceID: "missing", Action: "reuse", CandidateEvidenceID: &candidateID},
			{EvidenceID: "evidence:0", Action: "reuse"},
		},
		EvidenceConflictResults: []assessor.SemanticAssessmentEvidenceConflictResult{
			{Positions: []assessor.SemanticAssessmentEvidenceConflictPosition{
				{EvidenceID: "evidence:0", Start: 0, End: 1},
				{EvidenceID: "evidence:1", Start: 0, End: 1},
				{EvidenceID: candidateID, Start: 0, End: 1},
				{EvidenceID: "known", Start: 0, End: 1},
				{EvidenceID: "unknown", Start: 0, End: 1},
			}},
			{Positions: []assessor.SemanticAssessmentEvidenceConflictPosition{{EvidenceID: "unknown", Start: 0, End: 1}}},
			{Positions: []assessor.SemanticAssessmentEvidenceConflictPosition{
				{EvidenceID: "evidence:0", Start: 0, End: 1},
				{EvidenceID: "evidence:1", Start: 0, End: 1},
				{EvidenceID: candidateID, Start: 0, End: 1},
				{EvidenceID: "known", Start: 0, End: 1},
			}},
		},
	}
	canonical := submissionAssessmentConflictCanonicalEvidence(plan, response)
	require.Equal(t, "canonical:canonical-exact", canonical["evidence:0"])
	require.Equal(t, "canonical:candidate-fragment", canonical["evidence:1"])
	require.Equal(t, "submitted:submitted-plain", canonical["evidence:2"])
	require.Equal(t, "canonical:canonical-known", canonical["known"])
	require.Equal(t, "canonical:known-empty", canonical["known-empty"])
	require.Equal(t, "canonical:candidate-fragment", canonical[candidateID])

	errs := validateSubmissionAssessmentEvidenceConflictCanonicalization(plan, response)
	require.Len(t, errs, 3)
	require.Equal(t, "duplicates another canonical position", errs[0].Message)
	require.Equal(t, "duplicates another canonical position", errs[1].Message)
	require.Equal(t, "duplicates another canonical conflict result", errs[2].Message)
}

func TestSynchronousAssessmentSupportsEvidenceOnlyRemember(t *testing.T) {
	runSynchronousEvidenceOnlyAssessorScenario(t)
}

func runSynchronousEvidenceOnlyAssessorScenario(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	input := fixture.input
	input.Snapshot = cloneRememberAssessmentSnapshot(input.Snapshot)

	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, input)
	require.NoError(t, err)
	require.Equal(t, 1, fixture.provider.calls)

	evidence := make([]repository.EvidenceInput, 0, len(input.Snapshot.Evidence))
	for _, fragment := range input.Snapshot.Evidence {
		evidence = append(evidence, repository.EvidenceInput{
			FragmentID: fragment.FragmentID, Content: fragment.Content, ContentHash: fragment.ContentHash,
			SourceType: "conversation", Authority: fragment.Authority,
		})
	}
	commit, err := BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{
		TeamID: input.Scope.TeamID, OwnerProfileID: input.Scope.OwnerProfileID, IngestID: input.Scope.IngestID,
		IdempotencyKey: "evidence-only", RequestHash: "sha256:evidence-only", Evidence: evidence, Assessment: prepared,
	})
	require.NoError(t, err)
	require.Len(t, commit.Commit.Items, len(evidence))
	require.Len(t, commit.EvidenceSecurityResults, len(evidence))
	for _, result := range commit.EvidenceSecurityResults {
		require.Equal(t, "pass", result.Decision)
		require.True(t, result.Safe)
		require.Empty(t, result.Signals)
	}
}

func TestSynchronousAssessmentRepairsInvalidResponseInSameSession(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, turn int) assessor.SemanticAssessmentResponse {
		response := validSynchronousAssessmentResponse(request)
		if turn <= SemanticMaxAssessorTurns {
			response.RequestID = "wrong-request"
		}
		return response
	}

	_, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.ErrorIs(t, err, ErrRememberProviderResponseInvalid)
	require.Equal(t, 1, fixture.provider.calls)
	require.Equal(t, SemanticMaxAssessorTurns-1, fixture.provider.repairCalls)
	require.Equal(t, SemanticMaxAssessorTurns, SynchronousAssessmentProviderTurns(err))
}

func TestBuildSynchronousRememberCommitInputRejectsInvalidSecurityTables(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SynchronousAssessmentResult)
		want   string
	}{
		{
			name: "duplicate result",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EvidenceSecurityResults = append(prepared.Response.EvidenceSecurityResults, prepared.Response.EvidenceSecurityResults[0])
			},
			want: "security result is duplicated",
		},
		{
			name: "unknown signal evidence",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EvidenceSecurityResults[0].EvidenceID = "evidence:missing"
				prepared.Response.EvidenceSecurityResults[0].Signals = []assessor.SemanticAssessmentSecuritySignal{{Start: 0, End: 1}}
			},
			want: "references unknown evidence",
		},
		{
			name: "invalid signal span",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EvidenceSecurityResults[0].Signals = []assessor.SemanticAssessmentSecuritySignal{{Start: 0, End: 999}}
			},
			want: "span is invalid",
		},
		{
			name: "missing result",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EvidenceSecurityResults = prepared.Response.EvidenceSecurityResults[:1]
			},
			want: "security result is missing",
		},
		{
			name: "unsupported decision",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EvidenceSecurityResults[0].Decision = "unknown"
			},
			want: "decision is unsupported",
		},
		{
			name: "pass with signal",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EvidenceSecurityResults[0].Signals = []assessor.SemanticAssessmentSecuritySignal{{Start: 0, End: 1}}
			},
			want: "pass contains unsafe signals",
		},
		{
			name: "reject without signal",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EvidenceSecurityResults[0].Decision = "reject"
			},
			want: "reject result has no security signal",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := synchronousAssessmentFixture(t)
			prepared := validPreparedSynchronousAssessment(t, fixture)
			test.mutate(prepared)
			_, err := BuildSynchronousRememberEvidenceSecurityResults(prepared)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestBuildSynchronousRememberCommitInputHandlesMissingAssessmentAndProviderTurnDefault(t *testing.T) {
	_, err := BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{})
	require.ErrorContains(t, err, "prepared result is required")

	fixture := synchronousAssessmentFixture(t)
	prepared := validPreparedSynchronousAssessment(t, fixture)
	prepared.Assessment.AssessmentID = ""
	_, err = BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{Assessment: prepared})
	require.ErrorContains(t, err, "persisted submission assessment is required")

	prepared = validPreparedSynchronousAssessment(t, fixture)
	prepared.Assessment.ProviderTurns = 0
	commit, err := BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{Assessment: prepared})
	require.NoError(t, err)
	require.Equal(t, 1, commit.ProviderTurns)
}

func TestSubmissionAssessmentSharedPoliciesAndFailureMeasurements(t *testing.T) {
	require.Equal(t, "response_contract", assessmentValidationStage(""))
	require.Equal(t, "assessment", assessmentValidationStage("assessment"))
	families := semanticAssessmentValidationFieldFamiliesForService([]assessor.SemanticValidationError{
		{Field: "request_id"}, {Field: "request_id"}, {Field: ""},
	})
	require.Equal(t, []string{"request_id", "other"}, families)

	base := errors.New("base")
	preflight := deterministicSemanticAssessmentPreflightError("assessment_input", "bounded")
	require.EqualError(t, preflight, "bounded")
	stage, ok := semanticAssessmentPreflightFailure(preflight)
	require.True(t, ok)
	require.Equal(t, "assessment_input", stage)
	measured := deterministicSemanticAssessmentPreflightErrorWithMeasurement("catalog_context", "measured", assessor.FailureMeasurement{Unit: "tokens", Observed: 2, Limit: 1})
	require.Equal(t, "catalog_context", mustPreflightStage(measured))
	withCause := deterministicSemanticAssessmentPreflightErrorWithCause("candidate_prefetch", "caused", base)
	require.ErrorIs(t, withCause, base)
	require.Equal(t, "candidate_prefetch", mustPreflightStage(withCause))
	require.Equal(t, "candidate_prefetch", mustPreflightStage(base))
	require.NoError(t, terminalizeAfterError(nil, func() error { return nil }))
	require.Error(t, terminalizeAfterError(base, func() error { return errors.New("completion") }))
	require.Equal(t, "o200k_base", assessmentTokenizer(assessor.SemanticAssessmentLimits{}))
	require.Equal(t, "custom", assessmentTokenizer(assessor.SemanticAssessmentLimits{Tokenizer: " custom "}))
	require.NotEmpty(t, semanticAssessmentHash([]byte("assessment")))

	malformed := &assessor.MalformedResponseError{FailureClass: "validation_failed", Attempts: 2}
	failureClass, attempts := semanticAssessmentMalformedFailure(malformed)
	require.Equal(t, "validation_failed", failureClass)
	require.Equal(t, 2, attempts)
	require.Equal(t, "malformed_response", mustMalformedFailure(base))
	require.Equal(t, "evidence:0:1:2", assessmentCandidateGroupKey("evidence:0", 1, 2))

	evidence := semanticAssessmentEvidence(fixtureEvidenceForHelpers(), "evidence:0")
	require.Equal(t, "evidence:0", evidence.EvidenceID)
	require.NotNil(t, new(" value "))

	require.Equal(t, "candidate_prefetch", mustPreflightStage(deterministicSemanticAssessmentPreflightError("", "default")))
}

func TestSynchronousAssessmentFailureDetailsPreserveBudgetMeasurement(t *testing.T) {
	err := &assessor.MalformedResponseError{
		FailureClass:    "input_budget",
		ValidationStage: "conversation_candidate_context_tokens",
		Measurement:     &assessor.FailureMeasurement{Unit: "tokens", Observed: 12, Limit: 10},
	}
	reason, details := SynchronousAssessmentFailureDetails(err)
	require.Equal(t, "assessor_conversation_candidate_context_exceeded", reason)
	require.Equal(t, "assessor.conversation_candidate_context", details["component"])
	require.Equal(t, 12, details["observed"])
	require.Equal(t, 10, details["limit"])
	require.Equal(t, true, details["server_owned"])
}

func TestAssessSynchronousRememberPreservesBudgetDetailsFromRepairFailure(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.provider.response = func(assessor.SemanticAssessmentRequest, int) assessor.SemanticAssessmentResponse {
		return assessor.SemanticAssessmentResponse{}
	}
	preflight := deterministicSemanticAssessmentPreflightErrorWithMeasurement(
		"assessment_input",
		"repair input exceeded the configured budget",
		assessor.FailureMeasurement{Unit: "tokens", Observed: 12, Limit: 10},
	)
	fixture.provider.repairErr = errors.Join(ErrRememberInputBudgetExceeded, preflight)

	_, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrRememberInputBudgetExceeded)
	reason, details := SynchronousAssessmentFailureDetails(err)
	require.Equal(t, "assessment_input", reason)
	require.Equal(t, "assessor.required_input", details["component"])
	require.Equal(t, true, details["client_controlled"])
	require.Equal(t, 12, details["observed"])
	require.Equal(t, 10, details["limit"])
	diagnostics := SynchronousAssessmentValidationDiagnostics(err)
	require.NotNil(t, diagnostics)
	turns, ok := diagnostics["turns"].([]any)
	require.True(t, ok)
	require.Len(t, turns, 1)
	require.Greater(t, turns[0].(map[string]any)["error_count"], 0)
}

func TestRememberInputIndexBounds(t *testing.T) {
	for _, raw := range []any{
		[]any{0}, []map[string]any{{"index": 0}}, []string{"1"}, "invalid",
	} {
		if rawValues := rememberArrayValues(raw); raw == "invalid" {
			require.Nil(t, rawValues)
		} else {
			require.NotNil(t, rawValues)
		}
	}
	for _, raw := range []any{int(1), int8(1), int16(1), int32(1), int64(1), uint(1), uint8(1), uint16(1), uint32(1), uint64(1), float64(1), float32(1), " 1 "} {
		index, ok := rememberEvidenceIndex(raw)
		require.True(t, ok)
		require.Equal(t, 1, index)
	}
	_, ok := rememberEvidenceIndex(1.5)
	require.False(t, ok)
}

func TestSynchronousAssessmentErrorClassificationAndHelpers(t *testing.T) {
	var nilConsumed *submissionAssessmentConsumedTurnsError
	require.Equal(t, "submission assessment session failed after consuming provider turns", nilConsumed.Error())
	require.Nil(t, nilConsumed.Unwrap())
	cause := errors.New("provider failed")
	consumed := &submissionAssessmentConsumedTurnsError{cause: cause, providerTurns: 4}
	require.EqualError(t, consumed, "provider failed")
	require.ErrorIs(t, consumed, cause)
	require.Equal(t, SemanticMaxAssessorTurns, SynchronousAssessmentProviderTurns(consumed))
	require.Equal(t, 0, SynchronousAssessmentProviderTurns(nil))
	require.Equal(t, SemanticMaxAssessorTurns, SynchronousAssessmentProviderTurns(&assessor.MalformedResponseError{Attempts: 99}))
	require.Equal(t, 0, SynchronousAssessmentProviderTurns(&assessor.MalformedResponseError{Attempts: -1}))

	engine := &assessmentEngine{}
	request := assessor.SemanticAssessmentRequest{}
	_, _, _, err := engine.assessRememberSession(context.Background(), request, func(context.Context) (assessor.SemanticAssessmentRequest, error) { return request, nil }, 0)
	require.ErrorContains(t, err, "provider is required")
	fixture := synchronousAssessmentFixture(t)
	engine = newSynchronousAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID).assessmentEngine
	_, _, _, err = engine.assessRememberSession(context.Background(), request, nil, 0)
	require.ErrorContains(t, err, "refresh is required")
	fixture.provider.err = errors.New("provider request failed")
	plan, planErr := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, planErr)
	request, err = engine.buildRequest(context.Background(), fixture.input.Scope, plan)
	require.NoError(t, err)
	_, _, _, err = engine.assessRememberSession(context.Background(), request, func(context.Context) (assessor.SemanticAssessmentRequest, error) { return request, nil }, 0)
	require.ErrorIs(t, err, fixture.provider.err)

	require.NoError(t, normalizeSynchronousAssessmentPreflightError(nil))
	budgetErr := deterministicSemanticAssessmentPreflightError("assessment_input", "too many tokens")
	require.ErrorIs(t, normalizeSynchronousAssessmentPreflightError(budgetErr), ErrRememberInputBudgetExceeded)
	framingErr := deterministicSemanticAssessmentPreflightError("provider_framing", "provider framing exceeds the configured budget")
	require.ErrorIs(t, normalizeSynchronousAssessmentPreflightError(framingErr), ErrRememberInputBudgetExceeded)
	otherErr := errors.New("other preflight")
	require.ErrorIs(t, normalizeSynchronousAssessmentPreflightError(otherErr), otherErr)
	for _, stale := range []error{
		errSubmissionAssessmentStaleInput, repository.ErrSourceRevisionConflict, repository.ErrEvidenceLifecycleConflict,
		repository.ErrEvidenceConflictStaleInput,
	} {
		require.True(t, IsRememberStaleInputError(stale), stale)
	}
	for _, stale := range []error{
		repository.ErrRememberExactReferenceStale, repository.ErrSubmissionAssessmentKnownEvidenceStale,
	} {
		require.True(t, IsRememberStaleInputError(stale), stale)
	}
	require.False(t, IsRememberStaleInputError(errors.New("fresh")))
}

func mustPreflightStage(err error) string {
	stage, _ := semanticAssessmentPreflightFailure(err)
	return stage
}

func mustMalformedFailure(err error) string {
	failureClass, _ := semanticAssessmentMalformedFailure(err)
	return failureClass
}

func fixtureEvidenceForHelpers() repository.EvidenceFragment {
	return repository.EvidenceFragment{FragmentID: "fragment:0", EvidenceIndex: 0, Content: "Alpha uses Beta."}
}

type synchronousAssessmentFixtureValue struct {
	input    SynchronousAssessmentInput
	deps     SynchronousAssessmentDependencies
	provider *synchronousAssessmentProviderStub
}

func synchronousAssessmentFixture(t *testing.T) synchronousAssessmentFixtureValue {
	t.Helper()
	scope := RememberAssessmentScope{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), IngestID: uuid.NewString(), SpaceID: uuid.NewString(), SpaceGeneration: 1}
	first := repository.EvidenceFragment{FragmentID: uuid.NewString(), EvidenceIndex: 0, Content: "Alpha uses Beta.", Authority: "primary", SourceID: uuid.NewString(), SourceRevisionID: uuid.NewString()}
	second := repository.EvidenceFragment{FragmentID: uuid.NewString(), EvidenceIndex: 1, Content: "Gamma has latency 42 ms.", Authority: "primary", SourceID: uuid.NewString(), SourceRevisionID: uuid.NewString()}
	snapshot := RememberAssessmentSnapshot{Scope: scope, Evidence: []repository.EvidenceFragment{first, second}, Items: []RememberAssessmentItem{{ItemID: uuid.NewString(), Fragment: first}, {ItemID: uuid.NewString(), Fragment: second}}}
	provider := &synchronousAssessmentProviderStub{model: "synchronous-assessment-model", response: func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		return validSynchronousAssessmentResponse(request)
	}}
	return synchronousAssessmentFixtureValue{input: snapshotAsInput(snapshot), deps: SynchronousAssessmentDependencies{Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits()}, provider: provider}
}

func snapshotAsInput(snapshot RememberAssessmentSnapshot) SynchronousAssessmentInput {
	return SynchronousAssessmentInput{Scope: snapshot.Scope, Snapshot: snapshot}
}

func validPreparedSynchronousAssessment(t *testing.T, fixture synchronousAssessmentFixtureValue) *SynchronousAssessmentResult {
	t.Helper()
	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.NoError(t, err)
	return prepared
}

func validSynchronousAssessmentResponse(request assessor.SemanticAssessmentRequest) assessor.SemanticAssessmentResponse {
	response := assessor.SemanticAssessmentResponse{RequestID: request.RequestID, EvidenceSecurityResults: []assessor.SemanticAssessmentEvidenceSecurityResult{}, EvidenceEquivalenceResults: []assessor.SemanticAssessmentEvidenceEquivalenceResult{}, EvidenceConflictResults: []assessor.SemanticAssessmentEvidenceConflictResult{}}
	for _, evidence := range request.Evidence {
		response.EvidenceSecurityResults = append(response.EvidenceSecurityResults, assessor.SemanticAssessmentEvidenceSecurityResult{EvidenceID: evidence.EvidenceID, Decision: "pass", Signals: []assessor.SemanticAssessmentSecuritySignal{}})
	}
	for _, group := range request.EvidenceEquivalenceCandidates {
		response.EvidenceEquivalenceResults = append(response.EvidenceEquivalenceResults, assessor.SemanticAssessmentEvidenceEquivalenceResult{EvidenceID: group.EvidenceID, Action: "new"})
	}
	return response
}

func assessmentEvidenceByID(evidence []assessor.SemanticReviewEvidence, id string) assessor.SemanticReviewEvidence {
	for _, item := range evidence {
		if item.EvidenceID == id {
			return item
		}
	}
	panic("assessment evidence is missing")
}

func cloneRememberAssessmentSnapshot(snapshot RememberAssessmentSnapshot) RememberAssessmentSnapshot {
	clone := snapshot
	clone.Evidence = append([]repository.EvidenceFragment(nil), snapshot.Evidence...)
	clone.Items = append([]RememberAssessmentItem(nil), snapshot.Items...)
	return clone
}
