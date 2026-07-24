package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

var ErrSemanticCommitNotAccepted = errors.New("semantic commit requires an accepted review result")

type SemanticCommitService interface {
	CommitSemantic(ctx context.Context, job SemanticCommitJob) (*repository.V2CommitPlacementSemanticResult, error)
	CompleteSemanticPlacement(ctx context.Context, job SemanticCommitJob) (*SemanticPlacementCompletionResult, error)
}

type SemanticCommitDependencies struct {
	PlacementCommit repository.V2PlacementCommitRepository
}

type semanticCommitService struct {
	placementCommit repository.V2PlacementCommitRepository
}

type SemanticCommitJob struct {
	TeamID           string
	OwnerProfileID   string
	IngestID         string
	PlacementRunID   string
	PlacementItemID  string
	WorkerID         string
	ExpectedAttempts int
	MaxAttempts      int
	Request          verifier.V2SemanticReviewRequest
	Result           SemanticReviewResult
	ReviewModel      string
	PromoteToFact    bool
}

type SemanticPlacementCompletionResult struct {
	Status         string
	SemanticCommit *repository.V2CommitPlacementSemanticResult
	Terminal       *repository.V2CompletePlacementReviewResult
}

func NewSemanticCommitService(deps SemanticCommitDependencies) SemanticCommitService {
	return &semanticCommitService{placementCommit: deps.PlacementCommit}
}

func (s *semanticCommitService) CommitSemantic(
	ctx context.Context,
	job SemanticCommitJob,
) (*repository.V2CommitPlacementSemanticResult, error) {
	if s.placementCommit == nil {
		return nil, errors.New("semantic commit: placement commit repository is required")
	}
	job = normalizeSemanticCommitJob(job)
	if job.Result.Status != string(domain.V2SemanticReviewAccepted) {
		return nil, ErrSemanticCommitNotAccepted
	}
	input, err := semanticCommitInputFromReview(job)
	if err != nil {
		return nil, err
	}
	return s.placementCommit.CommitPlacementSemanticResult(ctx, input)
}

func (s *semanticCommitService) CompleteSemanticPlacement(
	ctx context.Context,
	job SemanticCommitJob,
) (*SemanticPlacementCompletionResult, error) {
	if s.placementCommit == nil {
		return nil, errors.New("semantic commit: placement commit repository is required")
	}
	job = normalizeSemanticCommitJob(job)
	if job.Result.Status == string(domain.V2SemanticReviewAccepted) {
		committed, err := s.CommitSemantic(ctx, job)
		if err != nil {
			return nil, err
		}
		return &SemanticPlacementCompletionResult{Status: committed.Status, SemanticCommit: committed}, nil
	}
	if job.Result.Status == string(domain.V2SemanticReviewRetryable) {
		if semanticRetryAttemptsExhausted(job) {
			exhausted := exhaustedRetryableSemanticCommitJob(job)
			input, err := v2TerminalReviewInputFromResult(exhausted)
			if err != nil {
				return nil, err
			}
			terminal, err := s.placementCommit.CompletePlacementReviewResult(ctx, input)
			if err != nil {
				return nil, err
			}
			return &SemanticPlacementCompletionResult{Status: terminal.Status, Terminal: terminal}, nil
		}
		input, err := v2RetryableReviewInputFromResult(job)
		if err != nil {
			return nil, err
		}
		requeued, err := s.placementCommit.RequeuePlacementReviewResult(ctx, input)
		if err != nil {
			return nil, err
		}
		return &SemanticPlacementCompletionResult{Status: requeued.Status}, nil
	}
	input, err := v2TerminalReviewInputFromResult(job)
	if err != nil {
		return nil, err
	}
	terminal, err := s.placementCommit.CompletePlacementReviewResult(ctx, input)
	if err != nil {
		return nil, err
	}
	return &SemanticPlacementCompletionResult{Status: terminal.Status, Terminal: terminal}, nil
}

func normalizeSemanticCommitJob(job SemanticCommitJob) SemanticCommitJob {
	job.TeamID = strings.TrimSpace(job.TeamID)
	job.OwnerProfileID = strings.TrimSpace(job.OwnerProfileID)
	job.IngestID = strings.TrimSpace(job.IngestID)
	job.PlacementRunID = strings.TrimSpace(job.PlacementRunID)
	job.PlacementItemID = strings.TrimSpace(job.PlacementItemID)
	job.WorkerID = strings.TrimSpace(job.WorkerID)
	job.ReviewModel = strings.TrimSpace(job.ReviewModel)
	job.Request.TeamID = strings.TrimSpace(job.Request.TeamID)
	if job.Request.TeamID == "" {
		job.Request.TeamID = job.TeamID
	}
	job.Request.OwnerProfileID = strings.TrimSpace(job.Request.OwnerProfileID)
	if job.Request.OwnerProfileID == "" {
		job.Request.OwnerProfileID = job.OwnerProfileID
	}
	job.Result.Status = strings.TrimSpace(job.Result.Status)
	job.Result.ResponseHash = strings.TrimSpace(job.Result.ResponseHash)
	return job
}

func semanticRetryAttemptsExhausted(job SemanticCommitJob) bool {
	return job.MaxAttempts > 0 && job.ExpectedAttempts >= job.MaxAttempts
}

func exhaustedRetryableSemanticCommitJob(job SemanticCommitJob) SemanticCommitJob {
	job.Result.Status = string(domain.V2SemanticReviewTerminalFailure)
	job.Result.FailureStage = semanticFailureStageOrDefault(job.Result.FailureStage, semanticFailureStageUnknown)
	job.Result.FailureClass = semanticFailureClassOrDefault(job.Result.FailureClass, semanticFailureClassUnknown)
	job.Result.RetryableExhausted = true
	job.Result.ValidationErrors = append(job.Result.ValidationErrors, verifier.V2SemanticValidationError{
		Field:   "placement_attempts",
		Message: "retryable semantic review exhausted placement attempts",
	})
	return job
}

func v2RetryableReviewInputFromResult(job SemanticCommitJob) (repository.V2RequeuePlacementReviewInput, error) {
	if job.Result.Status != string(domain.V2SemanticReviewRetryable) {
		return repository.V2RequeuePlacementReviewInput{}, fmt.Errorf("semantic commit: unsupported retryable status %q", job.Result.Status)
	}
	return repository.V2RequeuePlacementReviewInput{
		TeamID:           job.TeamID,
		OwnerProfileID:   job.OwnerProfileID,
		IngestID:         job.IngestID,
		PlacementRunID:   job.PlacementRunID,
		PlacementItemID:  job.PlacementItemID,
		WorkerID:         job.WorkerID,
		ExpectedAttempts: job.ExpectedAttempts,
	}, nil
}

func v2TerminalReviewInputFromResult(job SemanticCommitJob) (repository.V2CompletePlacementReviewInput, error) {
	switch job.Result.Status {
	case string(domain.V2SemanticReviewReviewRequired),
		string(domain.V2SemanticReviewRejected),
		string(domain.V2SemanticReviewQuarantined),
		string(domain.V2SemanticReviewTerminalFailure):
	default:
		return repository.V2CompletePlacementReviewInput{}, fmt.Errorf("semantic commit: unsupported terminal status %q", job.Result.Status)
	}
	category := "candidate"
	if job.Result.Status == string(domain.V2SemanticReviewQuarantined) {
		category = "quarantined"
	}
	if job.Result.Status == string(domain.V2SemanticReviewTerminalFailure) {
		category = "failed"
	}
	payload := map[string]any{
		"request_id":         job.Request.RequestID,
		"response_hash":      job.Result.ResponseHash,
		"review_model":       job.ReviewModel,
		"placement_attempts": job.ExpectedAttempts,
		"max_attempts":       job.MaxAttempts,
		"review_outcome_ids": append([]string(nil), job.Result.OutcomeIDs...),
		"validation_errors":  semanticValidationMessages(job.Result.ValidationErrors),
	}
	if job.Result.FailureStage != "" {
		payload["failure_stage"] = job.Result.FailureStage
	}
	if job.Result.FailureClass != "" {
		payload["failure_class"] = job.Result.FailureClass
	}
	if job.Result.RetryableExhausted {
		payload["retryable_exhausted"] = true
	}
	return repository.V2CompletePlacementReviewInput{
		TeamID:           job.TeamID,
		OwnerProfileID:   job.OwnerProfileID,
		IngestID:         job.IngestID,
		PlacementRunID:   job.PlacementRunID,
		PlacementItemID:  job.PlacementItemID,
		WorkerID:         job.WorkerID,
		ExpectedAttempts: job.ExpectedAttempts,
		Status:           job.Result.Status,
		Category:         category,
		Payload:          payload,
	}, nil
}

func semanticCommitInputFromReview(job SemanticCommitJob) (repository.V2CommitPlacementSemanticInput, error) {
	mentionsByRef := make(map[string]verifier.V2SemanticEntityMention, len(job.Request.EntityMentions))
	for _, mention := range job.Request.EntityMentions {
		mentionsByRef[mention.Ref] = mention
	}
	evidenceByID := make(map[string]verifier.V2SemanticReviewEvidence, len(job.Request.Evidence))
	for _, evidence := range job.Request.Evidence {
		evidenceByID[evidence.EvidenceID] = evidence
	}
	observationsByRef := make(map[string]verifier.V2SemanticRelationshipObservation, len(job.Request.RelationshipObservations))
	for _, observation := range job.Request.RelationshipObservations {
		observationsByRef[observation.Ref] = observation
	}

	entities := make([]repository.V2PlacementEntityResolutionInput, 0, len(job.Result.EntityResults))
	for _, result := range job.Result.EntityResults {
		mention, ok := mentionsByRef[result.Ref]
		if !ok {
			return repository.V2CommitPlacementSemanticInput{}, fmt.Errorf("semantic commit: unknown entity result ref %q", result.Ref)
		}
		resolution := repository.V2PlacementEntityResolutionInput{
			MentionRef:      result.Ref,
			Action:          result.Action,
			EntityKind:      mention.Kind,
			CanonicalName:   mention.Surface,
			SpanStart:       &mention.Start,
			SpanEnd:         &mention.End,
			IdentityContext: mention.IdentityContext,
			VerifierResult: map[string]any{
				"confidence": result.Confidence,
			},
		}
		if evidence, ok := evidenceByID[mention.EvidenceID]; ok {
			resolution.FragmentID = evidence.FragmentID
		}
		if result.CandidateEntityID != nil {
			resolution.EntityID = *result.CandidateEntityID
		}
		entities = append(entities, resolution)
	}

	relationships := make([]repository.V2PlacementRelationshipDecisionInput, 0, len(job.Result.RelationshipResults))
	relationshipReviews := make([]repository.V2PlacementRelationshipReviewInput, 0)
	for _, result := range job.Result.RelationshipResults {
		observation, ok := observationsByRef[result.Ref]
		if !ok {
			return repository.V2CommitPlacementSemanticInput{}, fmt.Errorf("semantic commit: unknown relationship result ref %q", result.Ref)
		}
		if result.PredicateStatus != "resolved" || result.PredicateKey == nil {
			confidence := result.Confidence
			support, err := v2SemanticPlacementSupport(observation, evidenceByID)
			if err != nil {
				return repository.V2CommitPlacementSemanticInput{}, err
			}
			review := repository.V2PlacementRelationshipReviewInput{
				Ref:               result.Ref,
				SubjectRef:        observation.SubjectRef,
				OriginalPredicate: observation.OriginalPredicate,
				ObjectRef:         observation.ObjectRef,
				ObjectValue:       nil,
				Polarity:          observation.Polarity,
				EvidenceVerdict:   result.EvidenceVerdict,
				Confidence:        &confidence,
				Rationale:         result.Rationale,
				Model:             job.ReviewModel,
				ResponseHash:      job.Result.ResponseHash,
				Support:           support,
				Reason:            "predicate_needs_review",
				Payload: map[string]any{
					"predicate_policy_version": domain.V2PredicatePolicyVersion,
				},
			}
			if observation.ObjectValue != nil {
				review.ObjectValue = &repository.V2PlacementValueInput{
					Ref:            observation.ObjectValue.Ref,
					ValueType:      observation.ObjectValue.Type,
					CanonicalValue: observation.ObjectValue.Value,
					Display:        observation.ObjectValue.Display,
					Unit:           observation.ObjectValue.Unit,
				}
				review.ObjectRef = ""
			}
			relationshipReviews = append(relationshipReviews, review)
			continue
		}
		predicateCandidate, err := v2SemanticSelectedPredicateCandidate(observation, *result.PredicateKey)
		if err != nil {
			return repository.V2CommitPlacementSemanticInput{}, err
		}
		relationship := repository.V2PlacementRelationshipDecisionInput{
			Ref:               result.Ref,
			SubjectRef:        observation.SubjectRef,
			OriginalPredicate: observation.OriginalPredicate,
			PredicateKey:      *result.PredicateKey,
			PredicateVersion:  predicateCandidate.Version,
			PredicateCandidate: &repository.V2PlacementPredicateCandidateInput{
				PredicateKey:     predicateCandidate.PredicateKey,
				PredicateVersion: predicateCandidate.Version,
				RelationshipKind: predicateCandidate.RelationshipKind,
			},
			ObjectRef:       observation.ObjectRef,
			Polarity:        observation.Polarity,
			ValidFrom:       observation.ValidFrom,
			ValidTo:         observation.ValidTo,
			EvidenceVerdict: result.EvidenceVerdict,
			PromoteToFact:   job.PromoteToFact,
			Confidence:      &result.Confidence,
			Rationale:       result.Rationale,
			Model:           job.ReviewModel,
			ResponseHash:    job.Result.ResponseHash,
			ObservationMetadata: map[string]any{
				"request_id": job.Request.RequestID,
				"review_ref": result.Ref,
			},
			RelationshipMetadata: map[string]any{
				"semantic_review_response_hash": job.Result.ResponseHash,
			},
		}
		if observation.CorrectionTarget != nil {
			relationship.CorrectionTarget = &repository.V2PlacementCorrectionTargetInput{
				RelationshipID:  observation.CorrectionTarget.RelationshipID,
				ExpectedVersion: observation.CorrectionTarget.ExpectedVersion,
			}
		}
		if observation.ObjectValue != nil {
			relationship.ObjectValue = &repository.V2PlacementValueInput{
				Ref:            observation.ObjectValue.Ref,
				ValueType:      observation.ObjectValue.Type,
				CanonicalValue: observation.ObjectValue.Value,
				Display:        observation.ObjectValue.Display,
				Unit:           observation.ObjectValue.Unit,
			}
			relationship.ObjectRef = ""
		}
		relationship.Support, err = v2SemanticPlacementSupport(observation, evidenceByID)
		if err != nil {
			return repository.V2CommitPlacementSemanticInput{}, err
		}
		relationships = append(relationships, relationship)
	}

	category := "validated_claim"
	if job.PromoteToFact {
		category = "fact"
	}
	return repository.V2CommitPlacementSemanticInput{
		TeamID:                   job.TeamID,
		OwnerProfileID:           job.OwnerProfileID,
		IngestID:                 job.IngestID,
		PlacementRunID:           job.PlacementRunID,
		PlacementItemID:          job.PlacementItemID,
		WorkerID:                 job.WorkerID,
		ExpectedAttempts:         job.ExpectedAttempts,
		Status:                   job.Result.Status,
		Category:                 category,
		EntityResolutions:        entities,
		RelationshipObservations: relationships,
		RelationshipReviews:      relationshipReviews,
		Payload: map[string]any{
			"request_id":         job.Request.RequestID,
			"response_hash":      job.Result.ResponseHash,
			"review_model":       job.ReviewModel,
			"review_outcome_ids": append([]string(nil), job.Result.OutcomeIDs...),
		},
	}, nil
}

func v2SemanticPlacementSupport(
	observation verifier.V2SemanticRelationshipObservation,
	evidenceByID map[string]verifier.V2SemanticReviewEvidence,
) (*repository.V2EvidenceSupportInput, error) {
	evidence, ok := evidenceByID[observation.EvidenceID]
	if !ok {
		return nil, fmt.Errorf(
			"v2 semantic commit: unknown relationship evidence_id %q",
			observation.EvidenceID,
		)
	}
	return &repository.V2EvidenceSupportInput{
		FragmentID:       evidence.FragmentID,
		SourceGroupKey:   "semantic_review:" + evidence.EvidenceID,
		SourceID:         evidence.SourceID,
		SourceRevisionID: evidence.SourceRevisionID,
		SpanStart:        observation.Start,
		SpanEnd:          observation.End,
		Quote:            observation.Quote,
		Authority:        string(domain.AuthorityPrimary),
	}, nil
}

func v2SemanticSelectedPredicateCandidate(
	observation verifier.V2SemanticRelationshipObservation,
	predicateKey string,
) (verifier.V2SemanticPredicateCandidate, error) {
	predicateKey = strings.TrimSpace(predicateKey)
	var selected *verifier.V2SemanticPredicateCandidate
	for i := range observation.PredicateCandidates {
		candidate := observation.PredicateCandidates[i]
		if strings.TrimSpace(candidate.PredicateKey) != predicateKey {
			continue
		}
		if selected != nil && (selected.Version != candidate.Version ||
			selected.RelationshipKind != candidate.RelationshipKind) {
			return verifier.V2SemanticPredicateCandidate{}, fmt.Errorf(
				"v2 semantic commit: predicate %q has ambiguous candidate definitions",
				predicateKey,
			)
		}
		selected = &candidate
	}
	if selected == nil {
		return verifier.V2SemanticPredicateCandidate{}, fmt.Errorf(
			"v2 semantic commit: predicate %q is outside relationship candidate set",
			predicateKey,
		)
	}
	return *selected, nil
}
