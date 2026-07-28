package memoryservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// SemanticPlacementDefaultAssessorCallBudget is deliberately separate from
// the legacy proposal/review budget. A V2.4 placement claim can make one call.
const SemanticPlacementDefaultAssessorCallBudget = 1

var errSemanticAssessmentProviderAttemptConsumed = errors.New("semantic assessment provider attempt already consumed")

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
		return true, errors.Join(err, s.completeTerminal(ctx, *run, item, string(domain.SemanticReviewTerminalFailure), "failed", "candidate_prefetch"))
	}

	assessment, response, reused, providerAttempted, releaseProviderAttempt, err := s.loadOrAssess(ctx, *run, item, request)
	if err != nil {
		if errors.Is(err, errSemanticAssessmentProviderAttemptConsumed) {
			return true, s.completeTerminal(ctx, *run, item, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_attempt_consumed")
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
		ClientProposal:            cloneAssessmentProposal(proposal),
		EntityCandidateGroups:     groups,
		PredicateOptions:          options,
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
		observability.RecordAssessorCall(s.metrics, prepared.InputTokens, 0, time.Since(started).Seconds(), "provider_error")
		return nil, verifier.SemanticAssessmentResponse{}, false, true, true, err
	}
	normalized, validationErrors := verifier.PrepareSemanticAssessmentResponse(prepared, response, s.limits)
	if len(validationErrors) > 0 {
		observability.RecordAssessorCall(s.metrics, prepared.InputTokens, response.OutputTokens, time.Since(started).Seconds(), "invalid_response")
		observability.RecordAssessorValidationFailure(s.metrics, "response")
		return nil, verifier.SemanticAssessmentResponse{}, false, true, true, errors.New("semantic assessor returned invalid complete response")
	}
	observability.RecordAssessorCall(s.metrics, prepared.InputTokens, normalized.OutputTokens, time.Since(started).Seconds(), "ok")
	normalizedJSON, err := json.Marshal(normalized)
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
		InputTokens:               prepared.InputTokens,
		OutputTokens:              normalized.OutputTokens,
		CandidateContextTokens:    prepared.CandidateContextTokens,
		CandidateContextTruncated: prepared.CandidateContextTruncated,
		NormalizedResponse:        normalizedJSON,
		ResponseHash:              semanticAssessmentHash(normalizedJSON),
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
	if semanticAssessmentHash(assessment.NormalizedResponse) != assessment.ResponseHash {
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

func semanticAssessmentRetryPayload(stage string, providerAttempted bool) map[string]any {
	if !providerAttempted {
		return nil
	}
	return map[string]any{
		"assessor_contract":           "dense-mem.v2.4",
		"assessor_provider_attempted": true,
		"failure_stage":               strings.TrimSpace(stage),
	}
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
			"assessor_contract": "dense-mem.v2.4",
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

func (s *semanticAssessmentPlacementWorkerService) prefetchEntityCandidates(
	ctx context.Context,
	run repository.PlacementRun,
	fragment repository.EvidenceFragment,
	proposal map[string]any,
	evidenceID string,
) ([]verifier.SemanticAssessmentEntityCandidateGroup, bool, error) {
	matches, err := s.catalog.ListSemanticAssessmentEntityMatches(ctx, repository.SemanticAssessmentEntityMatchInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		EvidenceText:   fragment.Content,
		Limit:          1000,
	})
	if err != nil {
		return nil, false, fmt.Errorf("load exact entity candidates: %w", err)
	}
	groups := map[string]*verifier.SemanticAssessmentEntityCandidateGroup{}
	truncated := matches.Truncated
	for _, match := range matches.Matches {
		candidate := assessmentEntityCandidate(match.Candidate)
		for _, span := range exactTokenSpans(fragment.Content, match.MatchedName) {
			key := assessmentCandidateGroupKey(evidenceID, span.start, span.end)
			group := groups[key]
			if group == nil {
				group = &verifier.SemanticAssessmentEntityCandidateGroup{
					Surface:    span.surface,
					EvidenceID: evidenceID,
					Start:      span.start,
					End:        span.end,
				}
				groups[key] = group
			}
			addAssessmentEntityCandidate(group, candidate)
		}
	}
	if err := s.addKnownEntityHintCandidates(ctx, run, fragment, proposal, evidenceID, groups); err != nil {
		return nil, false, err
	}
	ordered := make([]verifier.SemanticAssessmentEntityCandidateGroup, 0, len(groups))
	for _, group := range groups {
		if truncated {
			group.CandidateContextTruncated = true
		}
		sort.Slice(group.Candidates, func(left, right int) bool {
			return group.Candidates[left].EntityID < group.Candidates[right].EntityID
		})
		ordered = append(ordered, *group)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Start != ordered[right].Start {
			return ordered[left].Start < ordered[right].Start
		}
		return ordered[left].End < ordered[right].End
	})
	if len(ordered) > verifier.SemanticAssessmentMaxEntityResults {
		ordered = ordered[:verifier.SemanticAssessmentMaxEntityResults]
		truncated = true
		for i := range ordered {
			ordered[i].CandidateContextTruncated = true
		}
	}
	for _, group := range ordered {
		if group.CandidateContextTruncated {
			truncated = true
			break
		}
	}
	return ordered, truncated, nil
}

func (s *semanticAssessmentPlacementWorkerService) addKnownEntityHintCandidates(
	ctx context.Context,
	run repository.PlacementRun,
	fragment repository.EvidenceFragment,
	proposal map[string]any,
	evidenceID string,
	groups map[string]*verifier.SemanticAssessmentEntityCandidateGroup,
) error {
	hints := placementReviewEntityHints(proposal)
	refs := make([]string, 0, len(hints))
	for ref, hint := range hints {
		if strings.TrimSpace(hint.KnownEntityID) != "" {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	for _, ref := range refs {
		hint := hints[ref]
		candidates, err := s.catalog.ListSemanticReviewEntityCandidates(ctx, repository.SemanticReviewEntityCandidateInput{
			TeamID:         run.TeamID,
			OwnerProfileID: run.OwnerProfileID,
			KnownEntityID:  hint.KnownEntityID,
			Limit:          1,
		})
		if err != nil {
			return fmt.Errorf("revalidate known entity %q: %w", hint.KnownEntityID, err)
		}
		if len(candidates) != 1 || candidates[0].EntityID != hint.KnownEntityID || candidates[0].Status != "active" {
			return fmt.Errorf("known entity %q is not a current active candidate", hint.KnownEntityID)
		}
		spans := assessmentHintSpans(fragment, hint)
		if len(spans) == 0 {
			return fmt.Errorf("known entity %q has no exact evidence span", hint.KnownEntityID)
		}
		candidate := assessmentEntityCandidate(candidates[0])
		for _, span := range spans {
			key := assessmentCandidateGroupKey(evidenceID, span.start, span.end)
			group := groups[key]
			if group == nil {
				group = &verifier.SemanticAssessmentEntityCandidateGroup{
					Surface:    span.surface,
					EvidenceID: evidenceID,
					Start:      span.start,
					End:        span.end,
				}
				groups[key] = group
			}
			addAssessmentEntityCandidate(group, candidate)
		}
	}
	return nil
}

func assessmentEntityCandidate(candidate repository.SemanticReviewEntityCandidate) verifier.SemanticAssessmentEntityCandidate {
	context := map[string]any{}
	for key, value := range candidate.IdentityContext {
		context[key] = value
	}
	return verifier.SemanticAssessmentEntityCandidate{
		EntityID:        candidate.EntityID,
		CanonicalName:   candidate.CanonicalName,
		Kind:            candidate.EntityKind,
		IdentityContext: context,
	}
}

func addAssessmentEntityCandidate(
	group *verifier.SemanticAssessmentEntityCandidateGroup,
	candidate verifier.SemanticAssessmentEntityCandidate,
) {
	if group == nil || candidate.EntityID == "" {
		return
	}
	for _, existing := range group.Candidates {
		if existing.EntityID == candidate.EntityID {
			return
		}
	}
	if len(group.Candidates) >= verifier.SemanticAssessmentMaxEntityCandidatesPerSurface {
		group.CandidateContextTruncated = true
		return
	}
	group.Candidates = append(group.Candidates, candidate)
}

type assessmentTextSpan struct {
	start   int
	end     int
	surface string
}

func assessmentHintSpans(fragment repository.EvidenceFragment, hint placementReviewEntityHint) []assessmentTextSpan {
	for _, evidence := range hint.Evidence {
		if evidence.evidenceIndex != fragment.EvidenceIndex {
			continue
		}
		surface, err := verifier.SemanticEvidenceSpan(fragment.Content, evidence.start, evidence.end)
		if err == nil && strings.TrimSpace(surface) != "" {
			return []assessmentTextSpan{{start: evidence.start, end: evidence.end, surface: surface}}
		}
	}
	return exactTokenSpans(fragment.Content, hint.Name)
}

func exactTokenSpans(content, surface string) []assessmentTextSpan {
	surface = strings.TrimSpace(surface)
	if surface == "" {
		return nil
	}
	text := []rune(content)
	needle := []rune(surface)
	if len(needle) == 0 || len(needle) > len(text) {
		return nil
	}
	spans := make([]assessmentTextSpan, 0, 1)
	for start := 0; start+len(needle) <= len(text); start++ {
		if !strings.EqualFold(string(text[start:start+len(needle)]), surface) {
			continue
		}
		end := start + len(needle)
		if !assessmentTokenBoundary(text, start, end) {
			continue
		}
		spans = append(spans, assessmentTextSpan{start: start, end: end, surface: string(text[start:end])})
	}
	return spans
}

func assessmentTokenBoundary(text []rune, start, end int) bool {
	if start > 0 && assessmentTokenRune(text[start-1]) {
		return false
	}
	return end >= len(text) || !assessmentTokenRune(text[end])
}

func assessmentTokenRune(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsNumber(value) || value == '_'
}

func assessmentCandidateGroupKey(evidenceID string, start, end int) string {
	return evidenceID + ":" + strconv.Itoa(start) + ":" + strconv.Itoa(end)
}

func trimSemanticAssessmentCandidateContext(
	req verifier.SemanticAssessmentRequest,
	limits verifier.SemanticAssessmentLimits,
) (verifier.SemanticAssessmentRequest, error) {
	for attempts := 0; attempts < 10000; attempts++ {
		prepared, validationErrors := verifier.PrepareSemanticAssessmentRequest(req, limits)
		if len(validationErrors) == 0 {
			return prepared, nil
		}
		if !semanticAssessmentLimitFailure(validationErrors) || !trimOneSemanticAssessmentCandidate(&req) {
			return verifier.SemanticAssessmentRequest{}, errors.New("semantic assessment request exceeds configured token limits")
		}
	}
	return verifier.SemanticAssessmentRequest{}, errors.New("semantic assessment candidate context could not be bounded")
}

func semanticAssessmentLimitFailure(validationErrors []verifier.SemanticValidationError) bool {
	if len(validationErrors) == 0 {
		return false
	}
	for _, validationError := range validationErrors {
		switch validationError.Field {
		case "candidate_context_tokens", "input_tokens":
		default:
			return false
		}
	}
	return true
}

func trimOneSemanticAssessmentCandidate(req *verifier.SemanticAssessmentRequest) bool {
	if req == nil {
		return false
	}
	if len(req.PredicateOptions) > 0 {
		req.PredicateOptions = req.PredicateOptions[:len(req.PredicateOptions)-1]
		req.CandidateContextTruncated = true
		return true
	}
	for index := len(req.EntityCandidateGroups) - 1; index >= 0; index-- {
		group := &req.EntityCandidateGroups[index]
		if len(group.Candidates) > 0 {
			group.Candidates = group.Candidates[:len(group.Candidates)-1]
			group.CandidateContextTruncated = true
			req.CandidateContextTruncated = true
			return true
		}
	}
	return false
}

type assessmentEntityCommitState struct {
	resolved bool
	group    *verifier.SemanticAssessmentEntityCandidateGroup
}

func semanticAssessmentCommitInput(
	run repository.PlacementRun,
	item repository.PlacementItem,
	fragment repository.EvidenceFragment,
	request verifier.SemanticAssessmentRequest,
	response verifier.SemanticAssessmentResponse,
	assessment *repository.PlacementAssessment,
	policy repository.AutoWriteConfidencePolicy,
) (repository.CommitPlacementSemanticInput, error) {
	if assessment == nil || strings.TrimSpace(assessment.AssessmentID) == "" {
		return repository.CommitPlacementSemanticInput{}, errors.New("persisted assessment is required before semantic commit")
	}
	if policy.Threshold < 0 || policy.Threshold > 1 {
		return repository.CommitPlacementSemanticInput{}, errors.New("effective confidence threshold is invalid")
	}
	groups := assessmentGroupsBySpan(request.EntityCandidateGroups)
	predicates := assessmentPredicatesByKeyVersion(request.PredicateOptions)
	policyVersion := assessmentPolicyVersion(policy)
	threshold := policy.Threshold

	entities, entityStates, err := semanticAssessmentEntityResolutions(response, fragment, assessment.AssessmentID, groups)
	if err != nil {
		return repository.CommitPlacementSemanticInput{}, err
	}
	observations := make([]repository.PlacementRelationshipDecisionInput, 0, len(response.RelationshipResults))
	reviews := make([]repository.PlacementRelationshipReviewInput, 0)
	for _, result := range response.RelationshipResults {
		support, err := semanticAssessmentSupport(fragment, assessment.AssessmentID, result.Evidence)
		if err != nil {
			return repository.CommitPlacementSemanticInput{}, err
		}
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
				assessment.AssessmentID,
				policyVersion,
				assessment.Model,
				assessment.ResponseHash,
				&threshold,
				"not_applicable",
				reviewKind,
				assessmentReviewOptions(reviewKind, result, entityStates, predicates),
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
			ObservationMetadata: map[string]any{
				"semantic_contract": "dense-mem.v2.4",
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
			ReviewOptions:           assessmentReviewOptions(reviewKind, result, entityStates, predicates),
			ReviewGuidance:          assessmentReviewGuidance(reviewKind),
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
			"assessor_contract": "dense-mem.v2.4",
			"assessment_id":     assessment.AssessmentID,
			"response_hash":     assessment.ResponseHash,
			"policy_version":    policyVersion,
			"request_id":        assessment.RequestID,
		},
	}, nil
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
