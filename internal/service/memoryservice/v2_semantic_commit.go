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

var ErrV2SemanticCommitNotAccepted = errors.New("v2 semantic commit requires an accepted review result")

type V2SemanticCommitService interface {
	CommitV2Semantic(ctx context.Context, job V2SemanticCommitJob) (*repository.V2CommitPlacementSemanticResult, error)
	CompleteV2SemanticPlacement(ctx context.Context, job V2SemanticCommitJob) (*V2SemanticPlacementCompletionResult, error)
}

type V2SemanticCommitDependencies struct {
	PlacementCommit repository.V2PlacementCommitRepository
}

type v2SemanticCommitService struct {
	placementCommit repository.V2PlacementCommitRepository
}

type V2SemanticCommitJob struct {
	TeamID           string
	OwnerProfileID   string
	IngestID         string
	PlacementRunID   string
	PlacementItemID  string
	WorkerID         string
	ExpectedAttempts int
	MaxAttempts      int
	Request          verifier.V2SemanticReviewRequest
	Result           V2SemanticReviewResult
	ReviewModel      string
	PromoteToFact    bool
}

type V2SemanticPlacementCompletionResult struct {
	Status         string
	SemanticCommit *repository.V2CommitPlacementSemanticResult
	Terminal       *repository.V2CompletePlacementReviewResult
}

func NewV2SemanticCommitService(deps V2SemanticCommitDependencies) V2SemanticCommitService {
	return &v2SemanticCommitService{placementCommit: deps.PlacementCommit}
}

func (s *v2SemanticCommitService) CommitV2Semantic(
	ctx context.Context,
	job V2SemanticCommitJob,
) (*repository.V2CommitPlacementSemanticResult, error) {
	if s.placementCommit == nil {
		return nil, errors.New("v2 semantic commit: placement commit repository is required")
	}
	job = normalizeV2SemanticCommitJob(job)
	if job.Result.Status != string(domain.V2SemanticReviewAccepted) {
		return nil, ErrV2SemanticCommitNotAccepted
	}
	input, err := v2SemanticCommitInputFromReview(job)
	if err != nil {
		return nil, err
	}
	return s.placementCommit.CommitPlacementSemanticResult(ctx, input)
}

func (s *v2SemanticCommitService) CompleteV2SemanticPlacement(
	ctx context.Context,
	job V2SemanticCommitJob,
) (*V2SemanticPlacementCompletionResult, error) {
	if s.placementCommit == nil {
		return nil, errors.New("v2 semantic commit: placement commit repository is required")
	}
	job = normalizeV2SemanticCommitJob(job)
	if job.Result.Status == string(domain.V2SemanticReviewAccepted) {
		committed, err := s.CommitV2Semantic(ctx, job)
		if err != nil {
			return nil, err
		}
		return &V2SemanticPlacementCompletionResult{Status: committed.Status, SemanticCommit: committed}, nil
	}
	if job.Result.Status == string(domain.V2SemanticReviewRetryable) {
		if v2SemanticRetryAttemptsExhausted(job) {
			exhausted := v2ExhaustedRetryableSemanticCommitJob(job)
			input, err := v2TerminalReviewInputFromResult(exhausted)
			if err != nil {
				return nil, err
			}
			terminal, err := s.placementCommit.CompletePlacementReviewResult(ctx, input)
			if err != nil {
				return nil, err
			}
			return &V2SemanticPlacementCompletionResult{Status: terminal.Status, Terminal: terminal}, nil
		}
		return &V2SemanticPlacementCompletionResult{Status: job.Result.Status}, nil
	}
	input, err := v2TerminalReviewInputFromResult(job)
	if err != nil {
		return nil, err
	}
	terminal, err := s.placementCommit.CompletePlacementReviewResult(ctx, input)
	if err != nil {
		return nil, err
	}
	return &V2SemanticPlacementCompletionResult{Status: terminal.Status, Terminal: terminal}, nil
}

func normalizeV2SemanticCommitJob(job V2SemanticCommitJob) V2SemanticCommitJob {
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

func v2SemanticRetryAttemptsExhausted(job V2SemanticCommitJob) bool {
	return job.MaxAttempts > 0 && job.ExpectedAttempts >= job.MaxAttempts
}

func v2ExhaustedRetryableSemanticCommitJob(job V2SemanticCommitJob) V2SemanticCommitJob {
	job.Result.Status = string(domain.V2SemanticReviewTerminalFailure)
	job.Result.ValidationErrors = append(job.Result.ValidationErrors, verifier.V2SemanticValidationError{
		Field:   "placement_attempts",
		Message: "retryable semantic review exhausted placement attempts",
	})
	return job
}

func v2TerminalReviewInputFromResult(job V2SemanticCommitJob) (repository.V2CompletePlacementReviewInput, error) {
	switch job.Result.Status {
	case string(domain.V2SemanticReviewReviewRequired),
		string(domain.V2SemanticReviewRejected),
		string(domain.V2SemanticReviewQuarantined),
		string(domain.V2SemanticReviewTerminalFailure):
	default:
		return repository.V2CompletePlacementReviewInput{}, fmt.Errorf("v2 semantic commit: unsupported terminal status %q", job.Result.Status)
	}
	category := "candidate"
	if job.Result.Status == string(domain.V2SemanticReviewQuarantined) {
		category = "quarantined"
	}
	if job.Result.Status == string(domain.V2SemanticReviewTerminalFailure) {
		category = "failed"
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
		Payload: map[string]any{
			"request_id":         job.Request.RequestID,
			"response_hash":      job.Result.ResponseHash,
			"review_model":       job.ReviewModel,
			"placement_attempts": job.ExpectedAttempts,
			"max_attempts":       job.MaxAttempts,
			"review_outcome_ids": append([]string(nil), job.Result.OutcomeIDs...),
			"validation_errors":  v2SemanticValidationMessages(job.Result.ValidationErrors),
		},
	}, nil
}

func v2SemanticCommitInputFromReview(job V2SemanticCommitJob) (repository.V2CommitPlacementSemanticInput, error) {
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
			return repository.V2CommitPlacementSemanticInput{}, fmt.Errorf("v2 semantic commit: unknown entity result ref %q", result.Ref)
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
	for _, result := range job.Result.RelationshipResults {
		observation, ok := observationsByRef[result.Ref]
		if !ok {
			return repository.V2CommitPlacementSemanticInput{}, fmt.Errorf("v2 semantic commit: unknown relationship result ref %q", result.Ref)
		}
		if result.PredicateStatus != "resolved" || result.PredicateKey == nil {
			return repository.V2CommitPlacementSemanticInput{}, fmt.Errorf("v2 semantic commit: relationship result %q is not resolved", result.Ref)
		}
		relationship := repository.V2PlacementRelationshipDecisionInput{
			Ref:               result.Ref,
			SubjectRef:        observation.SubjectRef,
			OriginalPredicate: observation.OriginalPredicate,
			PredicateKey:      *result.PredicateKey,
			ObjectRef:         observation.ObjectRef,
			ValidFrom:         observation.ValidFrom,
			ValidTo:           observation.ValidTo,
			EvidenceVerdict:   result.EvidenceVerdict,
			PromoteToFact:     job.PromoteToFact,
			Confidence:        &result.Confidence,
			Rationale:         result.Rationale,
			Model:             job.ReviewModel,
			ResponseHash:      job.Result.ResponseHash,
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
				Display:        observation.ObjectValue.Value,
			}
			relationship.ObjectRef = ""
		}
		evidence, ok := evidenceByID[observation.EvidenceID]
		if !ok {
			return repository.V2CommitPlacementSemanticInput{}, fmt.Errorf("v2 semantic commit: unknown relationship evidence_id %q", observation.EvidenceID)
		}
		relationship.Support = &repository.V2EvidenceSupportInput{
			FragmentID:       evidence.FragmentID,
			SourceGroupKey:   "semantic_review:" + evidence.EvidenceID,
			SourceRevisionID: evidence.SourceRevisionID,
			SpanStart:        observation.Start,
			SpanEnd:          observation.End,
			Quote:            observation.Quote,
			Authority:        "primary",
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
		Payload: map[string]any{
			"request_id":         job.Request.RequestID,
			"response_hash":      job.Result.ResponseHash,
			"review_model":       job.ReviewModel,
			"review_outcome_ids": append([]string(nil), job.Result.OutcomeIDs...),
		},
	}, nil
}
