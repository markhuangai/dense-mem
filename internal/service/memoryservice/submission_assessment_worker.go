package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

var errSubmissionAssessmentRequiresReview = errors.New("submission assessment requires review")

// SubmissionAssessmentCatalog provides the server-owned entity candidates and
// exact-plus-relevant predicate options used by one closed submission
// assessment. Predicate lookup must not require a complete team-wide catalog.
type SubmissionAssessmentCatalog interface {
	ListSubmissionAssessmentEntityCatalog(ctx context.Context, input repository.SubmissionAssessmentEntityCatalogInput) (repository.SubmissionAssessmentEntityCatalogResult, error)
	ResolveSemanticReviewPredicateCandidates(ctx context.Context, input repository.SemanticReviewPredicateResolutionInput) ([]repository.SemanticReviewPredicateResolution, error)
	ListSemanticAssessmentPredicateOptions(ctx context.Context, input repository.SemanticAssessmentPredicateOptionsInput) ([]repository.SemanticReviewPredicateCandidate, error)
}

type SubmissionAssessmentPlacementWorkerService interface {
	ProcessNextSubmissionAssessmentPlacement(ctx context.Context) (bool, error)
}

type SubmissionAssessmentPlacementWorkerDependencies struct {
	Ledger                    repository.LedgerRepository
	Assessments               repository.SubmissionAssessmentRepository
	Catalog                   SubmissionAssessmentCatalog
	Provider                  SemanticAssessorProvider
	Normalizer                RememberNormalizerProvider
	Limits                    verifier.SemanticAssessmentLimits
	GlobalConfidenceThreshold float64
	TeamID                    string
	WorkerID                  string
	Lease                     time.Duration
	Now                       func() time.Time
	Metrics                   observability.DiscoverabilityMetrics
	Logger                    observability.LogProvider
}

type submissionAssessmentPlacementWorkerService struct {
	ledger                    repository.LedgerRepository
	assessments               repository.SubmissionAssessmentRepository
	catalog                   SubmissionAssessmentCatalog
	provider                  SemanticAssessorProvider
	normalizer                RememberNormalizerProvider
	limits                    verifier.SemanticAssessmentLimits
	globalConfidenceThreshold float64
	teamID                    string
	workerID                  string
	lease                     time.Duration
	now                       func() time.Time
	metrics                   observability.DiscoverabilityMetrics
	logger                    observability.LogProvider
}

func NewSubmissionAssessmentPlacementWorkerService(
	deps SubmissionAssessmentPlacementWorkerDependencies,
) SubmissionAssessmentPlacementWorkerService {
	lease := deps.Lease
	if lease <= 0 {
		lease = time.Minute
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	normalizer := deps.Normalizer
	if normalizer == nil {
		if configured, ok := deps.Provider.(RememberNormalizerProvider); ok {
			normalizer = configured
		}
	}
	return &submissionAssessmentPlacementWorkerService{
		ledger:                    deps.Ledger,
		assessments:               deps.Assessments,
		catalog:                   deps.Catalog,
		provider:                  deps.Provider,
		normalizer:                normalizer,
		limits:                    deps.Limits,
		globalConfidenceThreshold: deps.GlobalConfidenceThreshold,
		teamID:                    strings.TrimSpace(deps.TeamID),
		workerID:                  strings.TrimSpace(deps.WorkerID),
		lease:                     lease,
		now:                       now,
		metrics:                   metrics,
		logger:                    deps.Logger,
	}
}

func (s *submissionAssessmentPlacementWorkerService) ProcessNextSubmissionAssessmentPlacement(ctx context.Context) (bool, error) {
	if err := s.validateDependencies(); err != nil {
		return false, err
	}
	run, err := s.ledger.ClaimNextPlacementRun(ctx, s.teamID, s.workerID, s.lease)
	if err != nil {
		return false, err
	}
	if run == nil {
		return false, nil
	}
	scope := submissionAssessmentRunScope(*run, s.workerID)
	placement, err := s.ledger.GetPlacementRun(ctx, repository.GetPlacementRunInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		IngestID:       run.IngestID,
	})
	if err != nil {
		return true, retryAfterError(err, func() error {
			return s.retryOrFail(ctx, *run, scope, "placement_load", false, false, err)
		})
	}
	if run.CorrelationID == "" {
		run.CorrelationID = placement.CorrelationID
		scope.CorrelationID = placement.CorrelationID
	}
	if strings.TrimSpace(placement.ContractVersion) != domain.ContractVersion {
		return true, s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "contract_superseded")
	}
	plan, err := buildSubmissionAssessmentPlan(placement)
	if err != nil {
		return true, terminalizeAfterError(err, func() error {
			return s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "trusted_context_validation")
		})
	}
	clientProposal := assessmentClientProposalWithoutTrustedContext(placement.Proposal)
	contents := make([]string, 0, len(plan.Items))
	for _, item := range plan.Items {
		contents = append(contents, item.Fragment.Content)
	}
	if scan, scanErr := scanSubmissionWithProviderProposal(contents, clientProposal); scanErr != nil {
		return true, s.completeDeterministicSecurityQuarantine(ctx, scope, plan, scan, "deterministic_security_scan")
	}

	request, err := s.buildRequest(ctx, *run, plan, placement.Proposal)
	if err != nil {
		stage, terminal := semanticAssessmentPreflightFailure(err)
		if terminal {
			return true, terminalizeAfterError(err, func() error {
				return s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", stage, err)
			})
		}
		return true, retryAfterError(err, func() error {
			return s.retryOrFail(ctx, *run, scope, stage, false, false, err)
		})
	}

	assessment, response, reused, providerAttempted, releaseProviderAttempt, err := s.loadOrAssess(ctx, *run, scope, request)
	if err != nil {
		if errors.Is(err, repository.ErrSubmissionAssessorAttemptConsumed) {
			return true, terminalizeAfterError(err, func() error {
				return s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_attempt_consumed")
			})
		}
		if providerAttempted && errors.Is(err, verifier.ErrVerifierMalformedResponse) {
			failureClass, providerTurns := semanticAssessmentMalformedFailure(err)
			return true, terminalizeAfterError(err, func() error {
				return s.completeTerminalWithFailure(ctx, scope, "assessment", failureClass, 0, providerTurns, err)
			})
		}
		if providerAttempted {
			return true, retryAfterError(err, func() error {
				return s.retryProviderFailure(ctx, *run, scope, "assessment", releaseProviderAttempt, verifier.ProviderFailureDetails(err))
			})
		}
		return true, retryAfterError(err, func() error {
			return s.retryOrFail(ctx, *run, scope, "assessment", providerAttempted, releaseProviderAttempt, err)
		})
	}
	if len(response.SecuritySignals) > 0 {
		return true, s.completeProviderSecurityQuarantine(ctx, scope, plan, response, "security_signal")
	}

	policy, err := s.assessments.LoadAutoWriteConfidencePolicy(ctx, repository.LoadAutoWriteConfidencePolicyInput{
		TeamID:          run.TeamID,
		OwnerProfileID:  run.OwnerProfileID,
		GlobalThreshold: s.globalConfidenceThreshold,
	})
	if err != nil {
		return true, retryAfterError(err, func() error {
			return s.retryOrFail(ctx, *run, scope, "confidence_policy", false, false, err)
		})
	}
	commitInput, err := submissionAssessmentCommitInput(*run, scope, plan, request, response, assessment, policy, reused)
	var reviewRequired *submissionAssessmentReviewRequiredError
	if errors.As(err, &reviewRequired) {
		return true, s.completeReview(ctx, scope, "policy_review", reviewRequired.Issues, reviewRequired.Truncated)
	}
	if errors.Is(err, errSubmissionAssessmentRequiresReview) {
		return true, s.completeReview(ctx, scope, "policy_review", nil, false)
	}
	if err != nil {
		return true, terminalizeAfterError(err, func() error {
			return s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "deterministic_policy")
		})
	}
	recordSubmissionAssessmentGateBands(s.metrics, commitInput)
	committed, err := s.assessments.CommitSubmissionAssessment(ctx, commitInput)
	if errors.Is(err, repository.ErrSubmissionAssessmentNonPromotable) {
		return true, s.completeReview(ctx, scope, "commit_review", []submissionAssessmentIssue{{
			Code: "semantic_commit_non_promotable", Component: "relationship", Message: "submission could not be promoted safely",
		}}, false)
	}
	if errors.Is(err, repository.ErrSubmissionPredicateRegistrationHeld) {
		return true, s.completeReview(ctx, scope, "commit_review", []submissionAssessmentIssue{{
			Code: "predicate_registration_conflict", Component: "predicate", Message: "predicate registration conflicted with current state",
		}}, false)
	}
	if errors.Is(err, repository.ErrConflictContextStale) {
		return true, s.completeReview(ctx, scope, "conflict_context_stale", []submissionAssessmentIssue{{
			Code: "conflict_context_stale", Component: "conflict", Message: "relationship conflict context changed before commit",
		}}, false)
	}
	if errors.Is(err, repository.ErrSubmissionAssessmentScopeMismatch) {
		return true, terminalizeAfterError(err, func() error {
			return s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_scope")
		})
	}
	if errors.Is(err, repository.ErrPlacementStaleSource) {
		return true, s.completeTerminal(ctx, scope, string(domain.SemanticReviewSuperseded), "failed", "stale_source")
	}
	if err != nil {
		return true, retryAfterError(err, func() error {
			return s.retryOrFail(ctx, *run, scope, "semantic_commit", false, false, err)
		})
	}
	if committed == nil {
		cause := errors.New("submission assessment worker: nil semantic commit result")
		return true, retryAfterError(cause, func() error {
			return s.retryOrFail(ctx, *run, scope, "semantic_commit", false, false, cause)
		})
	}
	s.logLifecycle(scope, "submission_completed", "completed", "semantic_commit", "semantic_commit_succeeded", nil)
	s.recordFirstDisposition(ctx, *run, committed.FirstDisposition)
	return true, nil
}

func (s *submissionAssessmentPlacementWorkerService) validateDependencies() error {
	if s.ledger == nil {
		return errors.New("submission assessment worker: ledger repository is required")
	}
	if s.assessments == nil {
		return errors.New("submission assessment worker: assessment repository is required")
	}
	if s.catalog == nil {
		return errors.New("submission assessment worker: semantic catalog is required")
	}
	if s.provider == nil {
		return errors.New("submission assessment worker: assessor provider is required")
	}
	if s.teamID == "" {
		return errors.New("submission assessment worker: team_id is required")
	}
	if s.workerID == "" {
		return errors.New("submission assessment worker: worker_id is required")
	}
	if s.globalConfidenceThreshold < 0 || s.globalConfidenceThreshold > 1 {
		return errors.New("submission assessment worker: global confidence threshold must be between 0 and 1")
	}
	return nil
}

func (s *submissionAssessmentPlacementWorkerService) loadOrAssess(
	ctx context.Context,
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	request verifier.SemanticAssessmentRequest,
) (*repository.SubmissionAssessment, verifier.SemanticAssessmentResponse, bool, bool, bool, error) {
	stored, err := s.assessments.LoadSubmissionAssessment(ctx, repository.LoadSubmissionAssessmentInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		PlacementRunID: run.PlacementRunID,
	})
	if err == nil {
		response, decodeErr := decodeStoredSubmissionAssessment(stored, request, s.limits)
		if decodeErr != nil {
			observability.RecordAssessorValidationFailure(s.metrics, "stored_response")
		} else {
			observability.RecordAssessorAssessmentPersistence(s.metrics, "reused")
			observability.RecordAssessorDuplicateRequestPrevention(s.metrics, "post_persist")
		}
		return stored, response, true, false, false, decodeErr
	}
	if !errors.Is(err, repository.ErrSubmissionAssessmentNotFound) {
		return nil, verifier.SemanticAssessmentResponse{}, false, false, false, err
	}
	reserved, err := s.assessments.ReserveSubmissionAssessorAttempt(ctx, repository.ReserveSubmissionAssessorAttemptInput{
		SubmissionAssessmentRunScope: scope,
	})
	if err != nil {
		return nil, verifier.SemanticAssessmentResponse{}, false, false, false, err
	}
	if !reserved {
		stored, err := s.assessments.LoadSubmissionAssessment(ctx, repository.LoadSubmissionAssessmentInput{
			TeamID:         run.TeamID,
			OwnerProfileID: run.OwnerProfileID,
			PlacementRunID: run.PlacementRunID,
		})
		if err == nil {
			response, decodeErr := decodeStoredSubmissionAssessment(stored, request, s.limits)
			if decodeErr != nil {
				observability.RecordAssessorValidationFailure(s.metrics, "stored_response")
			} else {
				observability.RecordAssessorAssessmentPersistence(s.metrics, "reused")
				observability.RecordAssessorDuplicateRequestPrevention(s.metrics, "post_persist")
			}
			return stored, response, true, false, false, decodeErr
		}
		if !errors.Is(err, repository.ErrSubmissionAssessmentNotFound) {
			return nil, verifier.SemanticAssessmentResponse{}, false, false, false, err
		}
		observability.RecordAssessorDuplicateRequestPrevention(s.metrics, "reservation")
		return nil, verifier.SemanticAssessmentResponse{}, false, false, false, repository.ErrSubmissionAssessorAttemptConsumed
	}

	started := time.Now()
	providerCtx := observability.WithMetricIdentity(ctx, run.TeamID, run.OwnerProfileID)
	providerCtx = observability.WithAIOperation(providerCtx, observability.AIOperationPlacementAssessment, 1)
	var response verifier.SemanticAssessmentResponse
	modelName := s.provider.ModelName()
	if s.normalizer != nil {
		modelName = s.normalizer.ModelName()
		var normalized verifier.RememberNormalizerResponse
		normalized, err = s.normalizer.NormalizeRemember(providerCtx, request)
		if err == nil {
			response, err = rememberNormalizerResponseToSemanticAssessment(request, normalized)
		}
	} else {
		response, err = s.provider.AssessSemantic(providerCtx, request)
	}
	if err != nil {
		outcome := "provider_error"
		releaseProviderAttempt := true
		if errors.Is(err, verifier.ErrVerifierMalformedResponse) {
			outcome = "malformed_exhausted"
			releaseProviderAttempt = false
		}
		observability.RecordAssessorCall(s.metrics, request.InputTokens, 0, time.Since(started).Seconds(), outcome)
		return nil, verifier.SemanticAssessmentResponse{}, false, true, releaseProviderAttempt, err
	}
	validationLimits := s.limits
	if s.normalizer != nil {
		validationLimits, err = rememberNormalizerFinalResponseLimits(validationLimits, response)
		if err != nil {
			return nil, verifier.SemanticAssessmentResponse{}, false, true, true, err
		}
	}
	normalized, validationErrors := verifier.PrepareSemanticAssessmentResponse(request, response, validationLimits)
	if len(validationErrors) > 0 {
		observability.RecordAssessorCall(s.metrics, request.InputTokens, response.OutputTokens, time.Since(started).Seconds(), "malformed_exhausted")
		observability.RecordAssessorValidationFailure(s.metrics, "response_contract")
		providerTurns := response.ProviderTurns
		if providerTurns <= 0 {
			providerTurns = 1
		}
		return nil, verifier.SemanticAssessmentResponse{}, false, true, false, &verifier.MalformedResponseError{
			Provider:     "semantic_assessor",
			Message:      "semantic assessor returned an invalid complete response",
			FailureClass: "malformed_response",
			Attempts:     providerTurns,
		}
	}
	inputTokens := normalized.InputTokens
	if inputTokens <= 0 {
		inputTokens = request.InputTokens
	}
	observability.RecordAssessorCall(s.metrics, inputTokens, normalized.OutputTokens, time.Since(started).Seconds(), "ok")
	normalizedJSON, err := json.Marshal(normalized)
	if err != nil {
		return nil, verifier.SemanticAssessmentResponse{}, false, true, false, err
	}
	canonicalJSON, err := verifier.CanonicalJSON(normalizedJSON)
	if err != nil {
		return nil, verifier.SemanticAssessmentResponse{}, false, true, false, err
	}
	persisted, existing, err := s.assessments.PersistSubmissionAssessment(ctx, repository.PersistSubmissionAssessmentInput{
		TeamID:                    run.TeamID,
		OwnerProfileID:            run.OwnerProfileID,
		IngestID:                  run.IngestID,
		PlacementRunID:            run.PlacementRunID,
		RequestID:                 request.RequestID,
		AssessorContractVersion:   domain.ContractVersion,
		Model:                     modelName,
		Tokenizer:                 assessmentTokenizer(s.limits),
		InputTokens:               inputTokens,
		OutputTokens:              normalized.OutputTokens,
		CandidateContextTokens:    request.CandidateContextTokens,
		CandidateContextTruncated: false,
		NormalizedResponse:        canonicalJSON,
		ResponseHash:              semanticAssessmentHash(canonicalJSON),
		ValidatedAt:               s.now().UTC(),
	})
	if err != nil {
		observability.RecordAssessorAssessmentPersistence(s.metrics, "error")
		return nil, verifier.SemanticAssessmentResponse{}, false, true, false, err
	}
	if existing {
		observability.RecordAssessorAssessmentPersistence(s.metrics, "reused")
		storedResponse, decodeErr := decodeStoredSubmissionAssessment(persisted, request, s.limits)
		if decodeErr != nil {
			observability.RecordAssessorValidationFailure(s.metrics, "stored_response")
		}
		return persisted, storedResponse, true, true, false, decodeErr
	}
	observability.RecordAssessorAssessmentPersistence(s.metrics, "persisted")
	return persisted, normalized, false, true, false, nil
}

func (s *submissionAssessmentPlacementWorkerService) retryProviderFailure(
	ctx context.Context,
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	stage string,
	releaseProviderAttempt bool,
	failure verifier.ProviderFailureMetadata,
) error {
	if run.MaxAttempts > 0 && run.Attempts >= run.MaxAttempts {
		return s.completeTerminalWithFailure(ctx, scope, stage, failure.Class, failure.StatusCode, 0)
	}
	requeued, err := s.assessments.RequeueSubmissionAssessment(ctx, repository.RequeueSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_attempt",
		Payload:                      semanticAssessmentRetryPayload(stage, true, failure),
		RetryAfter:                   failure.RetryAfter,
		ReleaseAssessorAttempt:       releaseProviderAttempt,
	})
	if err == nil && requeued == nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, errors.New("submission assessment worker: nil retry result"))
	}
	if err != nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, err)
	}
	if err == nil {
		s.logLifecycle(scope, "submission_retry_scheduled", "queued", stage, string(submissionFailureCode(stage, failure.Class)), requeued.NextAttemptAt)
	}
	return err
}

func (s *submissionAssessmentPlacementWorkerService) retryOrFail(
	ctx context.Context,
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	stage string,
	providerAttempted bool,
	releaseProviderAttempt bool,
	failureCause ...error,
) error {
	if run.MaxAttempts > 0 && run.Attempts >= run.MaxAttempts {
		return s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", stage, firstError(failureCause))
	}
	requeued, err := s.assessments.RequeueSubmissionAssessment(ctx, repository.RequeueSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_attempt",
		Payload:                      semanticAssessmentFailurePayload(stage, providerAttempted, firstError(failureCause)),
		ReleaseAssessorAttempt:       releaseProviderAttempt,
	})
	if err == nil && requeued == nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, errors.New("submission assessment worker: nil retry result"))
	}
	if err != nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, err)
	}
	if err == nil {
		s.logLifecycle(scope, "submission_retry_scheduled", "queued", stage, "retryable_failure", requeued.NextAttemptAt)
	}
	return err
}

func (s *submissionAssessmentPlacementWorkerService) completeTerminalWithFailure(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	stage string,
	failureClass string,
	providerStatus int,
	providerTurns int,
	failureCause ...error,
) error {
	failureCode := submissionFailureCode(stage, failureClass)
	if s.normalizer != nil {
		switch failureClass {
		case "malformed_exhausted", "malformed_response", "validation_failed":
			failureCode = SubmissionErrorNormalizationFailed
		case verifier.ProviderFailureClassTimeout, verifier.ProviderFailureClassRateLimited,
			verifier.ProviderFailureClassHTTPClient, verifier.ProviderFailureClassHTTPServer,
			verifier.ProviderFailureClassHTTPUnexpected, verifier.ProviderFailureClassTransport,
			verifier.ProviderFailureClassProtocol, verifier.ProviderFailureClassRequestInvalid,
			verifier.ProviderFailureClassProviderUnavailable:
			failureCode = SubmissionErrorNormalizerUnavailable
		}
		if failureCode == SubmissionErrorProcessingFailed {
			failureCode = SubmissionErrorRequiresResubmission
		}
	}
	payload := semanticAssessmentFailurePayload(stage, true, firstError(failureCause))
	payload["failure_code"] = string(failureCode)
	if failureClass = strings.TrimSpace(failureClass); failureClass != "" {
		failureClass = boundedPlacementFailureClass(failureClass)
		payload["failure_class"] = failureClass
		payload["failure_reason_code"] = placementFailureReasonCode(stage, failureClass)
	}
	if providerStatus > 0 {
		payload["provider_status"] = providerStatus
	}
	if providerTurns > 0 {
		payload["assessor_turns"] = providerTurns
	}
	completed, err := s.assessments.CompleteSubmissionAssessment(ctx, repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_terminal",
		Status:                       string(domain.SemanticReviewTerminalFailure),
		Category:                     "failed",
		Payload:                      payload,
	})
	if err == nil && completed == nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, errors.New("submission assessment worker: nil terminal result"))
	}
	if err != nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, err)
	}
	if err == nil && completed != nil {
		observability.RecordAssessorTerminalFailure(s.metrics, stage)
		s.logLifecycle(scope, "submission_failed", "failed", stage, string(failureCode), nil)
	}
	return err
}

func (s *submissionAssessmentPlacementWorkerService) completeTerminal(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	status, category, stage string,
	failureCause ...error,
) error {
	failureCode := submissionFailureCode(stage, "")
	if s.normalizer != nil && failureCode == SubmissionErrorProcessingFailed {
		failureCode = SubmissionErrorRequiresResubmission
	}
	var payload map[string]any
	if status == string(domain.SemanticReviewSuperseded) {
		payload = map[string]any{"assessor_contract": domain.ContractVersion}
	} else {
		payload = semanticAssessmentFailurePayload(stage, false, firstError(failureCause))
		payload["assessor_contract"] = domain.ContractVersion
		payload["failure_code"] = string(failureCode)
	}
	failureClass, _ := payload["failure_class"].(string)
	completed, err := s.assessments.CompleteSubmissionAssessment(ctx, repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_terminal",
		Status:                       status,
		Category:                     category,
		Payload:                      payload,
	})
	if err == nil && completed == nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, errors.New("submission assessment worker: nil terminal result"))
	}
	if err != nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, err)
	}
	if err == nil && completed != nil && status == string(domain.SemanticReviewTerminalFailure) {
		observability.RecordAssessorTerminalFailure(s.metrics, stage)
	}
	if err == nil && completed != nil {
		event, destination, reasonCode := "submission_failed", "failed", string(submissionFailureCode(stage, failureClass))
		if status == string(domain.SemanticReviewSuperseded) {
			event, destination, reasonCode = "submission_superseded", "superseded", strings.TrimSpace(stage)
		}
		s.logLifecycle(scope, event, destination, stage, reasonCode, nil)
	}
	return err
}

func (s *submissionAssessmentPlacementWorkerService) completeDeterministicSecurityQuarantine(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	plan submissionAssessmentPlan,
	scan SubmissionSecurityBatchScan,
	stage string,
) error {
	quarantines, err := submissionAssessmentDeterministicQuarantines(plan, scan)
	if err != nil {
		return newPlacementWorkerDiagnosticError(scope.TeamID, scope.IngestID, placementFailureDiagnosticFor(stage, err), err)
	}
	payload := map[string]any{"assessor_contract": domain.ContractVersion}
	completed, err := s.assessments.CompleteSubmissionAssessment(ctx, repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_security",
		Status:                       string(domain.SemanticReviewQuarantined),
		Category:                     "quarantined",
		Payload:                      payload,
		SecurityQuarantines:          quarantines,
	})
	if err == nil && completed == nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, errors.New("submission assessment worker: nil security terminal result"))
	}
	if err != nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, err)
	}
	if err == nil {
		s.logLifecycle(scope, "submission_quarantined", "quarantined", stage, string(SubmissionErrorQuarantined), nil)
	}
	return err
}

func (s *submissionAssessmentPlacementWorkerService) completeProviderSecurityQuarantine(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	plan submissionAssessmentPlan,
	response verifier.SemanticAssessmentResponse,
	stage string,
) error {
	type group struct {
		signals []repository.SecuritySignalInput
	}
	byFragmentID := map[string]*group{}
	for _, signal := range response.SecuritySignals {
		item, ok := plan.itemsByEvidenceID[signal.EvidenceID]
		if !ok {
			return errors.New("submission assessor security signal references unknown evidence")
		}
		quote, err := verifier.SemanticEvidenceSpan(item.Fragment.Content, signal.Start, signal.End)
		if err != nil {
			return err
		}
		entry := byFragmentID[item.Fragment.FragmentID]
		if entry == nil {
			entry = &group{}
			byFragmentID[item.Fragment.FragmentID] = entry
		}
		entry.signals = append(entry.signals, repository.SecuritySignalInput{
			Kind:      signal.Kind,
			Severity:  "high",
			SpanStart: signal.Start,
			SpanEnd:   signal.End,
			Quote:     quote,
		})
	}
	quarantines := make([]repository.SubmissionAssessmentSecurityQuarantineInput, 0, len(byFragmentID))
	for _, item := range plan.Items {
		entry := byFragmentID[item.Fragment.FragmentID]
		if entry == nil {
			continue
		}
		quarantines = append(quarantines, repository.SubmissionAssessmentSecurityQuarantineInput{
			FragmentID: item.Fragment.FragmentID,
			SecurityEventDraft: repository.SecurityEventDraft{
				EventKind: "verifier_signal",
				Decision:  "quarantine",
				Reason:    "semantic assessor reported security signal",
				Signals:   entry.signals,
			},
		})
	}
	if len(quarantines) == 0 {
		err := errors.New("submission assessor security quarantine has no target")
		return newPlacementWorkerDiagnosticError(scope.TeamID, scope.IngestID, placementFailureDiagnosticFor(stage, err), err)
	}
	payload := map[string]any{"assessor_contract": domain.ContractVersion}
	completed, err := s.assessments.CompleteSubmissionAssessment(ctx, repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_security",
		Status:                       string(domain.SemanticReviewQuarantined),
		Category:                     "quarantined",
		Payload:                      payload,
		SecurityQuarantines:          quarantines,
	})
	if err == nil && completed == nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, errors.New("submission assessment worker: nil provider security terminal result"))
	}
	if err != nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, err)
	}
	if err == nil {
		s.logLifecycle(scope, "submission_quarantined", "quarantined", stage, string(SubmissionErrorQuarantined), nil)
	}
	return err
}

func submissionAssessmentCommitInput(
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	plan submissionAssessmentPlan,
	request verifier.SemanticAssessmentRequest,
	response verifier.SemanticAssessmentResponse,
	assessment *repository.SubmissionAssessment,
	policy repository.AutoWriteConfidencePolicy,
	reused bool,
) (repository.CommitSubmissionAssessmentInput, error) {
	if assessment == nil || strings.TrimSpace(assessment.AssessmentID) == "" {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("persisted submission assessment is required before semantic commit")
	}
	if policy.Threshold < 0 || policy.Threshold > 1 {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessment confidence threshold is invalid")
	}
	policyVersion := assessmentPolicyVersion(policy)
	threshold := policy.Threshold
	if issues, truncated := submissionAssessmentReviewIssues(plan, response, threshold); len(issues) > 0 {
		return repository.CommitSubmissionAssessmentInput{}, &submissionAssessmentReviewRequiredError{Issues: issues, Truncated: truncated}
	}
	items := make([]repository.SubmissionAssessmentItemInput, 0, len(plan.Items))
	for _, item := range plan.Items {
		items = append(items, repository.SubmissionAssessmentItemInput{
			PlacementItemID: item.PlacementItem.PlacementItemID,
			FragmentID:      item.Fragment.FragmentID,
		})
	}
	entityGroups := assessmentGroupsBySpan(request.EntityCandidateGroups)
	entityResolutions := make([]repository.SubmissionAssessmentEntityResolutionInput, 0, len(response.EntityResults))
	entityKinds := make(map[string]string, len(response.EntityResults))
	entityResolutionsByGrounding := make(map[string]struct {
		action        string
		candidateID   string
		knownEntityID string
		mentionRef    string
	}, len(response.EntityResults))
	entityRefAliases := make(map[string]string, len(response.EntityResults))
	for _, result := range response.EntityResults {
		target, ok := plan.entityTargetsByRef[result.Ref]
		if !ok {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor entity result is outside the contract")
		}
		if result.Action == string(domain.EntityResolutionAmbiguous) {
			return repository.CommitSubmissionAssessmentInput{}, errSubmissionAssessmentRequiresReview
		}
		item, ok := plan.itemsByEvidenceID[result.EvidenceID]
		if !ok {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor entity grounding is outside the run")
		}
		resolution := repository.PlacementEntityResolutionInput{
			MentionRef:    result.Ref,
			Action:        result.Action,
			EntityKind:    result.Kind,
			CanonicalName: target.Target.Name,
			FragmentID:    item.Fragment.FragmentID,
			VerifierResult: map[string]any{
				"confidence": result.Confidence,
				"rationale":  result.Rationale,
				"action":     result.Action,
			},
			Metadata: map[string]any{
				"semantic_contract": domain.ContractVersion,
			},
			AssessmentID: assessment.AssessmentID,
		}
		start, end := result.Start, result.End
		resolution.SpanStart = &start
		resolution.SpanEnd = &end
		switch result.Action {
		case string(domain.EntityResolutionReuse):
			if result.CandidateEntityID == nil {
				return repository.CommitSubmissionAssessmentInput{}, errSubmissionAssessmentRequiresReview
			}
			resolution.EntityID = *result.CandidateEntityID
		case string(domain.EntityResolutionCreate):
			group := entityGroups[assessmentCandidateGroupKey(result.EvidenceID, result.Start, result.End)]
			if group != nil && (group.CandidateContextTruncated || assessmentCompatibleCandidateExists(group, result.Kind)) {
				return repository.CommitSubmissionAssessmentInput{}, errSubmissionAssessmentRequiresReview
			}
			resolution.IdentityContext = map[string]any{
				"surface": result.Surface,
				"source":  "submission_assessment",
			}
		default:
			return repository.CommitSubmissionAssessmentInput{}, errSubmissionAssessmentRequiresReview
		}
		entityKinds[result.Ref] = result.Kind
		groundingKey := fmt.Sprintf("%s:%d:%d:%s", result.EvidenceID, result.Start, result.End, result.Kind)
		candidateID := resolution.EntityID
		if previous, exists := entityResolutionsByGrounding[groundingKey]; exists {
			if previous.action != resolution.Action || previous.candidateID != candidateID ||
				(previous.knownEntityID != "" && target.KnownEntityID != "" && previous.knownEntityID != target.KnownEntityID) {
				return repository.CommitSubmissionAssessmentInput{}, errSubmissionAssessmentRequiresReview
			}
			entityRefAliases[result.Ref] = previous.mentionRef
			continue
		}
		entityResolutionsByGrounding[groundingKey] = struct {
			action        string
			candidateID   string
			knownEntityID string
			mentionRef    string
		}{action: resolution.Action, candidateID: candidateID, knownEntityID: target.KnownEntityID, mentionRef: resolution.MentionRef}
		entityRefAliases[result.Ref] = resolution.MentionRef
		entityResolutions = append(entityResolutions, repository.SubmissionAssessmentEntityResolutionInput{
			PlacementItemID: item.PlacementItem.PlacementItemID,
			Resolution:      resolution,
		})
	}
	if len(entityKinds) != len(plan.EntityTargets) {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor omitted an entity result")
	}

	observations := make([]repository.SubmissionAssessmentRelationshipObservationInput, 0, len(response.RelationshipResults))
	registrations := make([]repository.SubmissionPredicateRegistrationInput, 0)
	for _, result := range response.RelationshipResults {
		target, ok := plan.relationshipsByRef[result.Ref]
		if !ok {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor relationship result is outside the contract")
		}
		if result.PredicateStatus == "needs_review" || result.Modality != "statement" ||
			result.ScopeStatus == "needs_review" || result.TemporalVerdict == "ambiguous" ||
			result.TemporalVerdict == "contradicted" || result.EvidenceVerdict != string(domain.VerificationEntailed) ||
			result.Confidence < threshold {
			return repository.CommitSubmissionAssessmentInput{}, errSubmissionAssessmentRequiresReview
		}
		if result.PredicateStatus != "resolved" && result.PredicateStatus != "registration_required" {
			return repository.CommitSubmissionAssessmentInput{}, errSubmissionAssessmentRequiresReview
		}
		validFrom, validTo, err := semanticAssessmentValidity(result)
		if err != nil {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor validity is invalid")
		}
		supports, err := submissionAssessmentSupports(plan, assessment.AssessmentID, result.Evidence)
		if err != nil {
			return repository.CommitSubmissionAssessmentInput{}, err
		}
		primarySupport, additionalSupports := semanticAssessmentPrimarySupport(supports)
		if primarySupport == nil {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor relationship has no support")
		}
		owner, ok := submissionAssessmentItemForFragment(plan, primarySupport.FragmentID)
		if !ok {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor support is outside the run")
		}
		objectRef, objectValue, err := semanticAssessmentObject(result)
		if err != nil {
			return repository.CommitSubmissionAssessmentInput{}, err
		}
		if entityKinds[result.SubjectRef] == "" || (objectRef != "" && entityKinds[objectRef] == "") {
			return repository.CommitSubmissionAssessmentInput{}, errSubmissionAssessmentRequiresReview
		}
		subjectRef := result.SubjectRef
		if canonicalRef := entityRefAliases[subjectRef]; canonicalRef != "" {
			subjectRef = canonicalRef
		}
		if canonicalRef := entityRefAliases[objectRef]; canonicalRef != "" {
			objectRef = canonicalRef
		}
		confidence := result.Confidence
		observation := repository.PlacementRelationshipDecisionInput{
			Ref:               result.Ref,
			SubjectRef:        subjectRef,
			OriginalPredicate: result.OriginalPredicate,
			ObjectRef:         objectRef,
			ObjectValue:       objectValue,
			Polarity:          result.Polarity,
			ScopeKey:          semanticAssessmentScopeKey(result),
			ValidFrom:         validFrom,
			ValidTo:           validTo,
			EvidenceVerdict:   result.EvidenceVerdict,
			Confidence:        &confidence,
			Rationale:         result.Rationale,
			Model:             assessment.Model,
			ResponseHash:      assessment.ResponseHash,
			Support:           primarySupport,
			Supports:          additionalSupports,
			ObservationMetadata: map[string]any{
				"semantic_contract": domain.ContractVersion,
				"assessment_id":     assessment.AssessmentID,
				"modality":          result.Modality,
				"scope_status":      result.ScopeStatus,
				"temporal_verdict":  result.TemporalVerdict,
			},
			RelationshipMetadata: map[string]any{
				"assessment_response_hash": assessment.ResponseHash,
			},
			AssessmentID:            assessment.AssessmentID,
			AssessmentPolicyVersion: policyVersion,
			ThresholdUsed:           &threshold,
			GateResult:              "meets_write_threshold",
		}
		if target.CorrectionTarget != nil {
			copy := *target.CorrectionTarget
			observation.CorrectionTarget = &copy
		}
		if target.ConflictContext != nil {
			copy := *target.ConflictContext
			observation.ConflictContext = &copy
		}
		switch result.PredicateStatus {
		case "resolved":
			if result.PredicateKey == nil || result.PredicateVersion == nil {
				return repository.CommitSubmissionAssessmentInput{}, errSubmissionAssessmentRequiresReview
			}
			observation.PredicateKey = *result.PredicateKey
			observation.PredicateVersion = *result.PredicateVersion
		case "registration_required":
			registrations = append(registrations, repository.SubmissionPredicateRegistrationInput{
				RelationshipRef: result.Ref,
				PredicateKey:    target.ProposedPredicate,
				SubjectKind:     target.SubjectKind,
				ObjectKind:      target.ObjectKind,
			})
		}
		observations = append(observations, repository.SubmissionAssessmentRelationshipObservationInput{
			PlacementItemID: owner.PlacementItem.PlacementItemID,
			Observation:     observation,
		})
	}
	if len(observations) != len(plan.RelationshipTargets) {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor omitted a relationship result")
	}
	return repository.CommitSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		AssessmentID:                 assessment.AssessmentID,
		Items:                        items,
		EntityResolutions:            entityResolutions,
		RelationshipObservations:     observations,
		PredicateRegistrations:       registrations,
		Payload: map[string]any{
			"assessor_contract": domain.ContractVersion,
			"assessment_id":     assessment.AssessmentID,
			"response_hash":     assessment.ResponseHash,
			"policy_version":    policyVersion,
			"request_id":        assessment.RequestID,
			"assessment_reused": reused,
		},
	}, nil
}

func recordSubmissionAssessmentGateBands(metrics observability.DiscoverabilityMetrics, input repository.CommitSubmissionAssessmentInput) {
	for _, observation := range input.RelationshipObservations {
		observability.RecordAssessorConfidenceGate(metrics, observation.Observation.GateResult)
	}
}
