package memoryservice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/stretchr/testify/require"
)

type synchronousAssessmentSessionStub struct{}

func (synchronousAssessmentSessionStub) SessionID() string { return "synchronous-assessment" }

type synchronousAssessmentProviderStub struct {
	repairs   int
	repairErr error
}

func (*synchronousAssessmentProviderStub) Assess(context.Context, assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	return synchronousAssessmentSessionStub{}, invalidSynchronousAssessmentTurn(), nil
}

func (s *synchronousAssessmentProviderStub) Repair(context.Context, assessor.SemanticAssessmentSession, assessor.SemanticAssessmentRepairRequest) (assessor.SemanticAssessmentTurn, error) {
	s.repairs++
	if s.repairErr != nil {
		return assessor.SemanticAssessmentTurn{}, s.repairErr
	}
	return invalidSynchronousAssessmentTurn(), nil
}

func (*synchronousAssessmentProviderStub) ModelName() string { return "synchronous-test" }

func invalidSynchronousAssessmentTurn() assessor.SemanticAssessmentTurn {
	return assessor.SemanticAssessmentTurn{Turn: 1, ValidationStage: "response_contract", ValidationErrors: []assessor.SemanticValidationError{{Field: "response", Message: "invalid"}}}
}

func TestCompleteSynchronousRememberTurnsCountsProviderCallsNotProviderTurnNumbers(t *testing.T) {
	provider := &synchronousAssessmentProviderStub{}
	_, _, err := completeSynchronousRememberTurns(
		context.Background(), provider, observability.NoopDiscoverabilityMetrics(), assessor.DefaultSemanticAssessmentLimits(),
		synchronousAssessmentSessionStub{}, invalidSynchronousAssessmentTurn(), assessor.SemanticAssessmentRequest{},
		func(context.Context) (assessor.SemanticAssessmentRequest, error) {
			return assessor.SemanticAssessmentRequest{}, nil
		},
	)

	var malformed *assessor.MalformedResponseError
	if !errors.As(err, &malformed) {
		t.Fatalf("expected malformed response exhaustion, got %v", err)
	}
	if malformed.Attempts != synchronousRememberMaxAssessorTurns {
		t.Fatalf("attempts = %d, want %d", malformed.Attempts, synchronousRememberMaxAssessorTurns)
	}
	if provider.repairs != synchronousRememberMaxAssessorTurns-1 {
		t.Fatalf("repairs = %d, want %d", provider.repairs, synchronousRememberMaxAssessorTurns-1)
	}
}

func TestCompleteSynchronousRememberTurnsPreservesConsumedTurnsOnRepairFailure(t *testing.T) {
	repairErr := errors.New("assessor repair unavailable")
	provider := &synchronousAssessmentProviderStub{repairErr: repairErr}
	_, _, err := completeSynchronousRememberTurns(
		context.Background(), provider, observability.NoopDiscoverabilityMetrics(), assessor.DefaultSemanticAssessmentLimits(),
		synchronousAssessmentSessionStub{}, invalidSynchronousAssessmentTurn(), assessor.SemanticAssessmentRequest{},
		func(context.Context) (assessor.SemanticAssessmentRequest, error) {
			return assessor.SemanticAssessmentRequest{}, nil
		},
	)

	require.ErrorIs(t, err, repairErr)
	require.Equal(t, 1, SynchronousRememberAssessmentConsumedProviderTurns(err))
}

func TestIsSemanticAssessmentInputBudgetErrorRecognizesBoundedPreflightStages(t *testing.T) {
	for _, stage := range []string{"entity_catalog", "catalog_context", "assessment_input", "predicate_options_overflow"} {
		t.Run(stage, func(t *testing.T) {
			err := deterministicSemanticAssessmentPreflightError(stage, "budget exceeded")
			if !IsSemanticAssessmentInputBudgetError(err) {
				t.Fatalf("stage %q was not classified as an input budget error", stage)
			}
		})
	}
	for _, stage := range []string{"catalog_context_validation", "trusted_context_validation", "placement_load"} {
		t.Run(stage, func(t *testing.T) {
			err := deterministicSemanticAssessmentPreflightError(stage, "validation failed")
			if IsSemanticAssessmentInputBudgetError(err) {
				t.Fatalf("stage %q was incorrectly classified as an input budget error", stage)
			}
		})
	}
	if IsSemanticAssessmentInputBudgetError(errors.New("catalog_context exceeded")) {
		t.Fatal("untyped error was incorrectly classified as an input budget error")
	}
	if !IsSemanticAssessmentInputBudgetError(&assessor.MalformedResponseError{FailureClass: "input_budget"}) {
		t.Fatal("typed assessor input budget error was not classified")
	}
	if IsSemanticAssessmentInputBudgetError(&assessor.MalformedResponseError{FailureClass: "malformed_exhausted"}) {
		t.Fatal("malformed assessor response was incorrectly classified as an input budget error")
	}
}

func TestSynchronousRememberAssessmentBuildsCommitAndTerminalInputs(t *testing.T) {
	ledger, _, catalog, provider, _ := submissionAssessmentWorkerFixture(t)
	evidence := make([]repository.EvidenceInput, 0, len(ledger.placement.Evidence))
	for _, fragment := range ledger.placement.Evidence {
		evidence = append(evidence, repository.EvidenceInput{
			Content: fragment.Content, ContentHash: fragment.ContentHash, Authority: fragment.Authority,
		})
	}
	prepared, err := AssessSynchronousRemember(context.Background(), SynchronousRememberAssessmentDependencies{
		Catalog: catalog, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
	}, SynchronousRememberAssessmentInput{
		TeamID: ledger.run.TeamID, OwnerProfileID: ledger.run.OwnerProfileID, IngestID: ledger.run.IngestID,
		Proposal: ledger.placement.Proposal, Evidence: evidence,
	})
	if err != nil {
		t.Fatalf("assess synchronous Remember: %v", err)
	}
	if prepared == nil || prepared.Placement == nil || prepared.Request.RequestID == "" {
		t.Fatal("assessment did not return complete prepared state")
	}
	if prepared.Response.ProviderTurns != 1 {
		t.Fatalf("provider turns = %d, want 1", prepared.Response.ProviderTurns)
	}

	scope := repository.SubmissionAssessmentRunScope{
		TeamID: ledger.run.TeamID, OwnerProfileID: ledger.run.OwnerProfileID, IngestID: ledger.run.IngestID,
		PlacementRunID: prepared.Placement.PlacementRunID, WorkerID: "synchronous-remember", ExpectedAttempts: 1, MaxAttempts: 1,
	}
	preview, err := BuildSynchronousRememberPreviewCommitInput(prepared)
	if err != nil {
		t.Fatalf("build preview commit: %v", err)
	}
	if len(preview.RelationshipObservations) == 0 {
		t.Fatal("preview commit has no relationship observations")
	}
	for _, observation := range preview.RelationshipObservations {
		if !observation.Observation.PromoteToFact {
			t.Fatal("synchronous preview observation was not promoted to fact")
		}
	}

	assessment := &repository.SubmissionAssessment{
		AssessmentID: uuid.NewString(), RequestID: prepared.Request.RequestID, ResponseHash: "response-hash", Model: prepared.Model,
	}
	commit, err := BuildSynchronousRememberCommitInput(prepared.Placement, *ledger.run, scope, prepared.Response, assessment)
	if err != nil {
		t.Fatalf("build commit: %v", err)
	}
	if len(commit.Items) != len(prepared.Placement.Items) || len(commit.RelationshipResults) != len(prepared.Plan.RelationshipTargets) {
		t.Fatalf("commit shape = %d items / %d relationship results, want %d / %d", len(commit.Items), len(commit.RelationshipResults), len(prepared.Placement.Items), len(prepared.Plan.RelationshipTargets))
	}
	for _, observation := range commit.RelationshipObservations {
		if !observation.Observation.PromoteToFact {
			t.Fatal("synchronous commit observation was not promoted to fact")
		}
	}

	persist, err := BuildSynchronousRememberAssessmentPersistenceInput(prepared.Placement, prepared)
	if err != nil {
		t.Fatalf("build assessment persistence: %v", err)
	}
	if len(persist.NormalizedResponse) == 0 || persist.ResponseHash == "" || persist.AssessorContractVersion != domain.ContractVersion {
		t.Fatal("assessment persistence omitted canonical response metadata")
	}

	rejected := BuildSynchronousRememberRejectedTerminalInput(scope, []repository.SubmissionRelationshipResultInput{{RelationshipRef: "r:unsupported", Disposition: "not_stored", Reason: "not_supported_by_evidence"}})
	if rejected.Status != string(domain.SemanticReviewRejected) || len(rejected.RelationshipResults) != 1 {
		t.Fatalf("rejected terminal = %#v", rejected)
	}

	quarantineResponse := prepared.Response
	quarantineResponse.SecuritySignals = []assessor.SemanticAssessmentSecuritySignal{{
		EvidenceID: "evidence:0", Kind: "instruction_override", Start: 0, End: 5,
	}}
	quarantine, err := BuildSynchronousRememberQuarantineTerminalInput(prepared.Placement, quarantineResponse, scope)
	if err != nil {
		t.Fatalf("build quarantine terminal: %v", err)
	}
	if quarantine.Status != string(domain.SemanticReviewQuarantined) || len(quarantine.SecurityQuarantines) != 1 {
		t.Fatalf("quarantine terminal = %#v", quarantine)
	}

	for _, test := range []struct {
		name    string
		signals []assessor.SemanticAssessmentSecuritySignal
		wantErr string
	}{
		{name: "unknown evidence", signals: []assessor.SemanticAssessmentSecuritySignal{{EvidenceID: "missing", Start: 0, End: 1}}, wantErr: "unknown evidence"},
		{name: "invalid span", signals: []assessor.SemanticAssessmentSecuritySignal{{EvidenceID: "evidence:0", Start: -1, End: 1}}, wantErr: "invalid span"},
		{name: "no target", signals: nil, wantErr: "no target"},
	} {
		response := prepared.Response
		response.SecuritySignals = test.signals
		if _, err := BuildSynchronousRememberQuarantineTerminalInput(prepared.Placement, response, scope); err == nil {
			t.Errorf("%s quarantine input unexpectedly succeeded", test.name)
		}
	}
}

func TestAssessSynchronousRememberPreservesMissingKnownEntityAsStaleInput(t *testing.T) {
	ledger, _, catalog, provider, _ := submissionAssessmentWorkerFixture(t)
	relationships := ledger.placement.Proposal["relationship_hints"].([]any)
	subject := relationships[0].(map[string]any)["subject"].(map[string]any)
	delete(subject, "name")
	subject["known_entity_id"] = uuid.NewString()

	evidence := make([]repository.EvidenceInput, 0, len(ledger.placement.Evidence))
	for _, fragment := range ledger.placement.Evidence {
		evidence = append(evidence, repository.EvidenceInput{Content: fragment.Content, ContentHash: fragment.ContentHash, Authority: fragment.Authority})
	}

	_, err := AssessSynchronousRemember(context.Background(), SynchronousRememberAssessmentDependencies{
		Catalog: catalog, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
	}, SynchronousRememberAssessmentInput{
		TeamID: ledger.run.TeamID, OwnerProfileID: ledger.run.OwnerProfileID, IngestID: ledger.run.IngestID,
		Proposal: ledger.placement.Proposal, Evidence: evidence,
	})

	require.Error(t, err)
	require.True(t, IsRememberStaleInputError(err))
	require.Zero(t, provider.calls)
}

func TestSynchronousRememberAssessmentBuildersRejectIncompleteState(t *testing.T) {
	if _, err := BuildSynchronousRememberPreviewCommitInput(nil); err == nil {
		t.Fatal("nil preview state must fail")
	}
	if _, err := BuildSynchronousRememberAssessmentPersistenceInput(nil, nil); err == nil {
		t.Fatal("nil persistence state must fail")
	}
	if _, err := BuildSynchronousRememberQuarantineTerminalInput(nil, assessor.SemanticAssessmentResponse{}, repository.SubmissionAssessmentRunScope{}); err == nil {
		t.Fatal("nil quarantine placement must fail")
	}
	if _, err := BuildSynchronousRememberCommitInput(nil, repository.PlacementRun{}, repository.SubmissionAssessmentRunScope{}, assessor.SemanticAssessmentResponse{}, &repository.SubmissionAssessment{AssessmentID: "assessment"}); err == nil {
		t.Fatal("nil commit placement must fail")
	}
	ledger, _, _, _, _ := submissionAssessmentWorkerFixture(t)
	invalidPrepared := &SynchronousRememberAssessmentResult{Placement: ledger.placement, Response: assessor.SemanticAssessmentResponse{}, Request: assessor.SemanticAssessmentRequest{}}
	if _, err := BuildSynchronousRememberPreviewCommitInput(invalidPrepared); err == nil {
		t.Fatal("incomplete preview response must fail")
	}
	if _, err := AssessSynchronousRemember(context.Background(), SynchronousRememberAssessmentDependencies{}, SynchronousRememberAssessmentInput{}); err == nil {
		t.Fatal("missing assessment dependencies must fail")
	}
}

func TestAssessSynchronousRememberRejectsScopeCatalogAndProviderFailures(t *testing.T) {
	newFixture := func() (*submissionAssessmentWorkerLedgerStub, *submissionAssessmentWorkerCatalogStub, *submissionAssessmentWorkerProviderStub, SynchronousRememberAssessmentDependencies, SynchronousRememberAssessmentInput) {
		ledger, _, catalog, provider, _ := submissionAssessmentWorkerFixture(t)
		input := synchronousAssessmentInput(ledger)
		return ledger, catalog, provider, SynchronousRememberAssessmentDependencies{
			Catalog: catalog, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
		}, input
	}

	{
		_, _, _, deps, input := newFixture()
		input.TeamID = ""
		if _, err := AssessSynchronousRemember(context.Background(), deps, input); err == nil {
			t.Fatal("missing synchronous assessment scope must fail")
		}
	}
	{
		_, _, _, deps, input := newFixture()
		input.Proposal = map[string]any{}
		if _, err := AssessSynchronousRemember(context.Background(), deps, input); err == nil {
			t.Fatal("invalid synchronous assessment plan must fail")
		}
	}
	{
		_, catalog, _, deps, input := newFixture()
		catalog.entityErr = errors.New("catalog unavailable")
		if _, err := AssessSynchronousRemember(context.Background(), deps, input); err == nil {
			t.Fatal("catalog failure must fail assessment")
		}
	}
	{
		_, _, provider, deps, input := newFixture()
		provider.startErr = errors.New("assessor unavailable")
		if _, err := AssessSynchronousRemember(context.Background(), deps, input); err == nil {
			t.Fatal("provider assessment failure must fail assessment")
		}
	}
	{
		_, catalog, provider, deps, input := newFixture()
		provider.responseForTurn = func(assessor.SemanticAssessmentRequest, int) (assessor.SemanticAssessmentResponse, error) {
			catalog.entityErr = errors.New("catalog refresh unavailable")
			return assessor.SemanticAssessmentResponse{}, nil
		}
		if _, err := AssessSynchronousRemember(context.Background(), deps, input); err == nil {
			t.Fatal("request refresh failure must fail assessment")
		}
	}
	{
		_, _, provider, deps, input := newFixture()
		provider.responseForTurn = func(assessor.SemanticAssessmentRequest, int) (assessor.SemanticAssessmentResponse, error) {
			return assessor.SemanticAssessmentResponse{}, nil
		}
		provider.repairErr = errors.New("assessor repair unavailable")
		if _, err := AssessSynchronousRemember(context.Background(), deps, input); err == nil {
			t.Fatal("provider repair failure must fail assessment")
		}
	}
}

func TestIsRememberStaleInputErrorRecognizesAllFences(t *testing.T) {
	errorsToCheck := []error{
		errSubmissionAssessmentStaleInput,
		repository.ErrSourceRevisionConflict,
		repository.ErrEvidenceLifecycleConflict,
		repository.ErrConflictContextStale,
		repository.ErrRememberExactReferenceStale,
		repository.ErrCorrectionTargetStale,
		repository.ErrPlacementStaleSource,
	}
	for _, candidate := range errorsToCheck {
		if !IsRememberStaleInputError(errors.Join(errors.New("wrapped"), candidate)) {
			t.Errorf("stale input error %v was not recognized", candidate)
		}
	}
	if !IsRememberStaleInputError(&repository.RememberPreflightError{Issues: []repository.RememberPreflightIssue{{Code: "stale"}}}) {
		t.Fatal("remember preflight stale issue was not recognized")
	}
	if !IsRememberStaleInputError(&repository.RememberPreflightError{Issues: []repository.RememberPreflightIssue{{Path: "/evidence/0/source_revision", Code: "conflict"}}}) {
		t.Fatal("source revision conflict preflight was not recognized")
	}
	if !IsRememberStaleInputError(&repository.RememberPreflightError{Issues: []repository.RememberPreflightIssue{{Path: "/evidence/0/supersedes_evidence_ids/0", Code: "unavailable"}}}) {
		t.Fatal("unavailable supersession target was not recognized")
	}
	if IsRememberStaleInputError(&repository.RememberPreflightError{Issues: []repository.RememberPreflightIssue{{Path: "/relationships/0/subject/entity_kind", Code: "conflict"}}}) {
		t.Fatal("relationship entity conflict was incorrectly recognized as stale input")
	}
	if IsRememberStaleInputError(errors.New("unrelated")) {
		t.Fatal("unrelated error was classified as stale input")
	}
}

func synchronousAssessmentInput(ledger *submissionAssessmentWorkerLedgerStub) SynchronousRememberAssessmentInput {
	evidence := make([]repository.EvidenceInput, 0, len(ledger.placement.Evidence))
	for _, fragment := range ledger.placement.Evidence {
		evidence = append(evidence, repository.EvidenceInput{
			Content: fragment.Content, ContentHash: fragment.ContentHash, Authority: fragment.Authority,
		})
	}
	return SynchronousRememberAssessmentInput{
		TeamID: ledger.run.TeamID, OwnerProfileID: ledger.run.OwnerProfileID, IngestID: ledger.run.IngestID,
		Proposal: ledger.placement.Proposal, Evidence: evidence,
	}
}
