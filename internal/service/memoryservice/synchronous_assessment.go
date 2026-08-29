package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const synchronousRememberMaxAssessorTurns = 3

type SynchronousRememberAssessmentDependencies struct {
	Catalog  SubmissionAssessmentCatalog
	Provider assessor.Provider
	Limits   assessor.SemanticAssessmentLimits
	Metrics  observability.DiscoverabilityMetrics
	Logger   observability.LogProvider
}

type SynchronousRememberAssessmentInput struct {
	TeamID         string
	OwnerProfileID string
	IngestID       string
	Proposal       map[string]any
	Evidence       []repository.EvidenceInput
}

type SynchronousRememberAssessmentResult struct {
	Response  assessor.SemanticAssessmentResponse
	Request   assessor.SemanticAssessmentRequest
	Plan      submissionAssessmentPlan
	Placement *repository.CreateIngestResult
	Model     string
	Tokenizer string
}

type NoSupportedMemoryError = submissionAssessmentNoSupportedMemoryError

func IsRememberStaleInputError(err error) bool {
	return isRememberStaleInputError(err)
}

// BuildSynchronousRememberCommitInput rebuilds the deterministic plan from
// the transaction-owned placement identities before converting the already
// validated assessor response into the existing semantic commit contract.
func BuildSynchronousRememberCommitInput(
	placement *repository.CreateIngestResult,
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	response assessor.SemanticAssessmentResponse,
	assessment *repository.SubmissionAssessment,
) (repository.CommitSubmissionAssessmentInput, error) {
	plan, err := buildSubmissionAssessmentPlan(placement)
	if err != nil {
		return repository.CommitSubmissionAssessmentInput{}, err
	}
	commit, err := submissionAssessmentCommitInput(run, scope, plan, response, assessment, false)
	if err != nil {
		return repository.CommitSubmissionAssessmentInput{}, err
	}
	// Synchronous Remember's embedding plan includes only observations promoted to durable facts.
	for index := range commit.RelationshipObservations {
		commit.RelationshipObservations[index].Observation.PromoteToFact = true
	}
	return commit, nil
}

func BuildSynchronousRememberPreviewCommitInput(prepared *SynchronousRememberAssessmentResult) (repository.CommitSubmissionAssessmentInput, error) {
	if prepared == nil || prepared.Placement == nil {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("synchronous Remember assessment state is required")
	}
	encoded, err := json.Marshal(prepared.Response)
	if err != nil {
		return repository.CommitSubmissionAssessmentInput{}, err
	}
	canonical, err := assessor.CanonicalJSON(encoded)
	if err != nil {
		return repository.CommitSubmissionAssessmentInput{}, err
	}
	assessment := &repository.SubmissionAssessment{
		AssessmentID: uuid.NewString(), RequestID: prepared.Request.RequestID, Model: prepared.Model,
		ResponseHash: semanticAssessmentHash(canonical),
	}
	run := repository.PlacementRun{
		TeamID: prepared.Placement.TeamID, OwnerProfileID: prepared.Placement.OwnerProfileID,
		IngestID: prepared.Placement.IngestID, PlacementRunID: prepared.Placement.PlacementRunID,
	}
	scope := repository.SubmissionAssessmentRunScope{
		TeamID: run.TeamID, OwnerProfileID: run.OwnerProfileID, IngestID: run.IngestID,
		PlacementRunID: run.PlacementRunID, WorkerID: "synchronous-remember", ExpectedAttempts: 1, MaxAttempts: 1,
	}
	commit, err := submissionAssessmentCommitInput(run, scope, prepared.Plan, prepared.Response, assessment, false)
	if err != nil {
		return repository.CommitSubmissionAssessmentInput{}, err
	}
	for index := range commit.RelationshipObservations {
		commit.RelationshipObservations[index].Observation.PromoteToFact = true
	}
	return commit, nil
}

func BuildSynchronousRememberAssessmentPersistenceInput(
	placement *repository.CreateIngestResult,
	prepared *SynchronousRememberAssessmentResult,
) (repository.PersistSubmissionAssessmentInput, error) {
	if placement == nil || prepared == nil {
		return repository.PersistSubmissionAssessmentInput{}, errors.New("synchronous Remember assessment state is required")
	}
	encoded, err := json.Marshal(prepared.Response)
	if err != nil {
		return repository.PersistSubmissionAssessmentInput{}, err
	}
	canonical, err := assessor.CanonicalJSON(encoded)
	if err != nil {
		return repository.PersistSubmissionAssessmentInput{}, err
	}
	return repository.PersistSubmissionAssessmentInput{
		TeamID: placement.TeamID, OwnerProfileID: placement.OwnerProfileID, IngestID: placement.IngestID,
		PlacementRunID: placement.PlacementRunID, RequestID: prepared.Request.RequestID,
		AssessorContractVersion: domain.ContractVersion, Model: prepared.Model, Tokenizer: prepared.Tokenizer,
		ProviderTurns: prepared.Response.ProviderTurns, InputTokens: prepared.Response.InputTokens,
		OutputTokens: prepared.Response.OutputTokens, CandidateContextTokens: prepared.Request.CandidateContextTokens,
		CandidateContextTruncated: prepared.Request.CandidateContextTruncated, NormalizedResponse: canonical,
		ResponseHash: semanticAssessmentHash(canonical), ValidatedAt: time.Now().UTC(),
	}, nil
}

func BuildSynchronousRememberRejectedTerminalInput(
	scope repository.SubmissionAssessmentRunScope,
	results []repository.SubmissionRelationshipResultInput,
) repository.CompleteSubmissionAssessmentInput {
	return repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope, OutcomeKind: "submission_assessment_rejected",
		Status: string(domain.SemanticReviewRejected), Category: "rejected",
		Payload:             map[string]any{"assessor_contract": domain.ContractVersion, "failure_stage": "semantic_commit", "failure_code": string(SubmissionErrorNoSupportedMemory), "retryable": true, "next_action": string(SubmissionNextActionResubmitSubmission)},
		RelationshipResults: results,
	}
}

func BuildSynchronousRememberQuarantineTerminalInput(
	placement *repository.CreateIngestResult,
	response assessor.SemanticAssessmentResponse,
	scope repository.SubmissionAssessmentRunScope,
) (repository.CompleteSubmissionAssessmentInput, error) {
	plan, err := buildSubmissionAssessmentPlan(placement)
	if err != nil {
		return repository.CompleteSubmissionAssessmentInput{}, err
	}
	byFragment := map[string][]repository.SecuritySignalInput{}
	for _, signal := range response.SecuritySignals {
		item, ok := plan.itemsByEvidenceID[signal.EvidenceID]
		if !ok {
			return repository.CompleteSubmissionAssessmentInput{}, errors.New("submission assessor security signal references unknown evidence")
		}
		quote, err := assessor.SemanticEvidenceSpan(item.Fragment.Content, signal.Start, signal.End)
		if err != nil {
			return repository.CompleteSubmissionAssessmentInput{}, err
		}
		byFragment[item.Fragment.FragmentID] = append(byFragment[item.Fragment.FragmentID], repository.SecuritySignalInput{Kind: signal.Kind, Severity: "high", SpanStart: signal.Start, SpanEnd: signal.End, Quote: quote})
	}
	quarantines := make([]repository.SubmissionAssessmentSecurityQuarantineInput, 0, len(byFragment))
	for _, item := range plan.Items {
		if signals := byFragment[item.Fragment.FragmentID]; len(signals) != 0 {
			quarantines = append(quarantines, repository.SubmissionAssessmentSecurityQuarantineInput{FragmentID: item.Fragment.FragmentID, SecurityEventDraft: repository.SecurityEventDraft{EventKind: "verifier_signal", Decision: "quarantine", Reason: "semantic assessor reported security signal", Signals: signals}})
		}
	}
	if len(quarantines) == 0 {
		return repository.CompleteSubmissionAssessmentInput{}, errors.New("submission assessor security quarantine has no target")
	}
	return repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope, OutcomeKind: "submission_assessment_security", Status: string(domain.SemanticReviewQuarantined), Category: "quarantined",
		Payload: map[string]any{"assessor_contract": domain.ContractVersion}, SecurityQuarantines: quarantines,
		RelationshipResults: submissionAssessmentQuarantineRelationshipResults(plan),
	}, nil
}

// AssessSynchronousRemember prepares and validates the whole assessor
// conversation without reserving or persisting a placement workflow row.
func AssessSynchronousRemember(
	ctx context.Context,
	deps SynchronousRememberAssessmentDependencies,
	input SynchronousRememberAssessmentInput,
) (*SynchronousRememberAssessmentResult, error) {
	if deps.Catalog == nil || deps.Provider == nil {
		return nil, errors.New("synchronous Remember assessment dependencies are required")
	}
	if strings.TrimSpace(input.TeamID) == "" || strings.TrimSpace(input.OwnerProfileID) == "" || strings.TrimSpace(input.IngestID) == "" {
		return nil, errors.New("synchronous Remember assessment scope is required")
	}
	placement := synchronousAssessmentPlacement(input)
	plan, err := buildSubmissionAssessmentPlan(placement)
	if err != nil {
		return nil, err
	}
	worker := &submissionAssessmentPlacementWorkerService{
		catalog: deps.Catalog, provider: deps.Provider, limits: deps.Limits, metrics: deps.Metrics, logger: deps.Logger,
	}
	if worker.metrics == nil {
		worker.metrics = observability.NoopDiscoverabilityMetrics()
	}
	run := repository.PlacementRun{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID, PlacementRunID: placement.PlacementRunID}
	request, err := worker.buildRequest(ctx, run, plan, input.Proposal)
	if err != nil {
		return nil, err
	}
	refresh := func(refreshCtx context.Context) (assessor.SemanticAssessmentRequest, error) {
		return worker.buildRequest(refreshCtx, run, plan, input.Proposal)
	}
	session, turn, err := deps.Provider.Assess(ctx, request)
	if err != nil {
		return nil, err
	}
	response, finalRequest, err := completeSynchronousRememberTurns(ctx, deps.Provider, worker.metrics, deps.Limits, session, turn, request, refresh)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	if _, err := assessor.CanonicalJSON(encoded); err != nil {
		return nil, err
	}
	return &SynchronousRememberAssessmentResult{
		Response: response, Request: finalRequest, Plan: plan, Placement: placement,
		Model: deps.Provider.ModelName(), Tokenizer: assessmentTokenizer(deps.Limits),
	}, nil
}

func synchronousAssessmentPlacement(input SynchronousRememberAssessmentInput) *repository.CreateIngestResult {
	result := &repository.CreateIngestResult{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
		PlacementRunID: uuid.NewString(), Status: string(domain.PlacementRunQueued), Proposal: input.Proposal,
		Evidence: make([]repository.EvidenceFragment, 0, len(input.Evidence)),
		Items:    make([]repository.PlacementItem, 0, len(input.Evidence)),
	}
	for index, evidence := range input.Evidence {
		fragmentID := uuid.NewString()
		result.Evidence = append(result.Evidence, repository.EvidenceFragment{
			FragmentID: fragmentID, EvidenceIndex: index, Content: evidence.Content, ContentHash: evidence.ContentHash,
			Authority: evidence.Authority, SupersededEvidenceIDs: append([]string(nil), evidence.SupersedesEvidenceIDs...),
		})
		result.Items = append(result.Items, repository.PlacementItem{
			PlacementItemID: uuid.NewString(), FragmentID: fragmentID, EvidenceIndex: index,
			Status: string(domain.PlacementRunQueued), Category: "candidate", Version: 1,
		})
	}
	return result
}

func completeSynchronousRememberTurns(
	ctx context.Context,
	provider assessor.Provider,
	metrics observability.DiscoverabilityMetrics,
	limits assessor.SemanticAssessmentLimits,
	session assessor.SemanticAssessmentSession,
	turn assessor.SemanticAssessmentTurn,
	request assessor.SemanticAssessmentRequest,
	refresh func(context.Context) (assessor.SemanticAssessmentRequest, error),
) (assessor.SemanticAssessmentResponse, assessor.SemanticAssessmentRequest, error) {
	turns := 0
	for {
		turns++
		response := turn.Response
		validationErrors := append([]assessor.SemanticValidationError(nil), turn.ValidationErrors...)
		if len(validationErrors) == 0 {
			response, validationErrors = assessor.PrepareSemanticAssessmentResponse(request, response, limits)
		}
		if len(validationErrors) == 0 {
			response.ProviderTurns = turns
			return response, request, nil
		}
		observability.RecordAssessorValidationFailure(metrics, assessmentValidationStage(turn.ValidationStage))
		if turns >= synchronousRememberMaxAssessorTurns {
			return assessor.SemanticAssessmentResponse{}, request, &assessor.MalformedResponseError{
				Provider: "semantic_assessor", Message: "semantic assessor response remained invalid after bounded correction",
				FailureClass: "malformed_exhausted", Attempts: turns, ValidationStage: assessmentValidationStage(turn.ValidationStage),
			}
		}
		nextRequest, err := refresh(ctx)
		if err != nil {
			return assessor.SemanticAssessmentResponse{}, request, &submissionAssessmentConsumedTurnsError{cause: err, providerTurns: turns}
		}
		turn, err = provider.Repair(ctx, session, assessor.SemanticAssessmentRepairRequest{Request: nextRequest, ValidationErrors: validationErrors})
		if err != nil {
			return assessor.SemanticAssessmentResponse{}, request, &submissionAssessmentConsumedTurnsError{cause: err, providerTurns: turns}
		}
		request = nextRequest
	}
}
