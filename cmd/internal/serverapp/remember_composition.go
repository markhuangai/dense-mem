package serverapp

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/assessor"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	remembercontract "github.com/markhuangai/dense-mem/internal/remember/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	rememberprocessor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

type rememberApplicationDependencies struct {
	Ledger   *repository.LedgerRepositoryImpl
	Catalog  *repository.SemanticRepositoryImpl
	Assessor assessor.Provider
	Embedder embeddingcontract.EmbeddingProviderInterface
	Limits   assessor.SemanticAssessmentLimits
	Metrics  observability.DiscoverabilityMetrics
	Logger   observability.LogProvider
	Audit    securityRejectionAuditAppender
}

func buildRememberApplication(deps rememberApplicationDependencies) rememberapp.Service {
	processor := rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{
		Ledger: newRememberPersistenceAdapter(deps.Ledger), Catalog: newRememberAssessmentCatalogAdapter(deps.Catalog), Assessor: deps.Assessor,
		Embedder: deps.Embedder, Limits: deps.Limits, Metrics: deps.Metrics,
		Logger: deps.Logger, IsStaleInput: repository.IsRememberStaleInputError,
		CommitFailureStage: repository.RememberCommitFailureStage,
	})
	return rememberapp.NewService(rememberapp.Dependencies{
		Synchronous: processor,
		Auditor:     newRememberSecurityRejectionAuditAdapter(deps.Audit),
		Metrics:     deps.Metrics,
		Logger:      deps.Logger,
	})
}

func buildRememberAttemptDiagnostics(repo repository.RememberAttemptDiagnosticsRepository) *service.RememberAttemptDiagnosticsService {
	return service.NewRememberAttemptDiagnosticsService(repo)
}

type rememberPersistenceAdapter struct {
	legacy *repository.LedgerRepositoryImpl
}

var _ remembercontract.Persistence = (*rememberPersistenceAdapter)(nil)
var _ interface {
	WithRememberAttemptLock(context.Context, string, string, string, func(bool) error) error
} = (*rememberPersistenceAdapter)(nil)

func newRememberPersistenceAdapter(legacy *repository.LedgerRepositoryImpl) remembercontract.Persistence {
	if legacy == nil {
		return nil
	}
	return &rememberPersistenceAdapter{legacy: legacy}
}

func (a *rememberPersistenceAdapter) LoadRememberAttempt(ctx context.Context, input knowledgecontract.RememberAttemptLookupInput) (*knowledgecontract.RememberAttempt, error) {
	return a.legacy.LoadRememberAttempt(ctx, input)
}

func (a *rememberPersistenceAdapter) PlanRememberDuplicateEmbeddings(ctx context.Context, input knowledgecontract.RememberDuplicateCandidateInput) (*knowledgecontract.RememberDuplicateEmbeddingPlan, error) {
	return a.legacy.PlanRememberDuplicateEmbeddings(ctx, input)
}

func (a *rememberPersistenceAdapter) ResolveRememberDuplicateCandidates(ctx context.Context, input knowledgecontract.RememberDuplicateCandidateInput, embeddings []knowledgecontract.InlineEmbeddingResult) (*knowledgecontract.RememberDuplicateResolutionResult, error) {
	return a.legacy.ResolveRememberDuplicateCandidates(ctx, input, embeddings)
}

func (a *rememberPersistenceAdapter) PlanRememberEmbeddings(ctx context.Context, input knowledgecontract.SynchronousRememberCommitInput) (*knowledgecontract.InlineEmbeddingPlan, error) {
	return a.legacy.PlanRememberEmbeddings(ctx, legacySynchronousRememberCommitInput(input))
}

func (a *rememberPersistenceAdapter) CommitRememberWithEmbeddings(ctx context.Context, input knowledgecontract.SynchronousRememberCommitInput, embeddings []knowledgecontract.InlineEmbeddingResult) (*knowledgecontract.SynchronousRememberCommitResult, error) {
	return a.legacy.CommitRememberWithEmbeddings(ctx, legacySynchronousRememberCommitInput(input), embeddings)
}

func (a *rememberPersistenceAdapter) RecordRememberFailure(ctx context.Context, input knowledgecontract.RememberFailureRecordInput) error {
	return a.legacy.RecordRememberFailure(ctx, input)
}

func (a *rememberPersistenceAdapter) WithRememberAttemptLock(ctx context.Context, teamID, ownerProfileID, idempotencyKey string, fn func(bool) error) error {
	return a.legacy.WithRememberAttemptLock(ctx, teamID, ownerProfileID, idempotencyKey, fn)
}

type rememberAssessmentCatalogAdapter struct {
	legacy *repository.SemanticRepositoryImpl
}

var _ remembercontract.SubmissionAssessmentCatalog = (*rememberAssessmentCatalogAdapter)(nil)
var _ remembercontract.SubmissionAssessmentKnownEvidenceCatalog = (*rememberAssessmentCatalogAdapter)(nil)

func newRememberAssessmentCatalogAdapter(legacy *repository.SemanticRepositoryImpl) remembercontract.SubmissionAssessmentCatalog {
	if legacy == nil {
		return nil
	}
	return &rememberAssessmentCatalogAdapter{legacy: legacy}
}

func (a *rememberAssessmentCatalogAdapter) ListSubmissionAssessmentEntityCatalog(ctx context.Context, input knowledgecontract.SubmissionAssessmentEntityCatalogInput) (knowledgecontract.SubmissionAssessmentEntityCatalogResult, error) {
	legacyInput := repository.SubmissionAssessmentEntityCatalogInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, SpaceID: input.SpaceID, CandidateLimit: input.CandidateLimit,
		Entities: make([]repository.SubmissionAssessmentEntityCatalogTarget, len(input.Entities)),
	}
	for i, target := range input.Entities {
		legacyInput.Entities[i] = repository.SubmissionAssessmentEntityCatalogTarget{
			Ref: target.Ref, Surface: target.Surface, EntityKind: target.EntityKind, KnownEntityID: target.KnownEntityID,
		}
	}
	result, err := a.legacy.ListSubmissionAssessmentEntityCatalog(ctx, legacyInput)
	if err != nil {
		return knowledgecontract.SubmissionAssessmentEntityCatalogResult{}, err
	}
	native := knowledgecontract.SubmissionAssessmentEntityCatalogResult{Complete: result.Complete, Groups: make([]knowledgecontract.SubmissionAssessmentEntityCatalogGroup, len(result.Groups))}
	for i, group := range result.Groups {
		native.Groups[i] = knowledgecontract.SubmissionAssessmentEntityCatalogGroup{Ref: group.Ref, Complete: group.Complete, Candidates: make([]knowledgecontract.SemanticReviewEntityCandidate, len(group.Candidates))}
		for j, candidate := range group.Candidates {
			native.Groups[i].Candidates[j] = knowledgecontract.SemanticReviewEntityCandidate{
				TeamID: candidate.TeamID, EntityID: candidate.EntityID, EntityKind: candidate.EntityKind,
				CanonicalName: candidate.CanonicalName, ActiveNames: candidate.ActiveNames,
				IdentityContext: candidate.IdentityContext, Status: candidate.Status,
			}
		}
	}
	return native, nil
}

func (a *rememberAssessmentCatalogAdapter) ResolveSemanticReviewPredicateCandidates(ctx context.Context, input knowledgecontract.SemanticReviewPredicateResolutionInput) ([]knowledgecontract.SemanticReviewPredicateResolution, error) {
	result, err := a.legacy.ResolveSemanticReviewPredicateCandidates(ctx, repository.SemanticReviewPredicateResolutionInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, Predicates: input.Predicates, Limit: input.Limit,
	})
	if err != nil {
		return nil, err
	}
	native := make([]knowledgecontract.SemanticReviewPredicateResolution, len(result))
	for i, resolution := range result {
		native[i] = knowledgecontract.SemanticReviewPredicateResolution{
			RequestedPredicate: resolution.RequestedPredicate, MatchKind: resolution.MatchKind,
			Candidate: knowledgecontract.SemanticReviewPredicateCandidate{
				PredicateKey: resolution.Candidate.PredicateKey, Version: resolution.Candidate.Version,
				Aliases: resolution.Candidate.Aliases, AllowedSubjectKinds: resolution.Candidate.AllowedSubjectKinds,
				AllowedObjectKinds: resolution.Candidate.AllowedObjectKinds, RelationshipKind: resolution.Candidate.RelationshipKind,
				CurrentCardinality: resolution.Candidate.CurrentCardinality, LifecycleState: resolution.Candidate.LifecycleState,
			},
		}
	}
	return native, nil
}

func (a *rememberAssessmentCatalogAdapter) ListSemanticAssessmentPredicateOptions(ctx context.Context, input knowledgecontract.SemanticAssessmentPredicateOptionsInput) ([]knowledgecontract.SemanticReviewPredicateCandidate, error) {
	result, err := a.legacy.ListSemanticAssessmentPredicateOptions(ctx, repository.SemanticAssessmentPredicateOptionsInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, QueryText: input.QueryText, ProposedKeys: input.ProposedKeys, Limit: input.Limit,
	})
	if err != nil {
		return nil, err
	}
	native := make([]knowledgecontract.SemanticReviewPredicateCandidate, len(result))
	for i, candidate := range result {
		native[i] = knowledgecontract.SemanticReviewPredicateCandidate{
			PredicateKey: candidate.PredicateKey, Version: candidate.Version, Aliases: candidate.Aliases,
			AllowedSubjectKinds: candidate.AllowedSubjectKinds, AllowedObjectKinds: candidate.AllowedObjectKinds,
			RelationshipKind: candidate.RelationshipKind, CurrentCardinality: candidate.CurrentCardinality,
			LifecycleState: candidate.LifecycleState,
		}
	}
	return native, nil
}

func (a *rememberAssessmentCatalogAdapter) ListSubmissionAssessmentKnownEvidence(ctx context.Context, input knowledgecontract.SubmissionAssessmentKnownEvidenceInput) (knowledgecontract.SubmissionAssessmentKnownEvidenceResult, error) {
	result, err := a.legacy.ListSubmissionAssessmentKnownEvidence(ctx, repository.SubmissionAssessmentKnownEvidenceInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, SpaceID: input.SpaceID, EvidenceIDs: input.EvidenceIDs,
	})
	if err != nil {
		return knowledgecontract.SubmissionAssessmentKnownEvidenceResult{}, err
	}
	native := knowledgecontract.SubmissionAssessmentKnownEvidenceResult{Evidence: make([]knowledgecontract.SubmissionAssessmentKnownEvidence, len(result.Evidence))}
	for i, item := range result.Evidence {
		native.Evidence[i] = knowledgecontract.SubmissionAssessmentKnownEvidence{
			TeamID: item.TeamID, EvidenceID: item.EvidenceID, FragmentID: item.FragmentID, IngestID: item.IngestID,
			OwnerProfileID: item.OwnerProfileID, Content: item.Content, ContentHash: item.ContentHash,
			Authority: item.Authority, SourceID: item.SourceID, SourceRevisionID: item.SourceRevisionID,
			CurrentSourceRevisionID: item.CurrentSourceRevisionID, SpaceID: item.SpaceID, SpaceGeneration: item.SpaceGeneration,
		}
	}
	return native, nil
}

func legacySynchronousRememberCommitInput(input knowledgecontract.SynchronousRememberCommitInput) repository.SynchronousRememberCommitInput {
	return repository.SynchronousRememberCommitInput{
		TeamID:                                  input.TeamID,
		OwnerProfileID:                          input.OwnerProfileID,
		IngestID:                                input.IngestID,
		SpaceID:                                 input.SpaceID,
		SpaceGeneration:                         input.SpaceGeneration,
		IdempotencyKey:                          input.IdempotencyKey,
		RequestHash:                             input.RequestHash,
		SourceSummary:                           input.SourceSummary,
		Proposal:                                input.Proposal,
		Metadata:                                input.Metadata,
		Evidence:                                input.Evidence,
		AssessmentID:                            input.AssessmentID,
		AssessmentJSON:                          input.AssessmentJSON,
		EvidenceSecurityResults:                 legacyEvidenceSecurityResults(input.EvidenceSecurityResults),
		ProviderTurns:                           input.ProviderTurns,
		InputTokens:                             input.InputTokens,
		OutputTokens:                            input.OutputTokens,
		CandidateContextOmittedCandidates:       input.CandidateContextOmittedCandidates,
		CandidateContextOmittedPredicateOptions: input.CandidateContextOmittedPredicateOptions,
		AssessorTurns:                           input.AssessorTurns,
		Duration:                                input.Duration,
		StartedAt:                               input.StartedAt,
		CorrelationID:                           input.CorrelationID,
		PublicResult:                            input.PublicResult,
		DuplicateResolutions:                    input.DuplicateResolutions,
		Commit:                                  legacyCommitSubmissionAssessmentInput(input.Commit),
	}
}

func legacyEvidenceSecurityResults(input []knowledgecontract.EvidenceSecurityResult) []repository.EvidenceSecurityResult {
	if input == nil {
		return nil
	}
	result := make([]repository.EvidenceSecurityResult, len(input))
	for i, item := range input {
		result[i] = repository.EvidenceSecurityResult{
			FragmentID: item.FragmentID, EvidenceID: item.EvidenceID, EvidenceIndex: item.EvidenceIndex,
			Decision: item.Decision, Safe: item.Safe, Signals: legacySecuritySignals(item.Signals),
		}
	}
	return result
}

func legacySecuritySignals(input []knowledgecontract.SecuritySignalInput) []repository.SecuritySignalInput {
	if input == nil {
		return nil
	}
	result := make([]repository.SecuritySignalInput, len(input))
	for i, signal := range input {
		result[i] = repository.SecuritySignalInput(signal)
	}
	return result
}

func legacyCommitSubmissionAssessmentInput(input knowledgecontract.CommitSubmissionAssessmentInput) repository.CommitSubmissionAssessmentInput {
	result := repository.CommitSubmissionAssessmentInput{
		RememberCommitScope: repository.RememberCommitScope{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
		},
		AssessmentID: input.AssessmentID, EvidenceConflictCandidateEvidenceIDs: input.EvidenceConflictCandidateEvidenceIDs,
		Payload: input.Payload,
	}
	if input.Items != nil {
		result.Items = make([]repository.SubmissionAssessmentItemInput, len(input.Items))
		for i, item := range input.Items {
			result.Items[i] = repository.SubmissionAssessmentItemInput(item)
		}
	}
	if input.KnownEvidenceSnapshot != nil {
		result.KnownEvidenceSnapshot = make([]repository.SubmissionAssessmentKnownEvidence, len(input.KnownEvidenceSnapshot))
		for i, item := range input.KnownEvidenceSnapshot {
			result.KnownEvidenceSnapshot[i] = repository.SubmissionAssessmentKnownEvidence(item)
		}
	}
	if input.EvidenceConflictResults != nil {
		result.EvidenceConflictResults = make([]repository.EvidenceConflictResultInput, len(input.EvidenceConflictResults))
		for i, item := range input.EvidenceConflictResults {
			result.EvidenceConflictResults[i] = repository.EvidenceConflictResultInput{Positions: legacyEvidenceConflictPositions(item.Positions)}
		}
	}
	if input.EntityResolutions != nil {
		result.EntityResolutions = make([]repository.SubmissionAssessmentEntityResolutionInput, len(input.EntityResolutions))
		for i, item := range input.EntityResolutions {
			result.EntityResolutions[i] = repository.SubmissionAssessmentEntityResolutionInput{
				Resolution: repository.SemanticEntityResolutionInput(item.Resolution),
			}
		}
	}
	if input.RelationshipObservations != nil {
		result.RelationshipObservations = make([]repository.SubmissionAssessmentRelationshipObservationInput, len(input.RelationshipObservations))
		for i, item := range input.RelationshipObservations {
			result.RelationshipObservations[i] = repository.SubmissionAssessmentRelationshipObservationInput{
				RelationshipRef: item.RelationshipRef, SplitIndex: item.SplitIndex,
				Observation: legacySemanticRelationshipDecisionInput(item.Observation),
			}
		}
	}
	if input.PredicateRegistrations != nil {
		result.PredicateRegistrations = make([]repository.SubmissionPredicateRegistrationInput, len(input.PredicateRegistrations))
		for i, item := range input.PredicateRegistrations {
			result.PredicateRegistrations[i] = repository.SubmissionPredicateRegistrationInput(item)
		}
	}
	if input.RelationshipResults != nil {
		result.RelationshipResults = make([]repository.SubmissionRelationshipResultInput, len(input.RelationshipResults))
		for i, item := range input.RelationshipResults {
			result.RelationshipResults[i] = repository.SubmissionRelationshipResultInput{
				RelationshipRef: item.RelationshipRef, Disposition: item.Disposition, Reason: item.Reason,
				Splits: legacySubmissionRelationshipSplits(item.Splits),
			}
		}
	}
	return result
}

func legacyEvidenceConflictPositions(input []knowledgecontract.EvidenceConflictPositionInput) []repository.EvidenceConflictPositionInput {
	if input == nil {
		return nil
	}
	result := make([]repository.EvidenceConflictPositionInput, len(input))
	for i, position := range input {
		result[i] = repository.EvidenceConflictPositionInput(position)
	}
	return result
}

func legacySubmissionRelationshipSplits(input []knowledgecontract.SubmissionRelationshipSplitInput) []repository.SubmissionRelationshipSplitInput {
	if input == nil {
		return nil
	}
	result := make([]repository.SubmissionRelationshipSplitInput, len(input))
	for i, split := range input {
		result[i] = repository.SubmissionRelationshipSplitInput(split)
	}
	return result
}

func legacySemanticRelationshipDecisionInput(input knowledgecontract.SemanticRelationshipDecisionInput) repository.SemanticRelationshipDecisionInput {
	result := repository.SemanticRelationshipDecisionInput{
		Ref: input.Ref, SubjectRef: input.SubjectRef, OriginalPredicate: input.OriginalPredicate,
		PredicateKey: input.PredicateKey, PredicateVersion: input.PredicateVersion, ExactPredicateKey: input.ExactPredicateKey,
		ObjectRef: input.ObjectRef, Polarity: input.Polarity, ScopeKey: input.ScopeKey,
		ValidFrom: input.ValidFrom, ValidTo: input.ValidTo, EvidenceVerdict: input.EvidenceVerdict,
		AssessorAccepted: input.AssessorAccepted, PromoteToFact: input.PromoteToFact, Confidence: input.Confidence,
		Rationale: input.Rationale, Model: input.Model, ResponseHash: input.ResponseHash,
		ObservationMetadata: input.ObservationMetadata, RelationshipMetadata: input.RelationshipMetadata,
		AssessmentID: input.AssessmentID, AssessmentPolicyVersion: input.AssessmentPolicyVersion,
		ThresholdUsed: input.ThresholdUsed, GateResult: input.GateResult, SuppressSupport: input.SuppressSupport,
	}
	if input.PredicateCandidate != nil {
		candidate := repository.SemanticPredicateCandidateInput(*input.PredicateCandidate)
		result.PredicateCandidate = &candidate
	}
	if input.ObjectValue != nil {
		value := repository.SemanticValueInput(*input.ObjectValue)
		result.ObjectValue = &value
	}
	if input.CorrectionTarget != nil {
		target := repository.SemanticCorrectionTargetInput(*input.CorrectionTarget)
		result.CorrectionTarget = &target
	}
	if input.ConflictContext != nil {
		conflict := repository.SemanticConflictContextInput(*input.ConflictContext)
		result.ConflictContext = &conflict
	}
	if input.Support != nil {
		support := repository.EvidenceSupportInput(*input.Support)
		result.Support = &support
	}
	if input.Supports != nil {
		result.Supports = make([]repository.EvidenceSupportInput, len(input.Supports))
		for i, support := range input.Supports {
			result.Supports[i] = repository.EvidenceSupportInput(support)
		}
	}
	return result
}
