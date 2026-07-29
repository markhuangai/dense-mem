package memoryservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// SemanticPlacementMaxAssessorTurns covers one initial assessor response and
// the bounded complete-response corrections within the same conversation.
const SemanticPlacementMaxAssessorTurns = verifier.SemanticAssessmentMaxProviderTurns

var errSemanticAssessmentProviderAttemptConsumed = errors.New("semantic assessment provider attempt already consumed")

type semanticAssessmentPreflightError struct {
	stage string
	err   error
}

func (err *semanticAssessmentPreflightError) Error() string {
	if err == nil || err.err == nil {
		return "semantic assessment preflight failed"
	}
	return err.err.Error()
}

func (err *semanticAssessmentPreflightError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.err
}

func deterministicSemanticAssessmentPreflightError(stage, message string) error {
	return &semanticAssessmentPreflightError{
		stage: strings.TrimSpace(stage),
		err:   errors.New(message),
	}
}

func semanticAssessmentPreflightFailure(err error) (string, bool) {
	var preflight *semanticAssessmentPreflightError
	if errors.As(err, &preflight) && preflight.stage != "" {
		return preflight.stage, true
	}
	return "candidate_prefetch", false
}

type SemanticAssessmentCatalog interface {
	ListSemanticAssessmentEntityMatches(ctx context.Context, input repository.SemanticAssessmentEntityMatchInput) (repository.SemanticAssessmentEntityMatchResult, error)
	ListSemanticReviewEntityCandidates(ctx context.Context, input repository.SemanticReviewEntityCandidateInput) ([]repository.SemanticReviewEntityCandidate, error)
	ListSemanticAssessmentPredicateOptions(ctx context.Context, input repository.SemanticAssessmentPredicateOptionsInput) ([]repository.SemanticReviewPredicateCandidate, error)
}

type SemanticAssessorProvider interface {
	AssessSemantic(ctx context.Context, req verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error)
	ModelName() string
}

type SemanticAssessmentPlacementWorkerService interface {
	ProcessNextSemanticAssessmentPlacement(ctx context.Context) (bool, error)
}

type SemanticAssessmentPlacementWorkerDependencies struct {
	Ledger                    repository.LedgerRepository
	Assessments               repository.PlacementAssessmentRepository
	Commit                    repository.PlacementCommitRepository
	Catalog                   SemanticAssessmentCatalog
	Provider                  SemanticAssessorProvider
	Limits                    verifier.SemanticAssessmentLimits
	GlobalConfidenceThreshold float64
	TeamID                    string
	WorkerID                  string
	Lease                     time.Duration
	Now                       func() time.Time
	Metrics                   observability.DiscoverabilityMetrics
}

type semanticAssessmentPlacementWorkerService struct {
	ledger                    repository.LedgerRepository
	assessments               repository.PlacementAssessmentRepository
	commit                    repository.PlacementCommitRepository
	catalog                   SemanticAssessmentCatalog
	provider                  SemanticAssessorProvider
	limits                    verifier.SemanticAssessmentLimits
	globalConfidenceThreshold float64
	teamID                    string
	workerID                  string
	lease                     time.Duration
	now                       func() time.Time
	metrics                   observability.DiscoverabilityMetrics
}

func NewSemanticAssessmentPlacementWorkerService(
	deps SemanticAssessmentPlacementWorkerDependencies,
) SemanticAssessmentPlacementWorkerService {
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
	return &semanticAssessmentPlacementWorkerService{
		ledger:                    deps.Ledger,
		assessments:               deps.Assessments,
		commit:                    deps.Commit,
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

func (s *semanticAssessmentPlacementWorkerService) ProcessNextSemanticAssessmentPlacement(ctx context.Context) (bool, error) {
	if err := s.validateDependencies(); err != nil {
		return false, err
	}
	expired, err := s.assessments.ExpirePlacementAssessmentReviews(ctx, repository.ExpirePlacementAssessmentReviewsInput{
		TeamID: s.teamID,
		Now:    s.now().UTC(),
	})
	if err != nil {
		return false, fmt.Errorf("semantic assessment worker: expire reviews: %w", err)
	}
	observability.RecordAssessorReviewExpiry(s.metrics, expired)
	run, err := s.ledger.ClaimNextPlacementRun(ctx, s.teamID, s.workerID, s.lease)
	if err != nil {
		return false, err
	}
	if run == nil {
		return false, nil
	}
	placement, err := s.ledger.GetPlacementRun(ctx, repository.GetPlacementRunInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		IngestID:       run.IngestID,
	})
	if err != nil {
		return true, errors.Join(err, s.retryOrFail(ctx, *run, repository.PlacementItem{}, "placement_load", false, false))
	}
	item, fragment, ok := nextSemanticReviewPlacementItem(placement)
	if !ok {
		return true, errors.Join(errors.New("semantic assessment worker: no claimable placement item"), s.retryOrFail(ctx, *run, repository.PlacementItem{}, "placement_item", false, false))
	}

	request, err := s.buildRequest(ctx, *run, item, fragment, placement.Proposal)
	if err != nil {
		stage, terminal := semanticAssessmentPreflightFailure(err)
		if terminal {
			return true, errors.Join(err, s.completeTerminal(ctx, *run, item, string(domain.SemanticReviewTerminalFailure), "failed", stage))
		}
		return true, errors.Join(err, s.retryOrFail(ctx, *run, item, stage, false, false))
	}

	assessment, response, reused, providerAttempted, releaseProviderAttempt, err := s.loadOrAssess(ctx, *run, item, request)
	if err != nil {
		if errors.Is(err, errSemanticAssessmentProviderAttemptConsumed) {
			return true, s.completeTerminal(ctx, *run, item, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_attempt_consumed")
		}
		if providerAttempted && errors.Is(err, verifier.ErrVerifierMalformedResponse) {
			failureClass, providerTurns := semanticAssessmentMalformedFailure(err)
			return true, errors.Join(err, s.completeTerminalWithFailure(
				ctx,
				*run,
				item,
				"assessment",
				failureClass,
				0,
				providerTurns,
			))
		}
		if providerAttempted {
			return true, errors.Join(err, s.retryProviderFailure(
				ctx,
				*run,
				item,
				"assessment",
				releaseProviderAttempt,
				verifier.ProviderFailureDetails(err),
			))
		}
		return true, errors.Join(err, s.retryOrFail(ctx, *run, item, "assessment", providerAttempted, releaseProviderAttempt))
	}
	overrides, err := s.assessments.LoadPlacementAssessmentReviewOverrides(ctx, repository.LoadPlacementAssessmentReviewOverridesInput{
		TeamID:          run.TeamID,
		OwnerProfileID:  run.OwnerProfileID,
		PlacementItemID: item.PlacementItemID,
		AssessmentID:    assessment.AssessmentID,
	})
	if err != nil {
		return true, errors.Join(err, s.retryOrFail(ctx, *run, item, "review_override", false, false))
	}
	response = applySemanticAssessmentReviewOverrides(response, overrides)
	if len(response.SecuritySignals) > 0 {
		if err := s.recordSecuritySignals(ctx, *run, fragment, response); err != nil {
			return true, errors.Join(err, s.retryOrFail(ctx, *run, item, "security_signal", false, false))
		}
		return true, s.completeTerminal(ctx, *run, item, string(domain.SemanticReviewQuarantined), "quarantined", "security_signal")
	}

	policy, err := s.assessments.LoadAutoWriteConfidencePolicy(ctx, repository.LoadAutoWriteConfidencePolicyInput{
		TeamID:          run.TeamID,
		OwnerProfileID:  run.OwnerProfileID,
		GlobalThreshold: s.globalConfidenceThreshold,
	})
	if err != nil {
		return true, errors.Join(err, s.retryOrFail(ctx, *run, item, "confidence_policy", false, false))
	}
	commitInput, err := semanticAssessmentCommitInput(
		*run,
		item,
		fragment,
		request,
		response,
		assessment,
		policy,
		overrides,
		placement.Proposal,
	)
	if err != nil {
		return true, errors.Join(err, s.completeTerminal(ctx, *run, item, string(domain.SemanticReviewTerminalFailure), "failed", "deterministic_policy"))
	}
	commitInput.WorkerID = s.workerID
	commitInput.Payload["assessment_reused"] = reused
	recordSemanticAssessmentGateBands(s.metrics, commitInput)
	committed, err := s.commit.CommitPlacementSemanticResult(ctx, commitInput)
	if err != nil {
		return true, errors.Join(err, s.retryOrFail(ctx, *run, item, "semantic_commit", false, false))
	}
	if committed == nil {
		return true, errors.Join(errors.New("semantic assessment worker: nil semantic commit result"), s.retryOrFail(ctx, *run, item, "semantic_commit", false, false))
	}
	return true, nil
}

func (s *semanticAssessmentPlacementWorkerService) validateDependencies() error {
	if s.ledger == nil {
		return errors.New("semantic assessment worker: ledger repository is required")
	}
	if s.assessments == nil {
		return errors.New("semantic assessment worker: assessment repository is required")
	}
	if s.commit == nil {
		return errors.New("semantic assessment worker: placement commit repository is required")
	}
	if s.catalog == nil {
		return errors.New("semantic assessment worker: semantic catalog is required")
	}
	if s.provider == nil {
		return errors.New("semantic assessment worker: assessor provider is required")
	}
	if s.teamID == "" {
		return errors.New("semantic assessment worker: team_id is required")
	}
	if s.workerID == "" {
		return errors.New("semantic assessment worker: worker_id is required")
	}
	if s.globalConfidenceThreshold < 0 || s.globalConfidenceThreshold > 1 {
		return errors.New("semantic assessment worker: global confidence threshold must be between 0 and 1")
	}
	return nil
}

func (s *semanticAssessmentPlacementWorkerService) buildRequest(
	ctx context.Context,
	run repository.PlacementRun,
	item repository.PlacementItem,
	fragment repository.EvidenceFragment,
	proposal map[string]any,
) (verifier.SemanticAssessmentRequest, error) {
	evidenceID := fmt.Sprintf("evidence:%d", fragment.EvidenceIndex)
	_, requiredRelationshipRefs, err := semanticAssessmentTrustedRelationshipContexts(proposal, fragment, evidenceID)
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError(
			"trusted_context_validation",
			"semantic assessment trusted relationship context is invalid",
		)
	}
	groups, truncated, err := s.prefetchEntityCandidates(ctx, run, fragment, proposal, evidenceID)
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, err
	}
	predicates, err := s.catalog.ListSemanticAssessmentPredicateOptions(ctx, repository.SemanticAssessmentPredicateOptionsInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		QueryText:      fragment.Content,
		Limit:          verifier.SemanticAssessmentMaxPredicateOptions,
	})
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, fmt.Errorf("load predicate options: %w", err)
	}
	options := make([]verifier.SemanticAssessmentPredicateOption, 0, len(predicates))
	for _, predicate := range predicates {
		if predicate.LifecycleState != "active" {
			continue
		}
		options = append(options, verifier.SemanticAssessmentPredicateOption{
			PredicateKey:        predicate.PredicateKey,
			Version:             predicate.Version,
			Aliases:             append([]string(nil), predicate.Aliases...),
			AllowedSubjectKinds: append([]string(nil), predicate.AllowedSubjectKinds...),
			AllowedObjectKinds:  append([]string(nil), predicate.AllowedObjectKinds...),
			RelationshipKind:    predicate.RelationshipKind,
			CurrentCardinality:  predicate.CurrentCardinality,
		})
	}
	req := verifier.SemanticAssessmentRequest{
		RequestID:                 "semantic-assessment:" + item.ClaimKey,
		TeamID:                    run.TeamID,
		OwnerProfileID:            run.OwnerProfileID,
		Evidence:                  []verifier.SemanticReviewEvidence{semanticReviewEvidence(fragment, evidenceID)},
		ClientProposal:            assessmentClientProposalWithoutTrustedContext(proposal),
		EntityCandidateGroups:     groups,
		PredicateOptions:          options,
		RequiredRelationshipRefs:  requiredRelationshipRefs,
		CandidateContextTruncated: truncated,
	}
	return trimSemanticAssessmentCandidateContext(req, s.limits)
}

func (s *semanticAssessmentPlacementWorkerService) loadOrAssess(
	ctx context.Context,
	run repository.PlacementRun,
	item repository.PlacementItem,
	request verifier.SemanticAssessmentRequest,
) (*repository.PlacementAssessment, verifier.SemanticAssessmentResponse, bool, bool, bool, error) {
	stored, err := s.assessments.LoadPlacementAssessment(ctx, repository.LoadPlacementAssessmentInput{
		TeamID:          run.TeamID,
		OwnerProfileID:  run.OwnerProfileID,
		PlacementItemID: item.PlacementItemID,
	})
	if err == nil {
		response, err := decodeStoredAssessment(stored, s.limits)
		if err != nil {
			observability.RecordAssessorValidationFailure(s.metrics, "stored_response")
		} else {
			observability.RecordAssessorAssessmentPersistence(s.metrics, "reused")
			observability.RecordAssessorDuplicateRequestPrevention(s.metrics, "post_persist")
		}
		return stored, response, true, false, false, err
	}
	if !errors.Is(err, repository.ErrPlacementAssessmentNotFound) {
		return nil, verifier.SemanticAssessmentResponse{}, false, false, false, err
	}
	prepared, validationErrors := verifier.PrepareSemanticAssessmentRequest(request, s.limits)
	if len(validationErrors) > 0 {
		observability.RecordAssessorValidationFailure(s.metrics, "request")
		return nil, verifier.SemanticAssessmentResponse{}, false, false, false, errors.New("semantic assessment request is outside server limits")
	}
	reserved, err := s.assessments.ReservePlacementAssessmentProviderAttempt(ctx, repository.ReservePlacementAssessmentProviderAttemptInput{
		TeamID:           run.TeamID,
		OwnerProfileID:   run.OwnerProfileID,
		PlacementRunID:   run.PlacementRunID,
		PlacementItemID:  item.PlacementItemID,
		WorkerID:         s.workerID,
		ExpectedAttempts: run.Attempts,
	})
	if err != nil {
		return nil, verifier.SemanticAssessmentResponse{}, false, false, false, err
	}
	if !reserved {
		stored, err := s.assessments.LoadPlacementAssessment(ctx, repository.LoadPlacementAssessmentInput{
			TeamID:          run.TeamID,
			OwnerProfileID:  run.OwnerProfileID,
			PlacementItemID: item.PlacementItemID,
		})
		if err == nil {
			response, decodeErr := decodeStoredAssessment(stored, s.limits)
			if decodeErr != nil {
				observability.RecordAssessorValidationFailure(s.metrics, "stored_response")
			} else {
				observability.RecordAssessorAssessmentPersistence(s.metrics, "reused")
				observability.RecordAssessorDuplicateRequestPrevention(s.metrics, "post_persist")
			}
			return stored, response, true, false, false, decodeErr
		}
		if !errors.Is(err, repository.ErrPlacementAssessmentNotFound) {
			return nil, verifier.SemanticAssessmentResponse{}, false, false, false, err
		}
		observability.RecordAssessorDuplicateRequestPrevention(s.metrics, "reservation")
		return nil, verifier.SemanticAssessmentResponse{}, false, false, false, errSemanticAssessmentProviderAttemptConsumed
	}
	if prepared.CandidateContextTruncated {
		observability.RecordAssessorCandidateTruncation(s.metrics)
	}
	started := time.Now()
	response, err := s.provider.AssessSemantic(ctx, prepared)
	if err != nil {
		outcome := "provider_error"
		releaseProviderAttempt := true
		if errors.Is(err, verifier.ErrVerifierMalformedResponse) {
			outcome = "malformed_exhausted"
			releaseProviderAttempt = false
		}
		observability.RecordAssessorCall(s.metrics, prepared.InputTokens, 0, time.Since(started).Seconds(), outcome)
		return nil, verifier.SemanticAssessmentResponse{}, false, true, releaseProviderAttempt, err
	}
	normalized, validationErrors := verifier.PrepareSemanticAssessmentResponse(prepared, response, s.limits)
	if len(validationErrors) > 0 {
		observability.RecordAssessorCall(s.metrics, prepared.InputTokens, response.OutputTokens, time.Since(started).Seconds(), "malformed_exhausted")
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
		inputTokens = prepared.InputTokens
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
	persisted, existing, err := s.assessments.PersistPlacementAssessment(ctx, repository.PersistPlacementAssessmentInput{
		TeamID:                    run.TeamID,
		OwnerProfileID:            run.OwnerProfileID,
		PlacementItemID:           item.PlacementItemID,
		ClaimKey:                  item.ClaimKey,
		RequestID:                 prepared.RequestID,
		AssessorContractVersion:   domain.ContractVersion,
		Model:                     s.provider.ModelName(),
		PromptRevision:            verifier.SemanticAssessmentPromptRev,
		Tokenizer:                 assessmentTokenizer(s.limits),
		InputTokens:               inputTokens,
		OutputTokens:              normalized.OutputTokens,
		CandidateContextTokens:    prepared.CandidateContextTokens,
		CandidateContextTruncated: prepared.CandidateContextTruncated,
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
		storedResponse, err := decodeStoredAssessment(persisted, s.limits)
		if err != nil {
			observability.RecordAssessorValidationFailure(s.metrics, "stored_response")
		}
		return persisted, storedResponse, true, true, false, err
	}
	observability.RecordAssessorAssessmentPersistence(s.metrics, "persisted")
	return persisted, normalized, false, true, false, nil
}

func recordSemanticAssessmentGateBands(metrics observability.DiscoverabilityMetrics, input repository.CommitPlacementSemanticInput) {
	for _, observation := range input.RelationshipObservations {
		observability.RecordAssessorConfidenceGate(metrics, observation.GateResult)
	}
	for _, review := range input.RelationshipReviews {
		observability.RecordAssessorConfidenceGate(metrics, review.GateResult)
	}
}

func decodeStoredAssessment(
	assessment *repository.PlacementAssessment,
	limits verifier.SemanticAssessmentLimits,
) (verifier.SemanticAssessmentResponse, error) {
	if assessment == nil {
		return verifier.SemanticAssessmentResponse{}, errors.New("stored placement assessment is nil")
	}
	canonicalJSON, err := verifier.CanonicalJSON(assessment.NormalizedResponse)
	if err != nil {
		return verifier.SemanticAssessmentResponse{}, fmt.Errorf("stored placement assessment response is invalid JSON: %w", err)
	}
	if semanticAssessmentHash(canonicalJSON) != assessment.ResponseHash {
		return verifier.SemanticAssessmentResponse{}, errors.New("stored placement assessment hash mismatch")
	}
	return verifier.DecodeSemanticAssessmentResponseJSON(assessment.NormalizedResponse, limits)
}

func (s *semanticAssessmentPlacementWorkerService) retryOrFail(
	ctx context.Context,
	run repository.PlacementRun,
	item repository.PlacementItem,
	stage string,
	providerAttempted bool,
	releaseProviderAttempt bool,
) error {
	if item.PlacementItemID == "" {
		return s.ledger.FinishPlacementRun(ctx, run.TeamID, run.PlacementRunID, s.workerID, string(domain.PlacementRunFailed), "semantic assessment failed before item selection")
	}
	if run.MaxAttempts > 0 && run.Attempts >= run.MaxAttempts {
		return s.completeTerminal(ctx, run, item, string(domain.SemanticReviewTerminalFailure), "failed", stage)
	}
	_, err := s.commit.RequeuePlacementReviewResult(ctx, repository.RequeuePlacementReviewInput{
		TeamID:                 run.TeamID,
		OwnerProfileID:         run.OwnerProfileID,
		IngestID:               run.IngestID,
		PlacementRunID:         run.PlacementRunID,
		PlacementItemID:        item.PlacementItemID,
		WorkerID:               s.workerID,
		ExpectedAttempts:       run.Attempts,
		OutcomeKind:            "semantic_assessment_attempt",
		Payload:                semanticAssessmentRetryPayload(stage, providerAttempted),
		ReleaseAssessorAttempt: releaseProviderAttempt,
	})
	return err
}

func (s *semanticAssessmentPlacementWorkerService) completeTerminal(
	ctx context.Context,
	run repository.PlacementRun,
	item repository.PlacementItem,
	status, category, stage string,
) error {
	_, err := s.commit.CompletePlacementReviewResult(ctx, repository.CompletePlacementReviewInput{
		TeamID:           run.TeamID,
		OwnerProfileID:   run.OwnerProfileID,
		IngestID:         run.IngestID,
		PlacementRunID:   run.PlacementRunID,
		PlacementItemID:  item.PlacementItemID,
		WorkerID:         s.workerID,
		ExpectedAttempts: run.Attempts,
		Status:           status,
		Category:         category,
		Payload: map[string]any{
			"assessor_contract": domain.ContractVersion,
			"failure_stage":     strings.TrimSpace(stage),
		},
	})
	return err
}

func (s *semanticAssessmentPlacementWorkerService) recordSecuritySignals(
	ctx context.Context,
	run repository.PlacementRun,
	fragment repository.EvidenceFragment,
	response verifier.SemanticAssessmentResponse,
) error {
	signals := make([]repository.SecuritySignalInput, 0, len(response.SecuritySignals))
	for _, signal := range response.SecuritySignals {
		quote, err := verifier.SemanticEvidenceSpan(fragment.Content, signal.Start, signal.End)
		if err != nil {
			return err
		}
		signals = append(signals, repository.SecuritySignalInput{
			Kind:      signal.Kind,
			Severity:  "high",
			SpanStart: signal.Start,
			SpanEnd:   signal.End,
			Quote:     quote,
		})
	}
	_, err := s.ledger.AppendSecurityEvent(ctx, repository.SecurityEventInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		IngestID:       run.IngestID,
		FragmentID:     fragment.FragmentID,
		SecurityEventDraft: repository.SecurityEventDraft{
			EventKind:      "verifier_signal",
			Decision:       "quarantine",
			ScanPolicyHash: "dense-mem.v2.4",
			Reason:         "semantic assessor reported security signal",
			Signals:        signals,
		},
	})
	return err
}

func assessmentTokenizer(limits verifier.SemanticAssessmentLimits) string {
	if tokenizer := strings.TrimSpace(limits.Tokenizer); tokenizer != "" {
		return tokenizer
	}
	return verifier.DefaultSemanticAssessmentLimits().Tokenizer
}

func semanticAssessmentHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func cloneAssessmentProposal(proposal map[string]any) map[string]any {
	if proposal == nil {
		return map[string]any{}
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return map[string]any{}
	}
	var cloned map[string]any
	if json.Unmarshal(encoded, &cloned) != nil {
		return map[string]any{}
	}
	return cloned
}

type semanticAssessmentTrustedRelationshipContext struct {
	correctionTarget *repository.PlacementCorrectionTargetInput
	conflictContext  *repository.PlacementConflictContextInput
}

func assessmentClientProposalWithoutTrustedContext(proposal map[string]any) map[string]any {
	cloned := cloneAssessmentProposal(proposal)
	for _, relationship := range placementReviewObjectArray(cloned, "relationship_hints", "relationships") {
		_, hasCorrection := relationship["correction_target"]
		_, hasConflict := relationship["conflict_context"]
		if hasCorrection || hasConflict {
			proposalID := reviewFirstNonEmpty(
				reviewString(relationship, "proposal_id"),
				reviewString(relationship, "ref"),
				reviewString(relationship, "legacy_id"),
			)
			if proposalID != "" {
				relationship["proposal_id"] = proposalID
			}
		}
		delete(relationship, "correction_target")
		delete(relationship, "conflict_context")
	}
	return cloned
}

func semanticAssessmentTrustedRelationshipContexts(
	proposal map[string]any,
	fragment repository.EvidenceFragment,
	evidenceID string,
) (map[string]semanticAssessmentTrustedRelationshipContext, []verifier.SemanticAssessmentRequiredRelationshipRef, error) {
	contexts := map[string]semanticAssessmentTrustedRelationshipContext{}
	required := make([]verifier.SemanticAssessmentRequiredRelationshipRef, 0)
	for _, relationship := range placementReviewObjectArray(proposal, "relationship_hints", "relationships") {
		_, hasCorrection := relationship["correction_target"]
		_, hasConflict := relationship["conflict_context"]
		if !hasCorrection && !hasConflict {
			continue
		}
		spans := make([]verifier.SemanticAssessmentEvidenceSpan, 0)
		for _, span := range placementReviewEvidenceSpanHints(relationship) {
			if span.evidenceIndex != fragment.EvidenceIndex {
				continue
			}
			if _, err := verifier.SemanticEvidenceSpan(fragment.Content, span.start, span.end); err != nil {
				return nil, nil, err
			}
			spans = append(spans, verifier.SemanticAssessmentEvidenceSpan{
				EvidenceID: evidenceID,
				Start:      span.start,
				End:        span.end,
			})
		}
		if len(spans) == 0 {
			continue
		}
		proposalID := reviewFirstNonEmpty(
			reviewString(relationship, "proposal_id"),
			reviewString(relationship, "ref"),
			reviewString(relationship, "legacy_id"),
		)
		if proposalID == "" {
			return nil, nil, errors.New("trusted relationship context proposal_id is required")
		}
		if _, exists := contexts[proposalID]; exists {
			return nil, nil, errors.New("trusted relationship context proposal_id is duplicated")
		}
		context := semanticAssessmentTrustedRelationshipContext{}
		if hasCorrection {
			target, ok := placementReviewCorrectionTarget(relationship)
			if !ok {
				return nil, nil, errors.New("trusted relationship correction_target is invalid")
			}
			context.correctionTarget = &repository.PlacementCorrectionTargetInput{
				RelationshipID:  target.RelationshipID,
				ExpectedVersion: target.ExpectedVersion,
			}
		}
		if hasConflict {
			conflict, ok := placementReviewConflictContext(relationship)
			if !ok {
				return nil, nil, errors.New("trusted relationship conflict_context is invalid")
			}
			context.conflictContext = &repository.PlacementConflictContextInput{
				ConflictID:      conflict.ConflictID,
				ExpectedVersion: conflict.ExpectedVersion,
			}
		}
		contexts[proposalID] = context
		required = append(required, verifier.SemanticAssessmentRequiredRelationshipRef{
			ProposalID: proposalID,
			Evidence:   spans,
		})
	}
	return contexts, required, nil
}

type assessmentEntityCommitState struct {
	resolved bool
	group    *verifier.SemanticAssessmentEntityCandidateGroup
	kind     string
}

func semanticAssessmentCommitInput(
	run repository.PlacementRun,
	item repository.PlacementItem,
	fragment repository.EvidenceFragment,
	request verifier.SemanticAssessmentRequest,
	response verifier.SemanticAssessmentResponse,
	assessment *repository.PlacementAssessment,
	policy repository.AutoWriteConfidencePolicy,
	overrides repository.PlacementAssessmentReviewOverrides,
	proposal map[string]any,
) (repository.CommitPlacementSemanticInput, error) {
	if assessment == nil || strings.TrimSpace(assessment.AssessmentID) == "" {
		return repository.CommitPlacementSemanticInput{}, errors.New("persisted assessment is required before semantic commit")
	}
	if policy.Threshold < 0 || policy.Threshold > 1 {
		return repository.CommitPlacementSemanticInput{}, errors.New("effective confidence threshold is invalid")
	}
	trustedContexts, requiredRelationshipRefs, err := semanticAssessmentTrustedRelationshipContexts(
		proposal,
		fragment,
		fmt.Sprintf("evidence:%d", fragment.EvidenceIndex),
	)
	if err != nil {
		return repository.CommitPlacementSemanticInput{}, fmt.Errorf("trusted relationship context: %w", err)
	}
	if validationErrors := verifier.ValidateSemanticAssessmentRequiredRelationshipRefs(requiredRelationshipRefs, response.RelationshipResults); len(validationErrors) > 0 {
		return repository.CommitPlacementSemanticInput{}, errors.New("semantic assessment trusted relationship correspondence is invalid")
	}
	groups := assessmentGroupsBySpan(request.EntityCandidateGroups)
	predicates := assessmentPredicatesByKeyVersion(request.PredicateOptions)
	policyVersion := assessmentPolicyVersion(policy)
	threshold := policy.Threshold

	entities, entityStates, err := semanticAssessmentEntityResolutions(
		response,
		fragment,
		assessment.AssessmentID,
		groups,
		overrides.EntitySelections,
	)
	if err != nil {
		return repository.CommitPlacementSemanticInput{}, err
	}
	observations := make([]repository.PlacementRelationshipDecisionInput, 0, len(response.RelationshipResults))
	reviews := make([]repository.PlacementRelationshipReviewInput, 0)
	for _, result := range response.RelationshipResults {
		supports, err := semanticAssessmentSupport(fragment, assessment.AssessmentID, result.Evidence)
		if err != nil {
			return repository.CommitPlacementSemanticInput{}, err
		}
		support, additionalSupports := semanticAssessmentPrimarySupport(supports)
		objectRef, objectValue, err := semanticAssessmentObject(result)
		if err != nil {
			return repository.CommitPlacementSemanticInput{}, err
		}
		reviewKind := semanticAssessmentRelationshipReviewKind(result, entityStates, predicates)
		if reviewKind != "" {
			reviews = append(reviews, semanticAssessmentRelationshipReviewInput(
				result,
				objectRef,
				objectValue,
				support,
				additionalSupports,
				assessment.AssessmentID,
				policyVersion,
				assessment.Model,
				assessment.ResponseHash,
				&threshold,
				"not_applicable",
				reviewKind,
				assessmentReviewOptions(reviewKind, result, entityStates, request.PredicateOptions),
			))
			continue
		}
		predicate := predicates[assessmentPredicateKey(*result.PredicateKey, *result.PredicateVersion)]
		validFrom, validTo, err := semanticAssessmentValidity(result)
		if err != nil {
			return repository.CommitPlacementSemanticInput{}, err
		}
		gateResult := "not_applicable"
		suppressSupport := false
		reviewKind = ""
		if result.Polarity == "+" && result.EvidenceVerdict == string(domain.VerificationEntailed) {
			if result.Confidence >= threshold {
				gateResult = "meets_write_threshold"
			} else {
				gateResult = "below_write_threshold"
				suppressSupport = true
				reviewKind = "support_confidence"
			}
		} else if result.EvidenceVerdict == string(domain.VerificationInsufficient) {
			reviewKind = "support_confidence"
		}
		confidence := result.Confidence
		observation := repository.PlacementRelationshipDecisionInput{
			Ref:               result.Ref,
			SubjectRef:        result.SubjectRef,
			OriginalPredicate: result.OriginalPredicate,
			PredicateKey:      *result.PredicateKey,
			PredicateVersion:  *result.PredicateVersion,
			PredicateCandidate: &repository.PlacementPredicateCandidateInput{
				PredicateKey:     predicate.PredicateKey,
				PredicateVersion: predicate.Version,
				RelationshipKind: predicate.RelationshipKind,
			},
			ObjectRef:       objectRef,
			ObjectValue:     objectValue,
			Polarity:        result.Polarity,
			ScopeKey:        semanticAssessmentScopeKey(result),
			ValidFrom:       validFrom,
			ValidTo:         validTo,
			EvidenceVerdict: result.EvidenceVerdict,
			Confidence:      &confidence,
			Rationale:       result.Rationale,
			Model:           assessment.Model,
			ResponseHash:    assessment.ResponseHash,
			Support:         support,
			Supports:        additionalSupports,
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
			GateResult:              gateResult,
			SuppressSupport:         suppressSupport,
			SemanticReviewKind:      reviewKind,
			ReviewQuestion:          assessmentReviewQuestion(reviewKind),
			ReviewOptions:           assessmentReviewOptions(reviewKind, result, entityStates, request.PredicateOptions),
			ReviewGuidance:          assessmentReviewGuidance(reviewKind),
		}
		if trustedContext, ok := trustedContexts[result.Ref]; ok {
			attachSemanticAssessmentTrustedRelationshipContext(&observation, trustedContext)
		}
		observations = append(observations, observation)
	}
	return repository.CommitPlacementSemanticInput{
		TeamID:                   run.TeamID,
		OwnerProfileID:           run.OwnerProfileID,
		IngestID:                 run.IngestID,
		PlacementRunID:           run.PlacementRunID,
		PlacementItemID:          item.PlacementItemID,
		WorkerID:                 "",
		ExpectedAttempts:         run.Attempts,
		OutcomeKind:              "semantic_assessment_commit",
		Status:                   string(domain.SemanticReviewAccepted),
		Category:                 "validated_claim",
		EntityResolutions:        entities,
		RelationshipObservations: observations,
		RelationshipReviews:      reviews,
		Payload: map[string]any{
			"assessor_contract": domain.ContractVersion,
			"assessment_id":     assessment.AssessmentID,
			"response_hash":     assessment.ResponseHash,
			"policy_version":    policyVersion,
			"request_id":        assessment.RequestID,
		},
	}, nil
}

func attachSemanticAssessmentTrustedRelationshipContext(
	observation *repository.PlacementRelationshipDecisionInput,
	context semanticAssessmentTrustedRelationshipContext,
) {
	if observation == nil {
		return
	}
	if context.correctionTarget != nil {
		target := *context.correctionTarget
		observation.CorrectionTarget = &target
	}
	if context.conflictContext != nil {
		conflict := *context.conflictContext
		observation.ConflictContext = &conflict
	}
}

func assessmentGroupsBySpan(groups []verifier.SemanticAssessmentEntityCandidateGroup) map[string]*verifier.SemanticAssessmentEntityCandidateGroup {
	result := make(map[string]*verifier.SemanticAssessmentEntityCandidateGroup, len(groups))
	for index := range groups {
		group := &groups[index]
		result[assessmentCandidateGroupKey(group.EvidenceID, group.Start, group.End)] = group
	}
	return result
}

func assessmentPredicatesByKeyVersion(options []verifier.SemanticAssessmentPredicateOption) map[string]verifier.SemanticAssessmentPredicateOption {
	result := make(map[string]verifier.SemanticAssessmentPredicateOption, len(options))
	for _, option := range options {
		result[assessmentPredicateKey(option.PredicateKey, option.Version)] = option
	}
	return result
}

func assessmentPredicateKey(key string, version int) string {
	return strings.TrimSpace(key) + ":" + strconv.Itoa(version)
}

func assessmentPolicyVersion(policy repository.AutoWriteConfidencePolicy) string {
	version := strings.TrimSpace(policy.Version)
	if version == "" {
		version = repository.AssessmentPolicyVersion
	}
	return version + ":config-" + strconv.FormatInt(policy.ConfigVersion, 10)
}
