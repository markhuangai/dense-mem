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

// SubmissionAssessmentCatalog provides the complete, bounded server-owned
// catalog used by one closed submission assessment.
type SubmissionAssessmentCatalog interface {
	ListSubmissionAssessmentEntityCatalog(ctx context.Context, input repository.SubmissionAssessmentEntityCatalogInput) (repository.SubmissionAssessmentEntityCatalogResult, error)
	ListSubmissionAssessmentPredicateCatalog(ctx context.Context, input repository.SubmissionAssessmentPredicateCatalogInput) (repository.SubmissionAssessmentPredicateCatalogResult, error)
}

type SubmissionAssessmentPlacementWorkerService interface {
	ProcessNextSubmissionAssessmentPlacement(ctx context.Context) (bool, error)
}

type SubmissionAssessmentPlacementWorkerDependencies struct {
	Ledger                    repository.LedgerRepository
	Assessments               repository.SubmissionAssessmentRepository
	Catalog                   SubmissionAssessmentCatalog
	Provider                  SemanticAssessorProvider
	Limits                    verifier.SemanticAssessmentLimits
	GlobalConfidenceThreshold float64
	TeamID                    string
	WorkerID                  string
	Lease                     time.Duration
	Now                       func() time.Time
	Metrics                   observability.DiscoverabilityMetrics
}

type submissionAssessmentPlacementWorkerService struct {
	ledger                    repository.LedgerRepository
	assessments               repository.SubmissionAssessmentRepository
	catalog                   SubmissionAssessmentCatalog
	provider                  SemanticAssessorProvider
	limits                    verifier.SemanticAssessmentLimits
	globalConfidenceThreshold float64
	teamID                    string
	workerID                  string
	lease                     time.Duration
	now                       func() time.Time
	metrics                   observability.DiscoverabilityMetrics
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
	return &submissionAssessmentPlacementWorkerService{
		ledger:                    deps.Ledger,
		assessments:               deps.Assessments,
		catalog:                   deps.Catalog,
		provider:                  deps.Provider,
		limits:                    deps.Limits,
		globalConfidenceThreshold: deps.GlobalConfidenceThreshold,
		teamID:                    strings.TrimSpace(deps.TeamID),
		workerID:                  strings.TrimSpace(deps.WorkerID),
		lease:                     lease,
		now:                       now,
		metrics:                   metrics,
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
		return true, errors.Join(err, s.retryOrFail(ctx, *run, scope, "placement_load", false, false))
	}
	plan, err := buildSubmissionAssessmentPlan(placement)
	if err != nil {
		return true, errors.Join(err, s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "trusted_context_validation"))
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
			return true, errors.Join(err, s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", stage))
		}
		return true, errors.Join(err, s.retryOrFail(ctx, *run, scope, stage, false, false))
	}

	assessment, response, reused, providerAttempted, releaseProviderAttempt, err := s.loadOrAssess(ctx, *run, scope, request)
	if err != nil {
		if errors.Is(err, repository.ErrSubmissionAssessorAttemptConsumed) {
			return true, s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_attempt_consumed")
		}
		if providerAttempted && errors.Is(err, verifier.ErrVerifierMalformedResponse) {
			failureClass, providerTurns := semanticAssessmentMalformedFailure(err)
			return true, errors.Join(err, s.completeTerminalWithFailure(ctx, scope, "assessment", failureClass, 0, providerTurns))
		}
		if providerAttempted {
			return true, errors.Join(err, s.retryProviderFailure(ctx, *run, scope, "assessment", releaseProviderAttempt, verifier.ProviderFailureDetails(err)))
		}
		return true, errors.Join(err, s.retryOrFail(ctx, *run, scope, "assessment", providerAttempted, releaseProviderAttempt))
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
		return true, errors.Join(err, s.retryOrFail(ctx, *run, scope, "confidence_policy", false, false))
	}
	commitInput, err := submissionAssessmentCommitInput(*run, scope, plan, request, response, assessment, policy, reused)
	if errors.Is(err, errSubmissionAssessmentRequiresReview) {
		return true, s.completeTerminal(ctx, scope, string(domain.SemanticReviewReviewRequired), "candidate", "policy_review")
	}
	if err != nil {
		return true, errors.Join(err, s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "deterministic_policy"))
	}
	recordSubmissionAssessmentGateBands(s.metrics, commitInput)
	committed, err := s.assessments.CommitSubmissionAssessment(ctx, commitInput)
	if errors.Is(err, repository.ErrSubmissionAssessmentNonPromotable) || errors.Is(err, repository.ErrSubmissionPredicateRegistrationHeld) {
		return true, s.completeTerminal(ctx, scope, string(domain.SemanticReviewReviewRequired), "candidate", "commit_review")
	}
	if errors.Is(err, repository.ErrSubmissionAssessmentScopeMismatch) {
		return true, errors.Join(err, s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_scope"))
	}
	if errors.Is(err, repository.ErrPlacementStaleSource) {
		return true, s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", "stale_source")
	}
	if err != nil {
		return true, errors.Join(err, s.retryOrFail(ctx, *run, scope, "semantic_commit", false, false))
	}
	if committed == nil {
		return true, errors.Join(errors.New("submission assessment worker: nil semantic commit result"), s.retryOrFail(ctx, *run, scope, "semantic_commit", false, false))
	}
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

func submissionAssessmentRunScope(run repository.PlacementRun, workerID string) repository.SubmissionAssessmentRunScope {
	return repository.SubmissionAssessmentRunScope{
		TeamID:           run.TeamID,
		OwnerProfileID:   run.OwnerProfileID,
		IngestID:         run.IngestID,
		PlacementRunID:   run.PlacementRunID,
		WorkerID:         workerID,
		ExpectedAttempts: run.Attempts,
	}
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
	response, err := s.provider.AssessSemantic(providerCtx, request)
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
	normalized, validationErrors := verifier.PrepareSemanticAssessmentResponse(request, response, s.limits)
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
		Model:                     s.provider.ModelName(),
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

func decodeStoredSubmissionAssessment(
	assessment *repository.SubmissionAssessment,
	request verifier.SemanticAssessmentRequest,
	limits verifier.SemanticAssessmentLimits,
) (verifier.SemanticAssessmentResponse, error) {
	if assessment == nil {
		return verifier.SemanticAssessmentResponse{}, errors.New("stored submission assessment is nil")
	}
	canonicalJSON, err := verifier.CanonicalJSON(assessment.NormalizedResponse)
	if err != nil {
		return verifier.SemanticAssessmentResponse{}, fmt.Errorf("stored submission assessment response is invalid JSON: %w", err)
	}
	if semanticAssessmentHash(canonicalJSON) != assessment.ResponseHash {
		return verifier.SemanticAssessmentResponse{}, errors.New("stored submission assessment hash mismatch")
	}
	response, err := verifier.DecodeSemanticAssessmentResponseJSON(assessment.NormalizedResponse, limits)
	if err != nil {
		return verifier.SemanticAssessmentResponse{}, err
	}
	prepared, validationErrors := verifier.PrepareSemanticAssessmentResponse(request, response, limits)
	if len(validationErrors) > 0 {
		return verifier.SemanticAssessmentResponse{}, errors.New("stored submission assessment does not match its current contract")
	}
	return prepared, nil
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
		return errors.New("submission assessment worker: nil retry result")
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
) error {
	if run.MaxAttempts > 0 && run.Attempts >= run.MaxAttempts {
		return s.completeTerminal(ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", stage)
	}
	requeued, err := s.assessments.RequeueSubmissionAssessment(ctx, repository.RequeueSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_attempt",
		Payload:                      semanticAssessmentRetryPayload(stage, providerAttempted),
		ReleaseAssessorAttempt:       releaseProviderAttempt,
	})
	if err == nil && requeued == nil {
		return errors.New("submission assessment worker: nil retry result")
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
) error {
	payload := map[string]any{
		"assessor_contract": domain.ContractVersion,
		"failure_stage":     strings.TrimSpace(stage),
	}
	if failureClass = strings.TrimSpace(failureClass); failureClass != "" {
		payload["failure_class"] = failureClass
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
		return errors.New("submission assessment worker: nil terminal result")
	}
	return err
}

func (s *submissionAssessmentPlacementWorkerService) completeTerminal(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	status, category, stage string,
) error {
	payload := map[string]any{
		"assessor_contract": domain.ContractVersion,
		"failure_stage":     strings.TrimSpace(stage),
	}
	completed, err := s.assessments.CompleteSubmissionAssessment(ctx, repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_terminal",
		Status:                       status,
		Category:                     category,
		Payload:                      payload,
	})
	if err == nil && completed == nil {
		return errors.New("submission assessment worker: nil terminal result")
	}
	return err
}

func (s *submissionAssessmentPlacementWorkerService) recordFirstDisposition(
	ctx context.Context,
	run repository.PlacementRun,
	disposition *repository.PlacementFirstDisposition,
) {
	if disposition == nil || !disposition.IsRemember {
		return
	}
	metricCtx := observability.WithMetricIdentity(ctx, run.TeamID, run.OwnerProfileID)
	observability.RecordRememberFirstDisposition(metricCtx, s.metrics, disposition.CompletedAt.Sub(disposition.CreatedAt), disposition.Status)
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
		return err
	}
	completed, err := s.assessments.CompleteSubmissionAssessment(ctx, repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_security",
		Status:                       string(domain.SemanticReviewQuarantined),
		Category:                     "quarantined",
		Payload: map[string]any{
			"assessor_contract": domain.ContractVersion,
			"failure_stage":     stage,
		},
		SecurityQuarantines: quarantines,
	})
	if err == nil && completed == nil {
		return errors.New("submission assessment worker: nil security terminal result")
	}
	return err
}

func submissionAssessmentDeterministicQuarantines(
	plan submissionAssessmentPlan,
	scan SubmissionSecurityBatchScan,
) ([]repository.SubmissionAssessmentSecurityQuarantineInput, error) {
	if len(plan.Items) == 0 {
		return nil, errors.New("submission assessment security quarantine requires evidence")
	}
	type group struct {
		signals []SubmissionSecurityBatchSignal
	}
	byFragmentID := map[string]*group{}
	for _, signal := range scan.Signals {
		item := plan.Items[0]
		if signal.Source == submissionSecuritySourceEvidence && signal.EvidenceIndex >= 0 && signal.EvidenceIndex < len(plan.Items) {
			item = plan.Items[signal.EvidenceIndex]
		}
		entry := byFragmentID[item.Fragment.FragmentID]
		if entry == nil {
			entry = &group{}
			byFragmentID[item.Fragment.FragmentID] = entry
		}
		entry.signals = append(entry.signals, signal)
	}
	if len(byFragmentID) == 0 {
		byFragmentID[plan.Items[0].Fragment.FragmentID] = &group{}
	}
	quarantines := make([]repository.SubmissionAssessmentSecurityQuarantineInput, 0, len(byFragmentID))
	for _, item := range plan.Items {
		entry := byFragmentID[item.Fragment.FragmentID]
		if entry == nil {
			continue
		}
		signals := make([]SubmissionSecuritySignal, 0, len(entry.signals))
		sources := make([]string, 0, len(entry.signals))
		for _, signal := range entry.signals {
			signals = append(signals, signal.SubmissionSecuritySignal)
			sources = append(sources, signal.Source)
		}
		draft := submissionSecurityQuarantineEventForSignals(signals, scan.SignalsTruncated, sources)
		for index, signal := range entry.signals {
			if signal.Source != submissionSecuritySourceEvidence || index >= len(draft.Signals) {
				continue
			}
			quote, err := verifier.SemanticEvidenceSpan(item.Fragment.Content, signal.Start, signal.End)
			if err == nil {
				draft.Signals[index].Quote = quote
			}
		}
		quarantines = append(quarantines, repository.SubmissionAssessmentSecurityQuarantineInput{
			FragmentID:         item.Fragment.FragmentID,
			SecurityEventDraft: draft,
		})
	}
	if len(quarantines) == 0 {
		return nil, errors.New("submission assessment security quarantine has no target")
	}
	return quarantines, nil
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
				EventKind:      "verifier_signal",
				Decision:       "quarantine",
				ScanPolicyHash: "dense-mem.v2.4",
				Reason:         "semantic assessor reported security signal",
				Signals:        entry.signals,
			},
		})
	}
	if len(quarantines) == 0 {
		return errors.New("submission assessor security quarantine has no target")
	}
	completed, err := s.assessments.CompleteSubmissionAssessment(ctx, repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_security",
		Status:                       string(domain.SemanticReviewQuarantined),
		Category:                     "quarantined",
		Payload: map[string]any{
			"assessor_contract": domain.ContractVersion,
			"failure_stage":     stage,
		},
		SecurityQuarantines: quarantines,
	})
	if err == nil && completed == nil {
		return errors.New("submission assessment worker: nil provider security terminal result")
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
	for _, result := range response.EntityResults {
		target, ok := plan.entityTargetsByRef[result.Ref]
		if !ok {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor entity result is outside the contract")
		}
		if result.Action == string(domain.EntityResolutionAmbiguous) {
			return repository.CommitSubmissionAssessmentInput{}, errSubmissionAssessmentRequiresReview
		}
		resolution := repository.PlacementEntityResolutionInput{
			MentionRef:    result.Ref,
			Action:        result.Action,
			EntityKind:    result.Kind,
			CanonicalName: result.Surface,
			FragmentID:    target.FragmentID,
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
		entityResolutions = append(entityResolutions, repository.SubmissionAssessmentEntityResolutionInput{
			PlacementItemID: target.PlacementItemID,
			Resolution:      resolution,
		})
		entityKinds[result.Ref] = result.Kind
	}
	if len(entityResolutions) != len(plan.EntityTargets) {
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
		confidence := result.Confidence
		observation := repository.PlacementRelationshipDecisionInput{
			Ref:               result.Ref,
			SubjectRef:        result.SubjectRef,
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

func submissionAssessmentSupports(
	plan submissionAssessmentPlan,
	assessmentID string,
	spans []verifier.SemanticAssessmentEvidenceSpan,
) ([]repository.EvidenceSupportInput, error) {
	if len(spans) == 0 {
		return nil, errors.New("submission assessor relationship has no evidence span")
	}
	supports := make([]repository.EvidenceSupportInput, 0, len(spans))
	for _, span := range spans {
		item, ok := plan.itemsByEvidenceID[span.EvidenceID]
		if !ok {
			return nil, errors.New("submission assessor evidence span is outside the run")
		}
		quote, err := verifier.SemanticEvidenceSpan(item.Fragment.Content, span.Start, span.End)
		if err != nil {
			return nil, err
		}
		authority, err := semanticSupportAuthority(item.Fragment.Authority)
		if err != nil {
			return nil, err
		}
		supports = append(supports, repository.EvidenceSupportInput{
			FragmentID:       item.Fragment.FragmentID,
			SourceGroupKey:   fmt.Sprintf("semantic_assessment:%s:%s:%d:%d", assessmentID, span.EvidenceID, span.Start, span.End),
			SourceID:         item.Fragment.SourceID,
			SourceRevisionID: item.Fragment.SourceRevisionID,
			SpanStart:        span.Start,
			SpanEnd:          span.End,
			Quote:            quote,
			Authority:        authority,
			Metadata: map[string]any{
				"semantic_contract": domain.ContractVersion,
				"assessment_id":     assessmentID,
				"evidence_id":       span.EvidenceID,
			},
		})
	}
	return supports, nil
}

func submissionAssessmentItemForFragment(plan submissionAssessmentPlan, fragmentID string) (submissionAssessmentItem, bool) {
	for _, item := range plan.Items {
		if item.Fragment.FragmentID == fragmentID {
			return item, true
		}
	}
	return submissionAssessmentItem{}, false
}

func recordSubmissionAssessmentGateBands(metrics observability.DiscoverabilityMetrics, input repository.CommitSubmissionAssessmentInput) {
	for _, observation := range input.RelationshipObservations {
		observability.RecordAssessorConfidenceGate(metrics, observation.Observation.GateResult)
	}
}
