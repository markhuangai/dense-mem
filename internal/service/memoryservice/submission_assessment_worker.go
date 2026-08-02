package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type SubmissionAssessorProvider interface {
	AssessSubmission(context.Context, verifier.SemanticAssessmentRequest) (verifier.SubmissionAssessmentResponse, error)
	ModelName() string
}

type SubmissionAssessmentWorkerService interface {
	ProcessNextSubmissionAssessment(context.Context) (bool, error)
}

type SubmissionAssessmentWorkerDependencies struct {
	Submissions               repository.SubmissionRepository
	Catalog                   SemanticAssessmentCatalog
	Provider                  SubmissionAssessorProvider
	Limits                    verifier.SemanticAssessmentLimits
	GlobalConfidenceThreshold float64
	TeamID                    string
	WorkerID                  string
	Lease                     time.Duration
	Now                       func() time.Time
	Metrics                   observability.DiscoverabilityMetrics
}

type submissionAssessmentWorkerService struct {
	submissions               repository.SubmissionRepository
	catalog                   SemanticAssessmentCatalog
	provider                  SubmissionAssessorProvider
	limits                    verifier.SemanticAssessmentLimits
	globalConfidenceThreshold float64
	teamID                    string
	workerID                  string
	lease                     time.Duration
	now                       func() time.Time
	metrics                   observability.DiscoverabilityMetrics
}

type submissionAssessmentEntityProposal struct {
	Ref           string
	Name          string
	KnownEntityID string
	Span          submissionProposalSpan
}

type submissionAssessmentRelationshipProposal struct {
	ProposalID    string
	SubjectRef    string
	Predicate     string
	PredicateSpan submissionProposalSpan
	ObjectRef     string
	ObjectValue   map[string]any
	Evidence      []submissionProposalSpan
	Polarity      string
	Modality      string
}

type submissionAssessmentProposal struct {
	Entities      []submissionAssessmentEntityProposal
	Relationships []submissionAssessmentRelationshipProposal
}

func NewSubmissionAssessmentWorkerService(
	deps SubmissionAssessmentWorkerDependencies,
) SubmissionAssessmentWorkerService {
	lease := deps.Lease
	if lease < time.Second {
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
	return &submissionAssessmentWorkerService{
		submissions:               deps.Submissions,
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

func (s *submissionAssessmentWorkerService) ProcessNextSubmissionAssessment(ctx context.Context) (bool, error) {
	if err := s.validateDependencies(); err != nil {
		return false, err
	}
	claim, err := s.submissions.ClaimNextSubmission(ctx, s.teamID, s.workerID, s.lease)
	if err != nil {
		return false, fmt.Errorf("submission assessment worker: claim: %w", err)
	}
	if claim == nil {
		return false, nil
	}
	staged, err := s.submissions.LoadClaimedSubmission(ctx, repository.LoadClaimedSubmissionInput{
		TeamID:         claim.TeamID,
		OwnerProfileID: claim.OwnerProfileID,
		SubmissionID:   claim.SubmissionID,
		WorkerID:       s.workerID,
		Attempts:       claim.Attempts,
	})
	if err != nil {
		return true, errors.Join(errors.New("submission assessment load failed"), s.requeue(ctx, *claim, "submission_load"))
	}

	for _, evidence := range staged.Evidence {
		if _, err := ScanSubmissionEvidence(evidence.Content); err != nil {
			if errors.Is(err, ErrEncodedEvidenceNotAllowed) || errors.Is(err, ErrEvidenceSecurityRejected) {
				return true, s.quarantine(ctx, *claim, staged, "deterministic_security_rejected")
			}
			return true, errors.Join(errors.New("submission deterministic scan failed"), s.requeue(ctx, *claim, "deterministic_scan"))
		}
	}

	request, proposal, err := s.buildRequest(ctx, staged)
	if err != nil {
		if errors.Is(err, errSubmissionAssessmentCatalogUnavailable) {
			return true, errors.Join(errors.New("submission catalog unavailable"), s.requeue(ctx, *claim, "catalog_unavailable"))
		}
		return true, errors.Join(errors.New("submission contract is invalid"), s.reject(ctx, *claim, staged, proposal, "deterministic_submission_contract"))
	}
	assessment, response, err := s.loadOrAssess(ctx, *claim, request)
	if err != nil {
		if errors.Is(err, errStoredSubmissionAssessmentInvalid) {
			return true, errors.Join(errors.New("stored submission assessment is invalid"), s.reject(ctx, *claim, staged, proposal, "stored_assessment_invalid"))
		}
		return true, errors.Join(errors.New("submission assessor failed"), s.requeue(ctx, *claim, "assessor_failure"))
	}
	if response.HasSecurityConcern() {
		return true, s.quarantine(ctx, *claim, staged, "assessor_security_concern")
	}
	if reason := submissionAssessmentRejectionReason(request, proposal, response, s.globalConfidenceThreshold); reason != "" {
		return true, s.reject(ctx, *claim, staged, proposal, reason)
	}
	promotion, err := submissionPromotionInput(*claim, staged, proposal, request, response, assessment, s.globalConfidenceThreshold, s.workerID, s.lease)
	if err != nil {
		return true, errors.Join(errors.New("submission assessment normalization failed"), s.reject(ctx, *claim, staged, proposal, "deterministic_submission_policy"))
	}
	if _, err := s.submissions.PromoteSubmission(ctx, promotion); err != nil {
		return true, errors.Join(errors.New("submission promotion failed"), s.requeue(ctx, *claim, submissionPromotionFailureReason(err)))
	}
	s.recordFirstDisposition(ctx, *claim, string(domain.SubmissionCompleted))
	return true, nil
}

func (s *submissionAssessmentWorkerService) validateDependencies() error {
	if s.submissions == nil {
		return errors.New("submission assessment worker: submission repository is required")
	}
	if s.catalog == nil {
		return errors.New("submission assessment worker: semantic catalog is required")
	}
	if s.provider == nil {
		return errors.New("submission assessment worker: assessor provider is required")
	}
	if _, err := uuid.Parse(s.teamID); err != nil {
		return fmt.Errorf("submission assessment worker: team_id is required: %w", err)
	}
	if s.workerID == "" {
		return errors.New("submission assessment worker: worker_id is required")
	}
	if s.globalConfidenceThreshold < 0 || s.globalConfidenceThreshold > 1 {
		return errors.New("submission assessment worker: global confidence threshold must be between 0 and 1")
	}
	return nil
}

func (s *submissionAssessmentWorkerService) buildRequest(
	ctx context.Context,
	staged *repository.Submission,
) (verifier.SemanticAssessmentRequest, submissionAssessmentProposal, error) {
	_, proposal, err := submissionStageRememberRequest(staged)
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, proposal, err
	}
	groups, truncated, err := s.prefetchEntityCandidates(ctx, staged, proposal)
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, proposal, fmt.Errorf("%w: entity candidates: %w", errSubmissionAssessmentCatalogUnavailable, err)
	}
	queryParts := make([]string, 0, len(staged.Evidence))
	for _, evidence := range staged.Evidence {
		queryParts = append(queryParts, evidence.Content)
	}
	predicates, err := s.catalog.ListSemanticAssessmentPredicateOptions(ctx, repository.SemanticAssessmentPredicateOptionsInput{
		TeamID:         staged.TeamID,
		OwnerProfileID: staged.OwnerProfileID,
		QueryText:      strings.Join(queryParts, "\n"),
		Limit:          verifier.SemanticAssessmentMaxPredicateOptions,
	})
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, proposal, fmt.Errorf("%w: predicate options: %w", errSubmissionAssessmentCatalogUnavailable, err)
	}
	options := make([]verifier.SemanticAssessmentPredicateOption, 0, len(predicates))
	for _, predicate := range predicates {
		if predicate.LifecycleState != string(domain.PredicateLifecycleActive) {
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
	evidence := make([]verifier.SemanticReviewEvidence, 0, len(staged.Evidence))
	for _, item := range staged.Evidence {
		evidence = append(evidence, verifier.SemanticReviewEvidence{
			EvidenceID:    submissionEvidenceID(item.EvidenceIndex),
			EvidenceIndex: item.EvidenceIndex,
			Content:       item.Content,
			Authority:     item.Authority,
		})
	}
	required := make([]verifier.SemanticAssessmentRequiredRelationshipRef, 0, len(proposal.Relationships))
	for _, relationship := range proposal.Relationships {
		spans := make([]verifier.SemanticAssessmentEvidenceSpan, 0, len(relationship.Evidence))
		for _, span := range relationship.Evidence {
			spans = append(spans, verifier.SemanticAssessmentEvidenceSpan{
				EvidenceID: submissionEvidenceID(span.EvidenceIndex),
				Start:      span.Start,
				End:        span.End,
			})
		}
		required = append(required, verifier.SemanticAssessmentRequiredRelationshipRef{
			ProposalID: relationship.ProposalID,
			Evidence:   spans,
		})
	}
	requiredSubmissionProposal := submissionAssessmentRequiredProposal(proposal)
	request := verifier.SemanticAssessmentRequest{
		RequestID:                  "submission-assessment:" + staged.SubmissionID,
		TeamID:                     staged.TeamID,
		OwnerProfileID:             staged.OwnerProfileID,
		Evidence:                   evidence,
		ClientProposal:             cloneAssessmentProposal(staged.Proposal),
		EntityCandidateGroups:      groups,
		PredicateOptions:           options,
		RequiredRelationshipRefs:   required,
		RequiredSubmissionProposal: requiredSubmissionProposal,
		CandidateContextTruncated:  truncated,
	}
	prepared, err := trimSemanticAssessmentCandidateContext(request, s.limits)
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, proposal, err
	}
	return prepared, proposal, nil
}

func (s *submissionAssessmentWorkerService) prefetchEntityCandidates(
	ctx context.Context,
	staged *repository.Submission,
	proposal submissionAssessmentProposal,
) ([]verifier.SemanticAssessmentEntityCandidateGroup, bool, error) {
	groups := make(map[string]*verifier.SemanticAssessmentEntityCandidateGroup, len(proposal.Entities))
	entitiesByEvidence := make(map[int][]submissionAssessmentEntityProposal)
	for _, entity := range proposal.Entities {
		key := assessmentCandidateGroupKey(submissionEvidenceID(entity.Span.EvidenceIndex), entity.Span.Start, entity.Span.End)
		groups[key] = &verifier.SemanticAssessmentEntityCandidateGroup{
			Surface:    entity.Name,
			EvidenceID: submissionEvidenceID(entity.Span.EvidenceIndex),
			Start:      entity.Span.Start,
			End:        entity.Span.End,
		}
		entitiesByEvidence[entity.Span.EvidenceIndex] = append(entitiesByEvidence[entity.Span.EvidenceIndex], entity)
	}
	truncated := false
	for _, evidence := range staged.Evidence {
		entities := entitiesByEvidence[evidence.EvidenceIndex]
		if len(entities) == 0 {
			continue
		}
		matches, err := s.catalog.ListSemanticAssessmentEntityMatches(ctx, repository.SemanticAssessmentEntityMatchInput{
			TeamID:         staged.TeamID,
			OwnerProfileID: staged.OwnerProfileID,
			EvidenceText:   evidence.Content,
			Limit:          1000,
		})
		if err != nil {
			return nil, false, err
		}
		for _, match := range matches.Matches {
			for _, span := range exactTokenSpans(evidence.Content, match.MatchedName) {
				key := assessmentCandidateGroupKey(submissionEvidenceID(evidence.EvidenceIndex), span.start, span.end)
				group := groups[key]
				if group == nil {
					continue
				}
				addAssessmentEntityCandidate(group, submissionAssessmentEntityCandidate(match.Candidate))
			}
		}
		if matches.Truncated {
			truncated = true
			for _, entity := range entities {
				groups[assessmentCandidateGroupKey(submissionEvidenceID(entity.Span.EvidenceIndex), entity.Span.Start, entity.Span.End)].CandidateContextTruncated = true
			}
		}
	}

	knownIDs := make([]string, 0, len(proposal.Entities))
	seenKnownIDs := make(map[string]struct{}, len(proposal.Entities))
	for _, entity := range proposal.Entities {
		if entity.KnownEntityID == "" {
			continue
		}
		if _, exists := seenKnownIDs[entity.KnownEntityID]; exists {
			continue
		}
		seenKnownIDs[entity.KnownEntityID] = struct{}{}
		knownIDs = append(knownIDs, entity.KnownEntityID)
	}
	if len(knownIDs) > 0 {
		known, err := s.catalog.ListSemanticAssessmentKnownEntities(ctx, repository.SemanticAssessmentKnownEntityInput{
			TeamID:         staged.TeamID,
			OwnerProfileID: staged.OwnerProfileID,
			EntityIDs:      knownIDs,
		})
		if err != nil {
			return nil, false, err
		}
		knownByID := make(map[string]repository.SemanticReviewEntityCandidate, len(known))
		for _, candidate := range known {
			if candidate.Status == "active" {
				knownByID[candidate.EntityID] = candidate
			}
		}
		for _, entity := range proposal.Entities {
			candidate, exists := knownByID[entity.KnownEntityID]
			if !exists {
				continue
			}
			group := groups[assessmentCandidateGroupKey(submissionEvidenceID(entity.Span.EvidenceIndex), entity.Span.Start, entity.Span.End)]
			addAssessmentEntityCandidate(group, submissionAssessmentEntityCandidate(candidate))
		}
	}

	ordered := make([]verifier.SemanticAssessmentEntityCandidateGroup, 0, len(groups))
	for _, group := range groups {
		sort.Slice(group.Candidates, func(left, right int) bool {
			return group.Candidates[left].EntityID < group.Candidates[right].EntityID
		})
		if group.CandidateContextTruncated {
			truncated = true
		}
		ordered = append(ordered, *group)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return assessmentCandidateGroupLess(ordered[left], ordered[right])
	})
	return ordered, truncated, nil
}

var (
	errStoredSubmissionAssessmentInvalid      = errors.New("stored submission assessment is invalid")
	errSubmissionAssessmentCatalogUnavailable = errors.New("submission assessment catalog unavailable")
)

func (s *submissionAssessmentWorkerService) loadOrAssess(
	ctx context.Context,
	claim repository.SubmissionClaim,
	request verifier.SemanticAssessmentRequest,
) (*repository.SubmissionAssessment, verifier.SubmissionAssessmentResponse, error) {
	stored, err := s.submissions.LoadSubmissionAssessment(ctx, repository.LoadSubmissionAssessmentInput{
		TeamID:         claim.TeamID,
		OwnerProfileID: claim.OwnerProfileID,
		SubmissionID:   claim.SubmissionID,
	})
	if err == nil {
		response, err := decodeStoredSubmissionAssessment(stored, request, s.limits)
		if err != nil {
			observability.RecordAssessorValidationFailure(s.metrics, "stored_response")
			return nil, verifier.SubmissionAssessmentResponse{}, errStoredSubmissionAssessmentInvalid
		}
		observability.RecordAssessorAssessmentPersistence(s.metrics, "reused")
		observability.RecordAssessorDuplicateRequestPrevention(s.metrics, "post_persist")
		return stored, response, nil
	}
	if !errors.Is(err, repository.ErrSubmissionAssessmentNotFound) {
		return nil, verifier.SubmissionAssessmentResponse{}, err
	}
	if request.CandidateContextTruncated {
		observability.RecordAssessorCandidateTruncation(s.metrics)
	}
	started := time.Now()
	providerCtx := observability.WithMetricIdentity(ctx, claim.TeamID, claim.OwnerProfileID)
	providerCtx = observability.WithAIOperation(providerCtx, observability.AIOperationSubmissionAssessment, len(request.Evidence))
	response, err := s.provider.AssessSubmission(providerCtx, request)
	if err != nil {
		outcome := "provider_error"
		if errors.Is(err, verifier.ErrVerifierMalformedResponse) {
			outcome = "malformed_exhausted"
		}
		observability.RecordAssessorCall(s.metrics, request.InputTokens, 0, time.Since(started).Seconds(), outcome)
		return nil, verifier.SubmissionAssessmentResponse{}, err
	}
	normalized, validationErrors := verifier.PrepareSubmissionAssessmentResponse(request, response, s.limits)
	if len(validationErrors) > 0 {
		observability.RecordAssessorCall(s.metrics, request.InputTokens, response.OutputTokens, time.Since(started).Seconds(), "malformed_exhausted")
		observability.RecordAssessorValidationFailure(s.metrics, "response_contract")
		return nil, verifier.SubmissionAssessmentResponse{}, &verifier.MalformedResponseError{
			Provider:     "submission_assessor",
			Message:      "submission assessor returned an invalid complete response",
			FailureClass: "malformed_response",
			Attempts:     maxSubmissionProviderTurns(response.ProviderTurns),
		}
	}
	normalizedJSON, err := json.Marshal(normalized)
	if err != nil {
		return nil, verifier.SubmissionAssessmentResponse{}, err
	}
	canonicalJSON, err := verifier.CanonicalJSON(normalizedJSON)
	if err != nil {
		return nil, verifier.SubmissionAssessmentResponse{}, err
	}
	inputTokens := normalized.InputTokens
	if inputTokens <= 0 {
		inputTokens = request.InputTokens
	}
	observability.RecordAssessorCall(s.metrics, inputTokens, normalized.OutputTokens, time.Since(started).Seconds(), "ok")
	persisted, existing, err := s.submissions.PersistSubmissionAssessment(ctx, repository.PersistSubmissionAssessmentInput{
		TeamID:                    claim.TeamID,
		OwnerProfileID:            claim.OwnerProfileID,
		SubmissionID:              claim.SubmissionID,
		WorkerID:                  s.workerID,
		ExpectedAttempts:          claim.Attempts,
		RequestID:                 request.RequestID,
		Model:                     s.provider.ModelName(),
		Tokenizer:                 assessmentTokenizer(s.limits),
		InputTokens:               inputTokens,
		OutputTokens:              normalized.OutputTokens,
		CandidateContextTokens:    request.CandidateContextTokens,
		CandidateContextTruncated: request.CandidateContextTruncated,
		NormalizedResponse:        canonicalJSON,
		ResponseHash:              semanticAssessmentHash(canonicalJSON),
		ValidatedAt:               s.now().UTC(),
	})
	if err != nil {
		observability.RecordAssessorAssessmentPersistence(s.metrics, "error")
		return nil, verifier.SubmissionAssessmentResponse{}, err
	}
	if existing {
		observability.RecordAssessorAssessmentPersistence(s.metrics, "reused")
		observability.RecordAssessorDuplicateRequestPrevention(s.metrics, "post_persist")
		storedResponse, err := decodeStoredSubmissionAssessment(persisted, request, s.limits)
		if err != nil {
			return nil, verifier.SubmissionAssessmentResponse{}, errStoredSubmissionAssessmentInvalid
		}
		return persisted, storedResponse, nil
	}
	observability.RecordAssessorAssessmentPersistence(s.metrics, "created")
	return persisted, normalized, nil
}

func maxSubmissionProviderTurns(turns int) int {
	if turns > 0 {
		return turns
	}
	return 1
}

func decodeStoredSubmissionAssessment(
	assessment *repository.SubmissionAssessment,
	request verifier.SemanticAssessmentRequest,
	limits verifier.SemanticAssessmentLimits,
) (verifier.SubmissionAssessmentResponse, error) {
	if assessment == nil {
		return verifier.SubmissionAssessmentResponse{}, errStoredSubmissionAssessmentInvalid
	}
	canonicalJSON, err := verifier.CanonicalJSON(assessment.NormalizedResponse)
	if err != nil || semanticAssessmentHash(canonicalJSON) != assessment.ResponseHash {
		return verifier.SubmissionAssessmentResponse{}, errStoredSubmissionAssessmentInvalid
	}
	response, err := verifier.DecodeSubmissionAssessmentResponseJSON(assessment.NormalizedResponse, limits)
	if err != nil {
		return verifier.SubmissionAssessmentResponse{}, err
	}
	normalized, validationErrors := verifier.PrepareSubmissionAssessmentResponse(request, response, limits)
	if len(validationErrors) > 0 {
		return verifier.SubmissionAssessmentResponse{}, errStoredSubmissionAssessmentInvalid
	}
	return normalized, nil
}

func (s *submissionAssessmentWorkerService) requeue(
	ctx context.Context,
	claim repository.SubmissionClaim,
	reason string,
) error {
	err := s.submissions.RequeueSubmission(ctx, repository.RequeueSubmissionInput{
		TeamID:           claim.TeamID,
		OwnerProfileID:   claim.OwnerProfileID,
		SubmissionID:     claim.SubmissionID,
		WorkerID:         s.workerID,
		ExpectedAttempts: claim.Attempts,
		ReasonCode:       reason,
	})
	if err == nil && claim.Attempts >= claim.MaxAttempts {
		s.recordFirstDisposition(ctx, claim, string(domain.SubmissionFailed))
	}
	return err
}

func (s *submissionAssessmentWorkerService) quarantine(
	ctx context.Context,
	claim repository.SubmissionClaim,
	staged *repository.Submission,
	reason string,
) error {
	err := s.submissions.QuarantineSubmission(ctx, repository.QuarantineSubmissionInput{
		TeamID:           claim.TeamID,
		OwnerProfileID:   claim.OwnerProfileID,
		SubmissionID:     claim.SubmissionID,
		WorkerID:         s.workerID,
		ExpectedAttempts: claim.Attempts,
		ReasonCode:       reason,
		EvidenceOutcomes: submissionEvidenceOutcomes(staged, string(domain.SubmissionQuarantined), reason, string(domain.SearchProjectionNotRequired)),
	})
	if err == nil {
		s.recordFirstDisposition(ctx, claim, string(domain.SubmissionQuarantined))
	}
	return err
}

func (s *submissionAssessmentWorkerService) reject(
	ctx context.Context,
	claim repository.SubmissionClaim,
	staged *repository.Submission,
	proposal submissionAssessmentProposal,
	reason string,
) error {
	err := s.submissions.CompleteSubmission(ctx, repository.CompleteSubmissionInput{
		TeamID:               claim.TeamID,
		OwnerProfileID:       claim.OwnerProfileID,
		SubmissionID:         claim.SubmissionID,
		WorkerID:             s.workerID,
		ExpectedAttempts:     claim.Attempts,
		Status:               string(domain.SubmissionRejected),
		ReasonCode:           reason,
		EvidenceOutcomes:     submissionEvidenceOutcomes(staged, string(domain.SubmissionRejected), reason, string(domain.SearchProjectionNotRequired)),
		RelationshipOutcomes: submissionRejectedRelationshipOutcomes(proposal, reason),
	})
	if err == nil {
		s.recordFirstDisposition(ctx, claim, string(domain.SubmissionRejected))
	}
	return err
}

func (s *submissionAssessmentWorkerService) recordFirstDisposition(
	ctx context.Context,
	claim repository.SubmissionClaim,
	status string,
) {
	if claim.CreatedAt.IsZero() {
		return
	}
	metricCtx := observability.WithMetricIdentity(ctx, claim.TeamID, claim.OwnerProfileID)
	observability.RecordRememberFirstDisposition(metricCtx, s.metrics, s.now().UTC().Sub(claim.CreatedAt.UTC()), status)
}

func submissionEvidenceOutcomes(staged *repository.Submission, status, reason, searchState string) []repository.SubmissionEvidenceStatus {
	if staged == nil {
		return []repository.SubmissionEvidenceStatus{}
	}
	result := make([]repository.SubmissionEvidenceStatus, 0, len(staged.Evidence))
	for _, evidence := range staged.Evidence {
		result = append(result, repository.SubmissionEvidenceStatus{
			EvidenceIndex: evidence.EvidenceIndex,
			Status:        status,
			ReasonCode:    reason,
			SearchState:   searchState,
		})
	}
	return result
}

func submissionRejectedRelationshipOutcomes(proposal submissionAssessmentProposal, reason string) []repository.SubmissionRelationshipOutcome {
	result := make([]repository.SubmissionRelationshipOutcome, 0, len(proposal.Relationships))
	for _, relationship := range proposal.Relationships {
		result = append(result, repository.SubmissionRelationshipOutcome{
			ProposalID: relationship.ProposalID,
			Status:     string(domain.SubmissionRejected),
			ReasonCode: reason,
		})
	}
	return result
}

func submissionAssessmentRejectionReason(
	request verifier.SemanticAssessmentRequest,
	proposal submissionAssessmentProposal,
	response verifier.SubmissionAssessmentResponse,
	threshold float64,
) string {
	entities := make(map[string]submissionAssessmentEntityProposal, len(proposal.Entities))
	for _, entity := range proposal.Entities {
		entities[entity.Ref] = entity
	}
	if len(response.EntityResults) != len(entities) {
		return "assessor_entity_correspondence_rejected"
	}
	entityResults := make(map[string]verifier.SemanticAssessmentEntityResult, len(response.EntityResults))
	for _, result := range response.EntityResults {
		expected, exists := entities[result.Ref]
		if !exists || result.EvidenceID != submissionEvidenceID(expected.Span.EvidenceIndex) || result.Start != expected.Span.Start || result.End != expected.Span.End || result.Surface != expected.Name {
			return "assessor_entity_correspondence_rejected"
		}
		if result.Action == string(domain.EntityResolutionAmbiguous) || result.Confidence < threshold {
			return "assessor_entity_confidence_rejected"
		}
		entityResults[result.Ref] = result
	}
	relationships := make(map[string]submissionAssessmentRelationshipProposal, len(proposal.Relationships))
	for _, relationship := range proposal.Relationships {
		relationships[relationship.ProposalID] = relationship
	}
	if len(response.RelationshipResults) != len(relationships) {
		return "assessor_relationship_correspondence_rejected"
	}
	predicates := assessmentPredicatesByKeyVersion(request.PredicateOptions)
	states := make(map[string]assessmentEntityCommitState, len(entityResults))
	for ref, result := range entityResults {
		states[ref] = assessmentEntityCommitState{resolved: true, kind: result.Kind}
	}
	for _, submitted := range response.RelationshipResults {
		result := submitted.SemanticAssessmentRelationshipResult
		expected, exists := relationships[result.Ref]
		if !exists || result.SubjectRef != expected.SubjectRef || result.OriginalPredicate != expected.Predicate ||
			!submissionRelationshipObjectMatches(expected, result) || !submissionRelationshipSpansMatch(expected.Evidence, result.Evidence) ||
			result.Polarity != expected.Polarity || result.Modality != expected.Modality {
			return "assessor_relationship_correspondence_rejected"
		}
		if result.EvidenceVerdict != string(domain.VerificationEntailed) || result.Confidence < threshold ||
			result.ScopeStatus == "needs_review" || result.TemporalVerdict == "ambiguous" || result.TemporalVerdict == "contradicted" {
			return "assessor_semantic_rejected"
		}
		switch result.PredicateStatus {
		case "resolved":
			if result.PredicateKey == nil || result.PredicateVersion == nil {
				return "assessor_predicate_rejected"
			}
			predicate, exists := predicates[assessmentPredicateKey(*result.PredicateKey, *result.PredicateVersion)]
			if !exists || !semanticAssessmentPredicateAllowsEndpoints(result, states, predicate) {
				return "assessor_predicate_rejected"
			}
		case "needs_review":
			if submitted.PredicateCandidate == nil {
				return "assessor_predicate_rejected"
			}
		default:
			return "assessor_predicate_rejected"
		}
	}
	return ""
}

func submissionRelationshipSpansMatch(
	expected []submissionProposalSpan,
	actual []verifier.SemanticAssessmentEvidenceSpan,
) bool {
	if len(expected) != len(actual) {
		return false
	}
	seen := make(map[string]struct{}, len(expected))
	for _, span := range expected {
		seen[assessmentCandidateGroupKey(submissionEvidenceID(span.EvidenceIndex), span.Start, span.End)] = struct{}{}
	}
	for _, span := range actual {
		key := assessmentCandidateGroupKey(span.EvidenceID, span.Start, span.End)
		if _, exists := seen[key]; !exists {
			return false
		}
		delete(seen, key)
	}
	return len(seen) == 0
}

func submissionPromotionInput(
	claim repository.SubmissionClaim,
	staged *repository.Submission,
	proposal submissionAssessmentProposal,
	request verifier.SemanticAssessmentRequest,
	response verifier.SubmissionAssessmentResponse,
	assessment *repository.SubmissionAssessment,
	threshold float64,
	workerID string,
	lease time.Duration,
) (repository.PromoteSubmissionInput, error) {
	if staged == nil || assessment == nil || assessment.AssessmentID == "" {
		return repository.PromoteSubmissionInput{}, errors.New("staged submission and assessment are required")
	}
	canonical, fragments, err := submissionCanonicalIngest(staged)
	if err != nil {
		return repository.PromoteSubmissionInput{}, err
	}
	resolutions := make([]repository.PlacementEntityResolutionInput, 0, len(response.EntityResults))
	for _, result := range response.EntityResults {
		fragment, exists := fragments[result.EvidenceID]
		if !exists {
			return repository.PromoteSubmissionInput{}, errors.New("entity result references unknown evidence")
		}
		start, end := result.Start, result.End
		resolution := repository.PlacementEntityResolutionInput{
			MentionRef:    result.Ref,
			Action:        result.Action,
			EntityKind:    result.Kind,
			CanonicalName: result.Surface,
			FragmentID:    fragment.FragmentID,
			SpanStart:     &start,
			SpanEnd:       &end,
			VerifierResult: map[string]any{
				"confidence": result.Confidence,
				"rationale":  result.Rationale,
				"action":     result.Action,
			},
			Metadata: map[string]any{
				"semantic_contract": domain.ContractVersion,
				"submission_id":     staged.SubmissionID,
			},
			SubmissionAssessmentID: assessment.AssessmentID,
		}
		if result.Action == string(domain.EntityResolutionReuse) {
			if result.CandidateEntityID == nil {
				return repository.PromoteSubmissionInput{}, errors.New("reuse result has no candidate")
			}
			resolution.EntityID = *result.CandidateEntityID
		} else if result.Action == string(domain.EntityResolutionCreate) {
			resolution.IdentityContext = map[string]any{"surface": result.Surface, "source": "submission_assessment"}
		} else {
			return repository.PromoteSubmissionInput{}, errors.New("unresolved entity result cannot be promoted")
		}
		resolutions = append(resolutions, resolution)
	}

	predicates := assessmentPredicatesByKeyVersion(request.PredicateOptions)
	observations := make([]repository.PlacementRelationshipDecisionInput, 0, len(response.RelationshipResults))
	for _, submitted := range response.RelationshipResults {
		result := submitted.SemanticAssessmentRelationshipResult
		supports, err := submissionAssessmentSupport(fragments, assessment.AssessmentID, result.Evidence)
		if err != nil {
			return repository.PromoteSubmissionInput{}, err
		}
		support, additional := semanticAssessmentPrimarySupport(supports)
		objectRef, objectValue, err := semanticAssessmentObject(result)
		if err != nil {
			return repository.PromoteSubmissionInput{}, err
		}
		validFrom, validTo, err := semanticAssessmentValidity(result)
		if err != nil {
			return repository.PromoteSubmissionInput{}, err
		}
		predicateKey := ""
		predicateVersion := 0
		var candidate *repository.PlacementPredicateCandidateInput
		switch result.PredicateStatus {
		case "resolved":
			if result.PredicateKey == nil || result.PredicateVersion == nil {
				return repository.PromoteSubmissionInput{}, errors.New("resolved predicate is incomplete")
			}
			predicateKey = *result.PredicateKey
			predicateVersion = *result.PredicateVersion
			_, exists := predicates[assessmentPredicateKey(predicateKey, predicateVersion)]
			if !exists {
				return repository.PromoteSubmissionInput{}, errors.New("resolved predicate was not supplied")
			}
		case "needs_review":
			if submitted.PredicateCandidate == nil {
				return repository.PromoteSubmissionInput{}, errors.New("novel predicate candidate is required")
			}
			predicateKey = submitted.PredicateCandidate.PredicateKey
			predicateVersion = 1
			candidate = &repository.PlacementPredicateCandidateInput{
				PredicateKey:                predicateKey,
				PredicateVersion:            predicateVersion,
				RelationshipKind:            submitted.PredicateCandidate.RelationshipKind,
				RegisterSubmissionPredicate: true,
			}
		default:
			return repository.PromoteSubmissionInput{}, errors.New("unsupported predicate status")
		}
		confidence := result.Confidence
		observationMetadata := map[string]any{
			"semantic_contract": domain.ContractVersion,
			"assessment_id":     assessment.AssessmentID,
			"submission_id":     staged.SubmissionID,
			"modality":          result.Modality,
			"scope_status":      result.ScopeStatus,
			"temporal_verdict":  result.TemporalVerdict,
		}
		if submitted.PredicateCandidate != nil {
			observationMetadata["assessor_predicate_candidate_key"] = submitted.PredicateCandidate.PredicateKey
		}
		observations = append(observations, repository.PlacementRelationshipDecisionInput{
			Ref:                 result.Ref,
			SubjectRef:          result.SubjectRef,
			OriginalPredicate:   result.OriginalPredicate,
			PredicateKey:        predicateKey,
			PredicateVersion:    predicateVersion,
			PredicateCandidate:  candidate,
			ObjectRef:           objectRef,
			ObjectValue:         objectValue,
			Polarity:            result.Polarity,
			ScopeKey:            semanticAssessmentScopeKey(result),
			ValidFrom:           validFrom,
			ValidTo:             validTo,
			EvidenceVerdict:     result.EvidenceVerdict,
			Confidence:          &confidence,
			Rationale:           result.Rationale,
			Model:               assessment.Model,
			ResponseHash:        assessment.ResponseHash,
			Support:             support,
			Supports:            additional,
			ObservationMetadata: observationMetadata,
			RelationshipMetadata: map[string]any{
				"assessment_response_hash": assessment.ResponseHash,
			},
			SubmissionAssessmentID:  assessment.AssessmentID,
			AssessmentPolicyVersion: repository.AssessmentPolicyVersion + ":submission",
			ThresholdUsed:           &threshold,
			GateResult:              "meets_write_threshold",
		})
	}
	commits := make([]repository.CommitPlacementSemanticInput, 0, len(canonical.Evidence))
	for index, evidence := range canonical.Evidence {
		commit := repository.CommitPlacementSemanticInput{
			TeamID:           staged.TeamID,
			OwnerProfileID:   staged.OwnerProfileID,
			IngestID:         canonical.IngestID,
			PlacementRunID:   canonical.PlacementRunID,
			PlacementItemID:  evidence.PlacementItemID,
			WorkerID:         workerID,
			ExpectedAttempts: claim.Attempts,
			OutcomeKind:      "submission_assessment_commit",
			Status:           string(domain.SemanticReviewAccepted),
			Category:         "validated_claim",
			Payload: map[string]any{
				"assessor_contract": domain.ContractVersion,
				"assessment_id":     assessment.AssessmentID,
				"response_hash":     assessment.ResponseHash,
				"request_id":        assessment.RequestID,
				"submission_id":     staged.SubmissionID,
			},
		}
		if index == 0 {
			commit.EntityResolutions = resolutions
			commit.RelationshipObservations = observations
		}
		commits = append(commits, commit)
	}
	return repository.PromoteSubmissionInput{
		TeamID:                          staged.TeamID,
		OwnerProfileID:                  staged.OwnerProfileID,
		SubmissionID:                    staged.SubmissionID,
		WorkerID:                        workerID,
		ExpectedAttempts:                claim.Attempts,
		Lease:                           lease,
		Canonical:                       canonical,
		Commits:                         commits,
		EvidenceOutcomes:                submissionEvidenceOutcomes(staged, "accepted", "assessed_and_promoted", string(domain.SearchProjectionPending)),
		ReplacesQuarantinedSubmissionID: staged.ReplacesQuarantinedSubmissionID,
	}, nil
}

func submissionCanonicalIngest(staged *repository.Submission) (repository.CreateIngestInput, map[string]repository.EvidenceFragment, error) {
	remember, _, err := submissionStageRememberRequest(staged)
	if err != nil {
		return repository.CreateIngestInput{}, nil, err
	}
	contentHashes := sourceRevisionContentHashes(remember.Evidence)
	canonical := repository.CreateIngestInput{
		IngestID:          uuid.NewString(),
		PlacementRunID:    uuid.NewString(),
		TeamID:            staged.TeamID,
		OwnerProfileID:    staged.OwnerProfileID,
		IdempotencyKey:    "submission:" + staged.SubmissionID,
		RequestHash:       staged.RequestHash,
		SourceSummary:     staged.SourceSummary,
		Status:            string(domain.PlacementRunProcessing),
		TelemetryRemember: true,
		Proposal:          cloneAssessmentProposal(staged.Proposal),
		Metadata: map[string]any{
			"contract_version": domain.ContractVersion,
			"submission_id":    staged.SubmissionID,
			"actor": map[string]any{
				"team_id":        staged.TeamID,
				"profile_id":     staged.OwnerProfileID,
				"credential_id":  staged.ActorCredentialID,
				"auth_method":    staged.ActorAuthMethod,
				"role":           staged.ActorRole,
				"scopes":         append([]string(nil), staged.ActorScopes...),
				"correlation_id": staged.CorrelationID,
			},
		},
		Evidence: make([]repository.EvidenceInput, 0, len(remember.Evidence)),
	}
	fragments := make(map[string]repository.EvidenceFragment, len(remember.Evidence))
	for index, item := range remember.Evidence {
		authority, metadata := ledgerAuthorityAndMetadata(item.Authority, item.Metadata)
		metadata = evidenceProcessingIntentMetadata(metadata, item)
		fragmentID := uuid.NewString()
		canonical.Evidence = append(canonical.Evidence, repository.EvidenceInput{
			FragmentID:                    fragmentID,
			PlacementItemID:               uuid.NewString(),
			Content:                       item.Content,
			ContentHash:                   staged.Evidence[index].ContentHash,
			SourceType:                    evidenceSourceType(item.SourceType),
			Authority:                     authority,
			SourceRef:                     strings.TrimSpace(item.Source),
			SourceKey:                     strings.TrimSpace(item.SourceKey),
			SourceRevisionToken:           strings.TrimSpace(item.SourceRevision),
			ExpectedPreviousRevisionToken: strings.TrimSpace(item.PreviousSourceRevision),
			SourceRevisionContentHash:     contentHashes[sourceRevisionBatchKey(item)],
			SourceRevisionEnvelope:        sourceRevisionEnvelope(item),
			SupersedesEvidenceIDs:         append([]string(nil), item.SupersedesEvidenceIDs...),
			IdempotencyKey:                strings.TrimSpace(item.IdempotencyKey),
			Labels:                        append([]string(nil), item.Labels...),
			Metadata:                      metadata,
		})
		fragments[submissionEvidenceID(index)] = repository.EvidenceFragment{
			FragmentID:    fragmentID,
			EvidenceIndex: index,
			Content:       item.Content,
			ContentHash:   staged.Evidence[index].ContentHash,
			Authority:     authority,
		}
	}
	return canonical, fragments, nil
}

func submissionAssessmentSupport(
	fragments map[string]repository.EvidenceFragment,
	assessmentID string,
	spans []verifier.SemanticAssessmentEvidenceSpan,
) ([]repository.EvidenceSupportInput, error) {
	if len(spans) == 0 {
		return nil, errors.New("submission relationship has no evidence support")
	}
	result := make([]repository.EvidenceSupportInput, 0, len(spans))
	for _, span := range spans {
		fragment, exists := fragments[span.EvidenceID]
		if !exists {
			return nil, errors.New("submission support references unknown evidence")
		}
		authority, err := semanticSupportAuthority(fragment.Authority)
		if err != nil {
			return nil, err
		}
		quote, err := verifier.SemanticEvidenceSpan(fragment.Content, span.Start, span.End)
		if err != nil {
			return nil, err
		}
		result = append(result, repository.EvidenceSupportInput{
			FragmentID:     fragment.FragmentID,
			SourceGroupKey: fmt.Sprintf("submission_assessment:%s:%s:%d:%d", assessmentID, span.EvidenceID, span.Start, span.End),
			SpanStart:      span.Start,
			SpanEnd:        span.End,
			Quote:          quote,
			Authority:      authority,
			Metadata: map[string]any{
				"semantic_contract": domain.ContractVersion,
				"assessment_id":     assessmentID,
				"evidence_id":       span.EvidenceID,
			},
		})
	}
	return result, nil
}
