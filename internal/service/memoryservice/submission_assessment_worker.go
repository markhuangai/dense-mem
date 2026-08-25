package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
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
	ProcessSubmissionAssessmentPlacement(ctx context.Context, submissionID string) (bool, error)
}

type SubmissionAssessmentPlacementWorkerDependencies struct {
	Ledger         repository.LedgerRepository
	Assessments    repository.SubmissionAssessmentRepository
	Catalog        SubmissionAssessmentCatalog
	Provider       assessor.Provider
	Limits         assessor.SemanticAssessmentLimits
	TeamID         string
	WorkerID       string
	Lease          time.Duration
	OwnerProfileID string
	Now            func() time.Time
	Metrics        observability.DiscoverabilityMetrics
	Logger         observability.LogProvider
	InlineEmbedder repository.InlineEmbeddingBatch
}

type submissionAssessmentPlacementWorkerService struct {
	ledger         repository.LedgerRepository
	assessments    repository.SubmissionAssessmentRepository
	catalog        SubmissionAssessmentCatalog
	provider       assessor.Provider
	limits         assessor.SemanticAssessmentLimits
	teamID         string
	workerID       string
	lease          time.Duration
	ownerProfileID string
	now            func() time.Time
	metrics        observability.DiscoverabilityMetrics
	logger         observability.LogProvider
	inlineEmbedder repository.InlineEmbeddingBatch
	targetID       string
	prepared       *SynchronousAssessmentResult
}

type submissionAssessmentLiveSession struct {
	session    assessor.SemanticAssessmentSession
	request    assessor.SemanticAssessmentRequest
	turnOffset int
}

var errRememberAssessorTurnBudgetExhausted = errors.New("remember assessor turn budget exhausted")

func NewSubmissionAssessmentPlacementWorkerService(
	deps SubmissionAssessmentPlacementWorkerDependencies,
) *submissionAssessmentPlacementWorkerService {
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
		ledger:         deps.Ledger,
		assessments:    deps.Assessments,
		catalog:        deps.Catalog,
		provider:       deps.Provider,
		limits:         deps.Limits,
		teamID:         strings.TrimSpace(deps.TeamID),
		workerID:       strings.TrimSpace(deps.WorkerID),
		lease:          lease,
		ownerProfileID: strings.TrimSpace(deps.OwnerProfileID),
		now:            now,
		metrics:        metrics,
		logger:         deps.Logger,
		inlineEmbedder: deps.InlineEmbedder,
	}
}

func (s *submissionAssessmentPlacementWorkerService) ProcessNextSubmissionAssessmentPlacement(ctx context.Context) (bool, error) {
	if err := s.validateDependencies(); err != nil {
		return false, err
	}
	var run *repository.PlacementRun
	var err error
	if s.targetID != "" {
		claimer, ok := s.ledger.(interface {
			ClaimPlacementRun(context.Context, repository.ClaimPlacementRunInput) (*repository.PlacementRun, error)
		})
		if !ok {
			return false, errors.New("submission assessment worker: targeted claim is unavailable")
		}
		run, err = claimer.ClaimPlacementRun(ctx, repository.ClaimPlacementRunInput{
			TeamID: s.teamID, OwnerProfileID: s.ownerProfileID, IngestID: s.targetID, WorkerID: s.workerID, Lease: s.lease,
		})
	} else {
		run, err = s.ledger.ClaimNextPlacementRun(ctx, s.teamID, s.workerID, s.lease)
	}
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

	var request assessor.SemanticAssessmentRequest
	if s.prepared != nil {
		request = s.prepared.Request
	} else {
		request, err = s.buildRequest(ctx, *run, plan, placement.Proposal)
		if err != nil {
			stage, terminal := semanticAssessmentPreflightFailure(err)
			if terminal {
				return true, terminalizeAfterError(err, func() error {
					results := submissionAssessmentNotStoredRelationshipResultsForPlan(plan, string(SubmissionErrorInternalFailure))
					return s.completeTerminalWithRelationshipResults(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", stage, results, err)
				})
			}
			return true, retryAfterError(err, func() error {
				results := submissionAssessmentNotStoredRelationshipResultsForPlan(plan, string(SubmissionErrorInternalFailure))
				return s.retryOrFailWithRelationshipResults(ctx, *run, scope, stage, false, false, results, err)
			})
		}
	}

	refreshRequest := func(refreshCtx context.Context) (assessor.SemanticAssessmentRequest, error) {
		return s.buildRequest(refreshCtx, *run, plan, placement.Proposal)
	}
	var assessment *repository.SubmissionAssessment
	var response assessor.SemanticAssessmentResponse
	var reused, providerAttempted, releaseProviderAttempt bool
	var liveSession *submissionAssessmentLiveSession
	if s.prepared != nil {
		response = s.prepared.Response
		request = s.prepared.Request
		providerAttempted = true
		assessment, err = s.persistPreparedAssessment(ctx, *run, request, response)
		if err != nil {
			return true, terminalizeAfterError(err, func() error {
				results := submissionAssessmentNotStoredRelationshipResultsForPlan(plan, string(SubmissionErrorInternalFailure))
				return s.completeTerminalWithRelationshipResults(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_persist", results, err)
			})
		}
	} else {
		assessment, response, reused, providerAttempted, releaseProviderAttempt, liveSession, err = s.loadOrAssess(
			ctx,
			*run,
			scope,
			request,
			refreshRequest,
		)
	}
	if err != nil {
		assessorTurnsReserved := submissionAssessmentConsumedProviderTurns(err)
		if errors.Is(err, errSubmissionAssessmentRevisionPersistence) {
			return true, terminalizeAfterError(err, func() error {
				results := submissionAssessmentNotStoredRelationshipResultsForPlan(plan, string(SubmissionErrorInternalFailure))
				return s.completeTerminalWithRelationshipResults(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_persist", results, err)
			})
		}
		if errors.Is(err, repository.ErrSubmissionAssessorAttemptConsumed) {
			return true, terminalizeAfterError(err, func() error {
				results := submissionAssessmentNotStoredRelationshipResultsForPlan(plan, string(SubmissionErrorInternalFailure))
				return s.completeTerminalWithRelationshipResults(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_attempt_consumed", results, err)
			})
		}
		if providerAttempted && errors.Is(err, assessor.ErrVerifierMalformedResponse) {
			failureClass, providerTurns := semanticAssessmentMalformedFailure(err)
			return true, terminalizeAfterError(err, func() error {
				results := submissionAssessmentNotStoredRelationshipResultsForPlan(plan, string(SubmissionErrorInternalFailure))
				return s.completeTerminalWithRelationshipResultsFailure(ctx, scope, "assessment", failureClass, 0, providerTurns, results, err)
			})
		}
		if providerAttempted {
			return true, retryAfterError(err, func() error {
				return s.retryProviderFailureWithTerminal(
					ctx, *run, scope, "assessment", releaseProviderAttempt, assessorTurnsReserved,
					assessor.ProviderFailureDetails(err),
				)
			})
		}
		return true, retryAfterError(err, func() error {
			results := submissionAssessmentNotStoredRelationshipResultsForPlan(plan, string(SubmissionErrorInternalFailure))
			return s.retryOrFailWithRelationshipResults(ctx, *run, scope, "assessment", providerAttempted, releaseProviderAttempt, results, err)
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
				results := submissionAssessmentNotStoredRelationshipResultsForPlan(plan, string(SubmissionErrorInternalFailure))
				return s.completeTerminalWithRelationshipResults(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "deterministic_policy", results, commitInputErr)
			})
		}
		commitCtx := ctx
		var cancelCommit context.CancelFunc
		if repository.InlineEmbeddingWrite(ctx) {
			commitCtx, cancelCommit = context.WithTimeout(ctx, 10*time.Second)
		}
		var committed *repository.CommitSubmissionAssessmentResult
		var commitErr error
		if s.inlineEmbedder != nil {
			inlineCommitter, ok := s.assessments.(repository.InlineSubmissionAssessmentCommitter)
			if !ok {
				commitErr = errors.New("submission assessment worker: inline semantic committer is unavailable")
			} else {
				committed, commitErr = inlineCommitter.CommitSubmissionAssessmentWithInlineEmbeddings(commitCtx, commitInput, s.inlineEmbedder)
			}
		} else {
			committed, commitErr = s.assessments.CommitSubmissionAssessment(commitCtx, commitInput)
		}
		if cancelCommit != nil {
			cancelCommit()
		}
		if isRememberStaleInputError(commitErr) {
			commitInput.RelationshipResults = submissionAssessmentNotStoredRelationshipResults(
				commitInput.RelationshipResults, string(SubmissionErrorStaleInput),
			)
			return true, s.completeRejected(ctx, scope, SubmissionErrorStaleInput, commitInput.RelationshipResults)
		}
		if errors.Is(commitErr, repository.ErrSubmissionAssessmentScopeMismatch) {
			return true, terminalizeAfterError(commitErr, func() error {
				results := submissionAssessmentNotStoredRelationshipResultsForPlan(plan, string(SubmissionErrorInternalFailure))
				return s.completeTerminalWithRelationshipResults(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_scope", results, commitErr)
			})
		}
		if isRepairableRememberCommitRace(commitErr) {
			response, liveSession, err = s.repairRememberCommitRace(
				ctx, *run, assessment.ProviderTurns, liveSession, refreshRequest, commitErr,
			)
			if err != nil {
				if consumedTurns := submissionAssessmentConsumedProviderTurns(err); consumedTurns > assessment.ProviderTurns {
					_, reserveErr := s.reserveSubmissionAssessmentProviderTurns(ctx, scope, assessment, consumedTurns)
					if reserveErr != nil {
						return true, terminalizeAfterError(reserveErr, func() error {
							results := submissionAssessmentNotStoredRelationshipResults(
								commitInput.RelationshipResults, string(SubmissionErrorInternalFailure),
							)
							return s.completeTerminalWithRelationshipResults(
								ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed",
								"assessment_persist", results, reserveErr,
							)
						})
					}
				}
				if errors.Is(err, errRememberAssessorTurnBudgetExhausted) {
					commitInput.RelationshipResults = submissionAssessmentNotStoredRelationshipResults(
						commitInput.RelationshipResults, string(SubmissionErrorInternalFailure),
					)
					return true, terminalizeAfterError(err, func() error {
						return s.completeTerminalWithRelationshipResults(
							ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "commit_race_exhausted",
							commitInput.RelationshipResults, err,
						)
					})
				}
				if errors.Is(err, assessor.ErrVerifierMalformedResponse) {
					failureClass, providerTurns := semanticAssessmentMalformedFailure(err)
					return true, terminalizeAfterError(err, func() error {
						results := submissionAssessmentNotStoredRelationshipResults(
							commitInput.RelationshipResults, string(SubmissionErrorInternalFailure),
						)
						return s.completeTerminalWithRelationshipResultsFailure(ctx, scope, "assessment", failureClass, 0, providerTurns, results, err)
					})
				}
				return true, retryAfterError(err, func() error {
					return s.retryProviderFailureWithTerminal(
						ctx, *run, scope, "assessment", true, 0, assessor.ProviderFailureDetails(err),
					)
				})
			}
			assessment, err = s.persistSubmissionAssessmentRevision(ctx, *run, scope, assessment, response, liveSession.request)
			if err != nil {
				if errors.Is(err, errSubmissionAssessmentRevisionPersistence) {
					return true, terminalizeAfterError(err, func() error {
						results := submissionAssessmentNotStoredRelationshipResults(
							commitInput.RelationshipResults, string(SubmissionErrorInternalFailure),
						)
						return s.completeTerminalWithRelationshipResults(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_persist", results, err)
					})
				}
				return true, retryAfterError(err, func() error {
					results := submissionAssessmentNotStoredRelationshipResults(
						commitInput.RelationshipResults, string(SubmissionErrorInternalFailure),
					)
					return s.retryOrFailWithRelationshipResults(ctx, *run, scope, "assessment_persist", false, false, results, err)
				})
			}
			reused = false
			continue
		}
		if commitErr != nil {
			// The synchronous path must surface the embedding/commit failure in
			// the originating call. Do not requeue or let a background worker
			// observe a partially prepared attempt; the caller terminalizes the
			// request after this transaction has rolled back.
			if s.inlineEmbedder != nil {
				return true, commitErr
			}
			return true, retryAfterError(commitErr, func() error {
				results := submissionAssessmentNotStoredRelationshipResults(
					commitInput.RelationshipResults, string(SubmissionErrorInternalFailure),
				)
				return s.retryOrFailWithRelationshipResults(ctx, *run, scope, "semantic_commit", false, false, results, commitErr)
			})
		}
		if committed == nil {
			cause := errors.New("submission assessment worker: nil semantic commit result")
			return true, retryAfterError(cause, func() error {
				results := submissionAssessmentNotStoredRelationshipResults(
					commitInput.RelationshipResults, string(SubmissionErrorInternalFailure),
				)
				return s.retryOrFailWithRelationshipResults(ctx, *run, scope, "semantic_commit", false, false, results, cause)
			})
		}
		s.logLifecycle(scope, "submission_completed", "completed", "semantic_commit", "semantic_commit_succeeded", nil)
		s.recordFirstDisposition(ctx, run.TeamID, run.OwnerProfileID, committed.FirstDisposition)
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
	refresh func(context.Context) (assessor.SemanticAssessmentRequest, error),
	cause error,
) (assessor.SemanticAssessmentResponse, *submissionAssessmentLiveSession, error) {
	if providerTurns >= SemanticPlacementMaxAssessorTurns {
		return assessor.SemanticAssessmentResponse{}, live, errRememberAssessorTurnBudgetExhausted
	}
	providerCtx := observability.WithMetricIdentity(ctx, run.TeamID, run.OwnerProfileID)
	providerCtx = observability.WithAIOperation(providerCtx, observability.AIOperationPlacementAssessment, 1)
	if live == nil || live.session == nil {
		request, err := refresh(providerCtx)
		if err != nil {
			return assessor.SemanticAssessmentResponse{}, nil, err
		}
		response, session, finalRequest, err := s.assessRememberSession(providerCtx, request, refresh, providerTurns)
		if err != nil {
			return assessor.SemanticAssessmentResponse{}, nil, err
		}
		return response, &submissionAssessmentLiveSession{
			session: session, request: finalRequest, turnOffset: providerTurns,
		}, nil
	}
	nextRequest, err := refresh(providerCtx)
	if err != nil {
		return assessor.SemanticAssessmentResponse{}, live, err
	}
	turn, err := s.provider.Repair(providerCtx, live.session, assessor.SemanticAssessmentRepairRequest{
		Request: nextRequest,
		ValidationErrors: []assessor.SemanticValidationError{{
			Field:   "server_state",
			Message: rememberCommitRaceRepairMessage(cause),
		}},
	})
	if err != nil {
		return assessor.SemanticAssessmentResponse{}, live, err
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

func (s *submissionAssessmentPlacementWorkerService) retryProviderFailure(
	ctx context.Context,
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	stage string,
	releaseProviderAttempt bool,
	assessorTurnsReserved int,
	failure assessor.ProviderFailureMetadata,
) error {
	if run.MaxAttempts > 0 && run.Attempts >= run.MaxAttempts {
		return s.completeTerminalWithFailure(ctx, scope, stage, failure.Class, failure.StatusCode, assessorTurnsReserved)
	}
	requeued, err := s.assessments.RequeueSubmissionAssessment(ctx, repository.RequeueSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_attempt",
		Payload:                      semanticAssessmentRetryPayload(stage, true, failure),
		RetryAfter:                   failure.RetryAfter,
		ReleaseAssessorAttempt:       releaseProviderAttempt,
		AssessorTurnsReserved:        assessorTurnsReserved,
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
		cause := firstError(failureCause)
		if isRepositoryDatabaseFailure(cause) {
			return s.completeTerminalWithoutRelationshipResults(ctx, scope, stage, cause)
		}
		return s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", stage, cause)
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
	return s.completeTerminalWithRelationshipResultsFailure(ctx, scope, stage, failureClass, providerStatus, providerTurns, nil, failureCause...)
}

func (s *submissionAssessmentPlacementWorkerService) completeTerminal(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	status, category, stage string,
	failureCause ...error,
) error {
	return s.completeTerminalWithRelationshipResults(ctx, scope, status, category, stage, nil, firstError(failureCause))
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
		RelationshipResults:          submissionAssessmentQuarantineRelationshipResults(plan),
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
	response assessor.SemanticAssessmentResponse,
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
		quote, err := assessor.SemanticEvidenceSpan(item.Fragment.Content, signal.Start, signal.End)
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
		RelationshipResults:          submissionAssessmentQuarantineRelationshipResults(plan),
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

func submissionAssessmentQuarantineRelationshipResults(
	plan submissionAssessmentPlan,
) []repository.SubmissionRelationshipResultInput {
	results := make([]repository.SubmissionRelationshipResultInput, len(plan.RelationshipTargets))
	for index, target := range plan.RelationshipTargets {
		results[index] = repository.SubmissionRelationshipResultInput{
			RelationshipRef: target.Target.ProposalID,
			Disposition:     "not_stored",
			Reason:          "security_quarantine",
		}
	}
	return results
}

func submissionAssessmentNotStoredRelationshipResults(
	results []repository.SubmissionRelationshipResultInput,
	reason string,
) []repository.SubmissionRelationshipResultInput {
	for index := range results {
		results[index].Disposition = "not_stored"
		results[index].Reason = reason
		results[index].Splits = nil
	}
	return results
}

func submissionAssessmentCommitInput(
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	plan submissionAssessmentPlan,
	response assessor.SemanticAssessmentResponse,
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
		for index := range relationshipResults {
			relationshipResults[index].Disposition = "not_stored"
			relationshipResults[index].Reason = "not_supported_by_evidence"
			relationshipResults[index].Splits = nil
		}
		return repository.CommitSubmissionAssessmentInput{}, &submissionAssessmentNoSupportedMemoryError{
			RelationshipResults: relationshipResults,
		}
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
