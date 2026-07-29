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
	CommitSemantic(ctx context.Context, job SemanticCommitJob) (*repository.CommitPlacementSemanticResult, error)
	CompleteSemanticPlacement(ctx context.Context, job SemanticCommitJob) (*SemanticPlacementCompletionResult, error)
}

type SemanticCommitDependencies struct {
	PlacementCommit repository.PlacementCommitRepository
}

type semanticCommitService struct {
	placementCommit repository.PlacementCommitRepository
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
	Request          verifier.SemanticReviewRequest
	Result           SemanticReviewResult
	ReviewModel      string
	PromoteToFact    bool
}

type SemanticPlacementCompletionResult struct {
	Status         string
	SemanticCommit *repository.CommitPlacementSemanticResult
	Terminal       *repository.CompletePlacementReviewResult
}

func NewSemanticCommitService(deps SemanticCommitDependencies) SemanticCommitService {
	return &semanticCommitService{placementCommit: deps.PlacementCommit}
}

func (s *semanticCommitService) CommitSemantic(
	ctx context.Context,
	job SemanticCommitJob,
) (*repository.CommitPlacementSemanticResult, error) {
	if s.placementCommit == nil {
		return nil, errors.New("semantic commit: placement commit repository is required")
	}
	job = normalizeSemanticCommitJob(job)
	if job.Result.Status != string(domain.SemanticReviewAccepted) {
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
	if job.Result.Status == string(domain.SemanticReviewAccepted) {
		committed, err := s.CommitSemantic(ctx, job)
		if err != nil {
			return nil, err
		}
		return &SemanticPlacementCompletionResult{Status: committed.Status, SemanticCommit: committed}, nil
	}
	if job.Result.Status == string(domain.SemanticReviewRetryable) {
		if semanticRetryAttemptsExhausted(job) {
			exhausted := exhaustedRetryableSemanticCommitJob(job)
			input, err := terminalReviewInputFromResult(exhausted)
			if err != nil {
				return nil, err
			}
			terminal, err := s.placementCommit.CompletePlacementReviewResult(ctx, input)
			if err != nil {
				return nil, err
			}
			return &SemanticPlacementCompletionResult{Status: terminal.Status, Terminal: terminal}, nil
		}
		input, err := retryableReviewInputFromResult(job)
		if err != nil {
			return nil, err
		}
		requeued, err := s.placementCommit.RequeuePlacementReviewResult(ctx, input)
		if err != nil {
			return nil, err
		}
		return &SemanticPlacementCompletionResult{Status: requeued.Status}, nil
	}
	input, err := terminalReviewInputFromResult(job)
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
	job.Result.Status = string(domain.SemanticReviewTerminalFailure)
	job.Result.FailureStage = semanticFailureStageOrDefault(job.Result.FailureStage, semanticFailureStageUnknown)
	job.Result.FailureClass = semanticFailureClassOrDefault(job.Result.FailureClass, semanticFailureClassUnknown)
	job.Result.RetryableExhausted = true
	job.Result.ValidationErrors = append(job.Result.ValidationErrors, verifier.SemanticValidationError{
		Field:   "placement_attempts",
		Message: "retryable semantic review exhausted placement attempts",
	})
	return job
}

func retryableReviewInputFromResult(job SemanticCommitJob) (repository.RequeuePlacementReviewInput, error) {
	if job.Result.Status != string(domain.SemanticReviewRetryable) {
		return repository.RequeuePlacementReviewInput{}, fmt.Errorf("semantic commit: unsupported retryable status %q", job.Result.Status)
	}
	return repository.RequeuePlacementReviewInput{
		TeamID:           job.TeamID,
		OwnerProfileID:   job.OwnerProfileID,
		IngestID:         job.IngestID,
		PlacementRunID:   job.PlacementRunID,
		PlacementItemID:  job.PlacementItemID,
		WorkerID:         job.WorkerID,
		ExpectedAttempts: job.ExpectedAttempts,
	}, nil
}

func terminalReviewInputFromResult(job SemanticCommitJob) (repository.CompletePlacementReviewInput, error) {
	switch job.Result.Status {
	case string(domain.SemanticReviewReviewRequired),
		string(domain.SemanticReviewRejected),
		string(domain.SemanticReviewQuarantined),
		string(domain.SemanticReviewTerminalFailure):
	default:
		return repository.CompletePlacementReviewInput{}, fmt.Errorf("semantic commit: unsupported terminal status %q", job.Result.Status)
	}
	category := "candidate"
	if job.Result.Status == string(domain.SemanticReviewQuarantined) {
		category = "quarantined"
	}
	if job.Result.Status == string(domain.SemanticReviewTerminalFailure) {
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
	return repository.CompletePlacementReviewInput{
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

func semanticCommitInputFromReview(job SemanticCommitJob) (repository.CommitPlacementSemanticInput, error) {
	mentionsByRef := make(map[string]verifier.SemanticEntityMention, len(job.Request.EntityMentions))
	for _, mention := range job.Request.EntityMentions {
		mentionsByRef[mention.Ref] = mention
	}
	evidenceByID := make(map[string]verifier.SemanticReviewEvidence, len(job.Request.Evidence))
	for _, evidence := range job.Request.Evidence {
		evidenceByID[evidence.EvidenceID] = evidence
	}
	observationsByRef := make(map[string]verifier.SemanticRelationshipObservation, len(job.Request.RelationshipObservations))
	for _, observation := range job.Request.RelationshipObservations {
		observationsByRef[observation.Ref] = observation
	}

	entities := make([]repository.PlacementEntityResolutionInput, 0, len(job.Result.EntityResults))
	for _, result := range job.Result.EntityResults {
		mention, ok := mentionsByRef[result.Ref]
		if !ok {
			return repository.CommitPlacementSemanticInput{}, fmt.Errorf("semantic commit: unknown entity result ref %q", result.Ref)
		}
		resolution := repository.PlacementEntityResolutionInput{
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

	relationships := make([]repository.PlacementRelationshipDecisionInput, 0, len(job.Result.RelationshipResults))
	relationshipReviews := make([]repository.PlacementRelationshipReviewInput, 0)
	for _, result := range job.Result.RelationshipResults {
		observation, ok := observationsByRef[result.Ref]
		if !ok {
			return repository.CommitPlacementSemanticInput{}, fmt.Errorf("semantic commit: unknown relationship result ref %q", result.Ref)
		}
		if result.PredicateStatus != "resolved" || result.PredicateKey == nil {
			confidence := result.Confidence
			support, err := semanticPlacementSupport(observation, evidenceByID)
			if err != nil {
				return repository.CommitPlacementSemanticInput{}, err
			}
			review := repository.PlacementRelationshipReviewInput{
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
					"predicate_policy_version": domain.PredicatePolicyVersion,
				},
			}
			if observation.ObjectValue != nil {
				review.ObjectValue = &repository.PlacementValueInput{
					Ref:            observation.ObjectValue.Ref,
					ValueType:      observation.ObjectValue.Type,
					CanonicalValue: observation.ObjectValue.Value,
					Display:        observation.ObjectValue.Display,
					Unit:           observation.ObjectValue.Unit,
				}
				review.ObjectRef = ""
			}
			if observation.CorrectionTarget != nil {
				review.CorrectionTarget = &repository.PlacementCorrectionTargetInput{
					RelationshipID:  observation.CorrectionTarget.RelationshipID,
					ExpectedVersion: observation.CorrectionTarget.ExpectedVersion,
				}
			}
			if observation.ConflictContext != nil {
				review.ConflictContext = &repository.PlacementConflictContextInput{
					ConflictID:      observation.ConflictContext.ConflictID,
					ExpectedVersion: observation.ConflictContext.ExpectedVersion,
				}
			}
			relationshipReviews = append(relationshipReviews, review)
			continue
		}
		predicateCandidate, err := semanticSelectedPredicateCandidate(observation, *result.PredicateKey)
		if err != nil {
			return repository.CommitPlacementSemanticInput{}, err
		}
		relationship := repository.PlacementRelationshipDecisionInput{
			Ref:               result.Ref,
			SubjectRef:        observation.SubjectRef,
			OriginalPredicate: observation.OriginalPredicate,
			PredicateKey:      *result.PredicateKey,
			PredicateVersion:  predicateCandidate.Version,
			PredicateCandidate: &repository.PlacementPredicateCandidateInput{
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
			relationship.CorrectionTarget = &repository.PlacementCorrectionTargetInput{
				RelationshipID:  observation.CorrectionTarget.RelationshipID,
				ExpectedVersion: observation.CorrectionTarget.ExpectedVersion,
			}
		}
		if observation.ConflictContext != nil {
			relationship.ConflictContext = &repository.PlacementConflictContextInput{
				ConflictID:      observation.ConflictContext.ConflictID,
				ExpectedVersion: observation.ConflictContext.ExpectedVersion,
			}
		}
		if observation.ObjectValue != nil {
			relationship.ObjectValue = &repository.PlacementValueInput{
				Ref:            observation.ObjectValue.Ref,
				ValueType:      observation.ObjectValue.Type,
				CanonicalValue: observation.ObjectValue.Value,
				Display:        observation.ObjectValue.Display,
				Unit:           observation.ObjectValue.Unit,
			}
			relationship.ObjectRef = ""
		}
		relationship.Support, err = semanticPlacementSupport(observation, evidenceByID)
		if err != nil {
			return repository.CommitPlacementSemanticInput{}, err
		}
		relationships = append(relationships, relationship)
	}

	category := "validated_claim"
	if job.PromoteToFact {
		category = "fact"
	}
	return repository.CommitPlacementSemanticInput{
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

func semanticPlacementSupport(
	observation verifier.SemanticRelationshipObservation,
	evidenceByID map[string]verifier.SemanticReviewEvidence,
) (*repository.EvidenceSupportInput, error) {
	evidence, ok := evidenceByID[observation.EvidenceID]
	if !ok {
		return nil, fmt.Errorf(
			"semantic commit: unknown relationship evidence_id %q",
			observation.EvidenceID,
		)
	}
	return &repository.EvidenceSupportInput{
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

func semanticSelectedPredicateCandidate(
	observation verifier.SemanticRelationshipObservation,
	predicateKey string,
) (verifier.SemanticPredicateCandidate, error) {
	predicateKey = strings.TrimSpace(predicateKey)
	var selected *verifier.SemanticPredicateCandidate
	for i := range observation.PredicateCandidates {
		candidate := observation.PredicateCandidates[i]
		if strings.TrimSpace(candidate.PredicateKey) != predicateKey {
			continue
		}
		if selected != nil && (selected.Version != candidate.Version ||
			selected.RelationshipKind != candidate.RelationshipKind) {
			return verifier.SemanticPredicateCandidate{}, fmt.Errorf(
				"semantic commit: predicate %q has ambiguous candidate definitions",
				predicateKey,
			)
		}
		selected = &candidate
	}
	if selected == nil {
		return verifier.SemanticPredicateCandidate{}, fmt.Errorf(
			"semantic commit: predicate %q is outside relationship candidate set",
			predicateKey,
		)
	}
	return *selected, nil
}
