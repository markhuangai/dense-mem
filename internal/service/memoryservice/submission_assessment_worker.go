package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

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
	Ledger      repository.LedgerRepository
	Assessments repository.SubmissionAssessmentRepository
	Catalog     SubmissionAssessmentCatalog
	Provider    verifier.RememberAssessor
	Limits      verifier.SemanticAssessmentLimits
	TeamID      string
	WorkerID    string
	Lease       time.Duration
	Now         func() time.Time
	Metrics     observability.DiscoverabilityMetrics
	Logger      observability.LogProvider
}

type submissionAssessmentPlacementWorkerService struct {
	ledger      repository.LedgerRepository
	assessments repository.SubmissionAssessmentRepository
	catalog     SubmissionAssessmentCatalog
	provider    verifier.RememberAssessor
	limits      verifier.SemanticAssessmentLimits
	teamID      string
	workerID    string
	lease       time.Duration
	now         func() time.Time
	metrics     observability.DiscoverabilityMetrics
	logger      observability.LogProvider
}

type submissionAssessmentLiveSession struct {
	session    verifier.SemanticAssessmentSession
	request    verifier.SemanticAssessmentRequest
	turnOffset int
}

var errRememberAssessorTurnBudgetExhausted = errors.New("remember assessor turn budget exhausted")

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
	return &submissionAssessmentPlacementWorkerService{
		ledger:      deps.Ledger,
		assessments: deps.Assessments,
		catalog:     deps.Catalog,
		provider:    deps.Provider,
		limits:      deps.Limits,
		teamID:      strings.TrimSpace(deps.TeamID),
		workerID:    strings.TrimSpace(deps.WorkerID),
		lease:       lease,
		now:         now,
		metrics:     metrics,
		logger:      deps.Logger,
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
	ctx = withPlacementRunSpace(ctx, *run)
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

	refreshRequest := func(refreshCtx context.Context) (verifier.SemanticAssessmentRequest, error) {
		return s.buildRequest(refreshCtx, *run, plan, placement.Proposal)
	}
	assessment, response, reused, providerAttempted, releaseProviderAttempt, liveSession, err := s.loadOrAssess(
		ctx,
		*run,
		scope,
		request,
		refreshRequest,
	)
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
	for {
		if len(response.SecuritySignals) > 0 {
			return true, s.completeProviderSecurityQuarantine(ctx, scope, plan, response, "security_signal")
		}

		commitInput, commitInputErr := submissionAssessmentCommitInput(*run, scope, plan, response, assessment, reused)
		var noSupported *submissionAssessmentNoSupportedMemoryError
		if errors.As(commitInputErr, &noSupported) {
			return true, s.completeRejected(ctx, scope, SubmissionErrorNoSupportedMemory, noSupported.RelationshipResults)
		}
		if commitInputErr != nil {
			return true, terminalizeAfterError(commitInputErr, func() error {
				return s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "deterministic_policy")
			})
		}
		committed, commitErr := s.assessments.CommitSubmissionAssessment(ctx, commitInput)
		if isRememberStaleInputError(commitErr) {
			return true, s.completeRejected(ctx, scope, SubmissionErrorStaleInput, nil)
		}
		if errors.Is(commitErr, repository.ErrSubmissionAssessmentScopeMismatch) {
			return true, terminalizeAfterError(commitErr, func() error {
				return s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_scope")
			})
		}
		if isRepairableRememberCommitRace(commitErr) {
			response, liveSession, err = s.repairRememberCommitRace(
				ctx, *run, assessment.ProviderTurns, liveSession, refreshRequest, commitErr,
			)
			if err != nil {
				if errors.Is(err, errRememberAssessorTurnBudgetExhausted) {
					return true, terminalizeAfterError(err, func() error {
						return s.completeTerminal(
							ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "commit_race_exhausted", err,
						)
					})
				}
				if errors.Is(err, verifier.ErrVerifierMalformedResponse) {
					failureClass, providerTurns := semanticAssessmentMalformedFailure(err)
					return true, terminalizeAfterError(err, func() error {
						return s.completeTerminalWithFailure(ctx, scope, "assessment", failureClass, 0, providerTurns, err)
					})
				}
				return true, retryAfterError(err, func() error {
					return s.retryProviderFailure(ctx, *run, scope, "assessment", true, verifier.ProviderFailureDetails(err))
				})
			}
			assessment, err = s.persistSubmissionAssessmentRevision(ctx, *run, scope, assessment, response, liveSession.request)
			if err != nil {
				return true, retryAfterError(err, func() error {
					return s.retryOrFail(ctx, *run, scope, "assessment_persist", false, false, err)
				})
			}
			reused = false
			continue
		}
		if commitErr != nil {
			return true, retryAfterError(commitErr, func() error {
				return s.retryOrFail(ctx, *run, scope, "semantic_commit", false, false, commitErr)
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
}

func withPlacementRunSpace(ctx context.Context, run repository.PlacementRun) context.Context {
	spaceID, err := uuid.Parse(strings.TrimSpace(run.SpaceID))
	if err != nil {
		return ctx
	}
	return requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{{ID: spaceID}})
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
	return nil
}

func isRepairableRememberCommitRace(err error) bool {
	return errors.Is(err, repository.ErrSubmissionAssessmentNonPromotable) ||
		errors.Is(err, repository.ErrSubmissionPredicateRegistrationHeld)
}

func (s *submissionAssessmentPlacementWorkerService) repairRememberCommitRace(
	ctx context.Context,
	run repository.PlacementRun,
	providerTurns int,
	live *submissionAssessmentLiveSession,
	refresh func(context.Context) (verifier.SemanticAssessmentRequest, error),
	cause error,
) (verifier.SemanticAssessmentResponse, *submissionAssessmentLiveSession, error) {
	if providerTurns >= SemanticPlacementMaxAssessorTurns {
		return verifier.SemanticAssessmentResponse{}, live, errRememberAssessorTurnBudgetExhausted
	}
	providerCtx := observability.WithMetricIdentity(ctx, run.TeamID, run.OwnerProfileID)
	providerCtx = observability.WithAIOperation(providerCtx, observability.AIOperationPlacementAssessment, 1)
	if live == nil || live.session == nil {
		request, err := refresh(providerCtx)
		if err != nil {
			return verifier.SemanticAssessmentResponse{}, nil, err
		}
		response, session, finalRequest, err := s.assessRememberSession(providerCtx, request, refresh, providerTurns)
		if err != nil {
			return verifier.SemanticAssessmentResponse{}, nil, err
		}
		return response, &submissionAssessmentLiveSession{
			session: session, request: finalRequest, turnOffset: providerTurns,
		}, nil
	}
	nextRequest, err := refresh(providerCtx)
	if err != nil {
		return verifier.SemanticAssessmentResponse{}, live, err
	}
	turn, err := s.provider.Repair(providerCtx, live.session, verifier.SemanticAssessmentRepairRequest{
		Request: nextRequest,
		ValidationErrors: []verifier.SemanticValidationError{{
			Field:   "server_state",
			Message: rememberCommitRaceRepairMessage(cause),
		}},
	})
	if err != nil {
		return verifier.SemanticAssessmentResponse{}, live, err
	}
	response, finalRequest, err := s.completeRememberSessionTurns(
		providerCtx, live.session, turn, nextRequest, refresh, live.turnOffset,
	)
	live.request = finalRequest
	return response, live, err
}

func rememberCommitRaceRepairMessage(err error) string {
	if errors.Is(err, repository.ErrSubmissionPredicateRegistrationHeld) {
		return "predicate state changed; use the refreshed compatible predicate options or return not_supported"
	}
	return "server-owned semantic state changed; reconcile the complete response against refreshed candidates"
}

func (s *submissionAssessmentPlacementWorkerService) persistSubmissionAssessmentRevision(
	ctx context.Context,
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	assessment *repository.SubmissionAssessment,
	response verifier.SemanticAssessmentResponse,
	request verifier.SemanticAssessmentRequest,
) (*repository.SubmissionAssessment, error) {
	if assessment == nil {
		return nil, errors.New("submission assessment revision requires a persisted assessment")
	}
	normalizedJSON, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	canonicalJSON, err := verifier.CanonicalJSON(normalizedJSON)
	if err != nil {
		return nil, err
	}
	inputTokens := response.InputTokens
	if inputTokens <= 0 {
		inputTokens = request.InputTokens
	}
	persisted, existing, err := s.assessments.AppendSubmissionAssessmentRevision(ctx, repository.AppendSubmissionAssessmentRevisionInput{
		SubmissionAssessmentRunScope: scope,
		AssessmentID:                 assessment.AssessmentID,
		ProviderTurns:                response.ProviderTurns,
		InputTokens:                  inputTokens,
		OutputTokens:                 response.OutputTokens,
		CandidateContextTokens:       request.CandidateContextTokens,
		CandidateContextTruncated:    request.CandidateContextTruncated,
		NormalizedResponse:           canonicalJSON,
		ResponseHash:                 semanticAssessmentHash(canonicalJSON),
		ValidatedAt:                  s.now().UTC(),
	})
	if err != nil {
		observability.RecordAssessorAssessmentPersistence(s.metrics, "error")
		return nil, err
	}
	outcome := "persisted"
	if existing {
		outcome = "reused"
	}
	observability.RecordAssessorAssessmentPersistence(s.metrics, outcome)
	return persisted, nil
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
	var payload map[string]any
	if status == string(domain.SemanticReviewSuperseded) {
		payload = map[string]any{"assessor_contract": domain.ContractVersion}
	} else {
		payload = semanticAssessmentFailurePayload(stage, false, firstError(failureCause))
		payload["assessor_contract"] = domain.ContractVersion
	}
	failureClass, _ := payload["failure_class"].(string)
	failureCode := submissionFailureCode(stage, failureClass)
	if status != string(domain.SemanticReviewSuperseded) {
		payload["failure_code"] = string(failureCode)
	}
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
		event, destination, reasonCode := "submission_failed", "failed", string(failureCode)
		if failureClass != "" {
			reasonCode = string(submissionFailureCode(stage, failureClass))
		}
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
	response verifier.SemanticAssessmentResponse,
	assessment *repository.SubmissionAssessment,
	reused bool,
) (repository.CommitSubmissionAssessmentInput, error) {
	if assessment == nil || strings.TrimSpace(assessment.AssessmentID) == "" {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("persisted submission assessment is required before semantic commit")
	}
	unsupportedEntities := repairSubmissionAssessmentResponse(&plan, &response)
	items := make([]repository.SubmissionAssessmentItemInput, 0, len(plan.Items))
	for _, item := range plan.Items {
		items = append(items, repository.SubmissionAssessmentItemInput{
			PlacementItemID: item.PlacementItem.PlacementItemID,
			FragmentID:      item.Fragment.FragmentID,
		})
	}
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
		if _, unsupported := unsupportedEntities[result.Ref]; unsupported {
			continue
		}
		if target.KnownEntityID != "" && result.Action == string(domain.EntityResolutionCreate) {
			return repository.CommitSubmissionAssessmentInput{}, fmt.Errorf("%w: assessor changed exact entity constraint for %s", errSubmissionAssessmentStaleInput, result.Ref)
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
				"action":   result.Action,
				"decision": "server_accepted_grounding",
			},
			Metadata: map[string]any{
				"semantic_contract": domain.ContractVersion,
			},
			AssessmentID: assessment.AssessmentID,
		}
		if target.KnownEntityID != "" {
			resolution.ExactEntityID = target.KnownEntityID
		}
		start, end := result.Start, result.End
		resolution.SpanStart = &start
		resolution.SpanEnd = &end
		switch result.Action {
		case string(domain.EntityResolutionReuse):
			if result.CandidateEntityID == nil {
				if target.KnownEntityID == "" {
					result.Action = string(domain.EntityResolutionCreate)
					resolution.Action = result.Action
					resolution.IdentityContext = map[string]any{
						"surface": result.Surface,
						"source":  "submission_assessment",
					}
				} else {
					candidate := target.KnownEntityID
					result.CandidateEntityID = &candidate
				}
			}
			if result.Action == string(domain.EntityResolutionReuse) {
				if target.KnownEntityID != "" && *result.CandidateEntityID != target.KnownEntityID {
					return repository.CommitSubmissionAssessmentInput{}, fmt.Errorf("%w: assessor changed exact entity constraint for %s", errSubmissionAssessmentStaleInput, result.Ref)
				}
				resolution.EntityID = *result.CandidateEntityID
			}
		case string(domain.EntityResolutionCreate):
			resolution.IdentityContext = map[string]any{
				"surface": result.Surface,
				"source":  "submission_assessment",
			}
		default:
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor returned unsupported entity action")
		}
		entityKinds[result.Ref] = result.Kind
		groundingKey := fmt.Sprintf("%s:%d:%d:%s", result.EvidenceID, result.Start, result.End, result.Kind)
		candidateID := resolution.EntityID
		if previous, exists := entityResolutionsByGrounding[groundingKey]; exists {
			if previous.action != resolution.Action || previous.candidateID != candidateID ||
				(previous.knownEntityID != "" && target.KnownEntityID != "" && previous.knownEntityID != target.KnownEntityID) {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor returned conflicting entity groundings")
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
	if len(entityKinds)+len(unsupportedEntities) != len(plan.EntityTargets) {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor omitted an entity result")
	}

	observations := make([]repository.SubmissionAssessmentRelationshipObservationInput, 0, len(response.RelationshipResults))
	registrations := make([]repository.SubmissionPredicateRegistrationInput, 0)
	relationshipResults := make([]repository.SubmissionRelationshipResultInput, 0, len(response.RelationshipResults))
	seenRelationshipRefs := make(map[string]struct{}, len(response.RelationshipResults))
	for _, result := range response.RelationshipResults {
		target, ok := plan.relationshipsByRef[result.Ref]
		if !ok {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor relationship result is outside the contract")
		}
		if _, duplicate := seenRelationshipRefs[result.Ref]; duplicate {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor returned duplicate relationship result")
		}
		seenRelationshipRefs[result.Ref] = struct{}{}
		switch result.Disposition {
		case "not_supported":
			reason := "not_supported_by_evidence"
			if result.Reason != nil && strings.TrimSpace(*result.Reason) != "" {
				reason = strings.TrimSpace(*result.Reason)
			}
			relationshipResults = append(relationshipResults, repository.SubmissionRelationshipResultInput{
				RelationshipRef: result.Ref,
				Disposition:     "not_stored",
				Reason:          reason,
			})
			continue
		case "stored":
		default:
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor returned unsupported relationship disposition")
		}
		if len(result.Splits) == 0 {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor stored relationship has no split")
		}
		if len(result.Splits) > 1 && (target.CorrectionTarget != nil || target.ConflictContext != nil) {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor split an exact lifecycle operation")
		}
		for _, split := range result.Splits {
			if unsupportedEntityResult(split, unsupportedEntities) {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor stored split references an ungrounded Entity")
			}
			if split.PredicateStatus != "resolved" && split.PredicateStatus != "registration_required" {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor returned unsupported predicate status")
			}
			validFrom, validTo, err := semanticAssessmentValidity(split)
			if err != nil {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor validity is invalid")
			}
			supports, err := submissionAssessmentSupports(plan, assessment.AssessmentID, split.Evidence)
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
			observationRef := submissionAssessmentObservationRef(result.Ref, split.SplitIndex, len(result.Splits))
			objectRef, objectValue, err := semanticAssessmentObject(observationRef, split)
			if err != nil {
				return repository.CommitSubmissionAssessmentInput{}, err
			}
			if entityKinds[split.SubjectRef] == "" || (objectRef != "" && entityKinds[objectRef] == "") {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor stored split references an ungrounded Entity")
			}
			subjectRef := split.SubjectRef
			if canonicalRef := entityRefAliases[subjectRef]; canonicalRef != "" {
				subjectRef = canonicalRef
			}
			if canonicalRef := entityRefAliases[objectRef]; canonicalRef != "" {
				objectRef = canonicalRef
			}
			observation := repository.PlacementRelationshipDecisionInput{
				Ref: observationRef, SubjectRef: subjectRef, OriginalPredicate: split.OriginalPredicate,
				ObjectRef: objectRef, ObjectValue: objectValue, Polarity: split.Polarity,
				ScopeKey: "", ValidFrom: validFrom, ValidTo: validTo, AssessorAccepted: true,
				Model: assessment.Model, ResponseHash: assessment.ResponseHash,
				Support: primarySupport, Supports: additionalSupports,
				ObservationMetadata: map[string]any{
					"semantic_contract": domain.ContractVersion, "assessment_id": assessment.AssessmentID,
					"support_policy": "server_accepted_grounded_response", "relationship_ref": result.Ref,
					"split_index": split.SplitIndex,
				},
				RelationshipMetadata: map[string]any{
					"assessment_response_hash":   assessment.ResponseHash,
					"submitted_relationship_ref": result.Ref, "split_index": split.SplitIndex,
				},
				AssessmentID: assessment.AssessmentID,
			}
			if target.KnownPredicateKey != "" {
				observation.ExactPredicateKey = target.KnownPredicateKey
			}
			if target.CorrectionTarget != nil {
				copy := *target.CorrectionTarget
				observation.CorrectionTarget = &copy
			}
			if target.ConflictContext != nil {
				copy := *target.ConflictContext
				observation.ConflictContext = &copy
			}
			switch split.PredicateStatus {
			case "resolved":
				if split.PredicateKey == nil || split.PredicateVersion == nil {
					return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor resolved predicate is incomplete")
				}
				if target.KnownPredicateKey != "" && *split.PredicateKey != target.KnownPredicateKey {
					return repository.CommitSubmissionAssessmentInput{}, fmt.Errorf("%w: assessor changed exact predicate constraint for %s", errSubmissionAssessmentStaleInput, result.Ref)
				}
				observation.PredicateKey = *split.PredicateKey
				observation.PredicateVersion = *split.PredicateVersion
			case "registration_required":
				if target.KnownPredicateKey != "" {
					return repository.CommitSubmissionAssessmentInput{}, fmt.Errorf("%w: assessor could not preserve exact predicate constraint for %s", errSubmissionAssessmentStaleInput, result.Ref)
				}
				if split.PredicateRegistration == nil {
					return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor predicate registration is incomplete")
				}
				registrations = append(registrations, repository.SubmissionPredicateRegistrationInput{
					RelationshipRef: observationRef, PredicateKey: split.PredicateRegistration.PredicateKey,
					SubjectKind: entityKinds[split.SubjectRef], ObjectKind: relationshipObjectKind(split, entityKinds, target.ObjectKind),
					RelationshipKind:   split.PredicateRegistration.RelationshipKind,
					CurrentCardinality: split.PredicateRegistration.CurrentCardinality,
				})
			}
			observations = append(observations, repository.SubmissionAssessmentRelationshipObservationInput{
				PlacementItemID: owner.PlacementItem.PlacementItemID,
				RelationshipRef: result.Ref,
				SplitIndex:      split.SplitIndex,
				Observation:     observation,
			})
		}
		relationshipResults = append(relationshipResults, repository.SubmissionRelationshipResultInput{
			RelationshipRef: result.Ref,
			Disposition:     "stored",
		})
	}
	if len(seenRelationshipRefs) != len(plan.RelationshipTargets) {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor omitted a relationship result")
	}
	coveredEvidence := make(map[string]struct{})
	for _, observation := range observations {
		if observation.Observation.Support != nil {
			coveredEvidence[observation.Observation.Support.FragmentID] = struct{}{}
		}
		for _, support := range observation.Observation.Supports {
			coveredEvidence[support.FragmentID] = struct{}{}
		}
	}
	if len(observations) == 0 {
		return repository.CommitSubmissionAssessmentInput{}, &submissionAssessmentNoSupportedMemoryError{
			RelationshipResults: relationshipResults,
		}
	}
	if len(coveredEvidence) < len(plan.Items) {
		return repository.CommitSubmissionAssessmentInput{}, &submissionAssessmentNoSupportedMemoryError{}
	}
	return repository.CommitSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		AssessmentID:                 assessment.AssessmentID,
		Items:                        items,
		EntityResolutions:            entityResolutions,
		RelationshipObservations:     observations,
		PredicateRegistrations:       registrations,
		RelationshipResults:          relationshipResults,
		Payload: map[string]any{
			"assessor_contract": domain.ContractVersion,
			"assessment_id":     assessment.AssessmentID,
			"response_hash":     assessment.ResponseHash,
			"request_id":        assessment.RequestID,
			"assessment_reused": reused,
		},
	}, nil
}
