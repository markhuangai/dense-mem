package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// SynchronousRememberCommitResult is the terminal, replayable result produced by
// the one request-owned Remember transaction. PublicResult is already safe to
// persist and replay; callers must not reconstruct it from placement rows.
type SynchronousRememberCommitResult struct {
	IngestID            string
	AssessmentID        string
	Outcome             string
	PublicResult        map[string]any
	RelationshipResults []RelationshipDecisionResult
	SearchDocuments     []SearchDocumentResult
	EntityResolutionIDs []string
}

type rememberCommitStageError struct {
	stage string
	err   error
}

func (e *rememberCommitStageError) Error() string {
	return fmt.Sprintf("repository: commit Remember at %s: %v", e.stage, e.err)
}

func (e *rememberCommitStageError) Unwrap() error { return e.err }

func RememberCommitFailureStage(err error) string {
	var staged *rememberCommitStageError
	if errors.As(err, &staged) {
		return staged.stage
	}
	return ""
}

// CommitRememberWithEmbeddings persists one accepted Remember after
// provider work has completed. No knowledge-ingest, evidence, placement, or
// attempt row exists before this transaction starts.
func (r *LedgerRepositoryImpl) CommitRememberWithEmbeddings(
	ctx context.Context,
	input SynchronousRememberCommitInput,
	embeddings []InlineEmbeddingResult,
) (*SynchronousRememberCommitResult, error) {
	input = normalizeSynchronousRememberCommitInput(input)
	if err := validateSynchronousRememberCommitInput(input); err != nil {
		return nil, err
	}
	if input.AssessmentID == "" || len(input.AssessmentJSON) == 0 {
		return nil, errors.New("remember assessment response is required for an accepted commit")
	}
	if err := validateRememberEmbeddingContractFence(embeddings); err != nil {
		return nil, err
	}
	ctx = WithInlineEmbeddingResults(ctx, embeddings)
	result := &SynchronousRememberCommitResult{IngestID: input.IngestID, AssessmentID: input.AssessmentID, Outcome: "completed"}
	stage := "transaction_setup"
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		stage = "idempotency_fence"
		if err := lockRememberIdempotencyKeyInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey); err != nil {
			return err
		}
		if err := validateRememberFailureRetryInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, input.RequestHash, input.MigratedRequestHash); err != nil && !errors.Is(err, ErrRememberReplay) {
			return err
		}
		if replay, err := loadRememberAttemptInTx(ctx, tx, input); err != nil {
			return err
		} else if replay != nil {
			result.PublicResult = replay.PublicResult
			result.Outcome = replay.Outcome
			return ErrRememberReplay
		}
		stage = "embedding_contract_fence"
		contract, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := validateRememberEmbeddingContractAgainstActive(embeddings, contract); err != nil {
			return err
		}
		stage = "ingest"
		createInput := rememberCreateIngestInput(input)
		if err := validateCreateIngestInput(createInput); err != nil {
			return err
		}
		created, err := insertRememberKnowledgeIngest(ctx, tx, createInput)
		if err != nil {
			return err
		}
		if !created {
			return ErrRememberReplay
		}
		stage = "predicate_catalog"
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}

		stage = "evidence"
		sources := make(map[string]SourceRevisionResult, len(input.Evidence))
		evidence := make([]EvidenceFragment, 0, len(input.Evidence))
		for index, item := range createInput.Evidence {
			var source *SourceRevisionResult
			if item.SourceKey != "" {
				advanced, err := advanceSourceRevisionInTx(ctx, tx, AdvanceSourceRevisionInput{
					TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
					SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
					SourceKey: item.SourceKey, SourceKind: sourceKindForEvidence(item.SourceType), Authority: item.Authority,
					RevisionToken: item.SourceRevisionToken, ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken,
					ContentHash: item.SourceRevisionContentHash, Envelope: item.SourceRevisionEnvelope,
				}, sources)
				if err != nil {
					return err
				}
				source = advanced
			}
			fragment, err := insertEvidenceFragment(ctx, tx, createInput, input.IngestID, index, item, source)
			if err != nil {
				return err
			}
			evidence = append(evidence, fragment)
			if item.InitialEvent != nil {
				eventInput := SecurityEventInput{
					TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
					FragmentID: fragment.FragmentID, SecurityEventDraft: *item.InitialEvent,
				}
				if _, err := insertSecurityEvent(ctx, tx, eventInput); err != nil {
					return err
				}
				if item.InitialEvent.Decision == "quarantine" {
					if err := insertEvidenceQuarantine(ctx, tx, createInput, input.IngestID, fragment.FragmentID, item.InitialEvent.Reason); err != nil {
						return err
					}
				}
			}
		}
		if err := validateRememberSubmissionSupersessionTargets(ctx, tx, createInput, input.IngestID); err != nil {
			return err
		}
		if err := applyEvidenceSupersessions(ctx, tx, createInput, input.IngestID, evidence); err != nil {
			return err
		}
		applyRememberCommitSourceReferences(input.Commit.RelationshipObservations, evidence)

		stage = "assessment_history"
		if err := insertRememberSemanticAssessment(ctx, tx, input); err != nil {
			return err
		}
		semanticResult := &submissionSemanticCommitState{Status: string(domain.SemanticReviewAccepted)}
		common := CommitSemanticInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
		}
		stage = "entity_resolution"
		entitiesByRef := make(map[string]string, len(input.Commit.EntityResolutions))
		for _, entry := range input.Commit.EntityResolutions {
			fragmentID := strings.TrimSpace(entry.Resolution.FragmentID)
			if !rememberEvidenceExists(evidence, fragmentID) {
				return errors.New("remember entity resolution fragment is outside the Remember request")
			}
			commit := common
			commit.FragmentID = fragmentID
			resolutionID, entityID, err := insertSemanticEntityResolution(ctx, tx, commit, entry.Resolution)
			if err != nil {
				return err
			}
			if entityID == "" {
				return ErrSubmissionAssessmentNonPromotable
			}
			if _, exists := entitiesByRef[entry.Resolution.MentionRef]; exists {
				return errors.New("remember entity resolution mention_ref is duplicated")
			}
			entitiesByRef[entry.Resolution.MentionRef] = entityID
			semanticResult.EntityResolutionIDs = append(semanticResult.EntityResolutionIDs, resolutionID)
		}

		stage = "predicate_resolution"
		if err := resolveSubmissionPredicates(ctx, tx, &input.Commit); err != nil {
			return err
		}
		stage = "relationships"
		appliedSplits := make([]submissionRelationshipAppliedSplit, 0, len(input.Commit.RelationshipObservations))
		for _, entry := range input.Commit.RelationshipObservations {
			fragmentID, err := rememberRelationshipFragmentID(entry)
			if err != nil {
				return err
			}
			for _, support := range relationshipEvidenceSupports(entry.Observation.Support, entry.Observation.Supports) {
				if !rememberEvidenceExists(evidence, support.FragmentID) {
					return errors.New("remember relationship support is outside the Remember request")
				}
			}
			commit := common
			commit.FragmentID = fragmentID
			decision, err := relationshipDecisionFromSemanticObservation(ctx, tx, commit, entry.Observation, entitiesByRef)
			if err != nil {
				return err
			}
			if entry.Observation.ConflictContext != nil {
				if err := requireRelationshipConflictContextMatchesDecision(ctx, tx, input.TeamID, *entry.Observation.ConflictContext, decision); err != nil {
					return err
				}
			}
			before := len(semanticResult.RelationshipResults)
			if err := applySemanticRelationshipDecision(
				ctx, tx, commit, decision, entry.Observation.CorrectionTarget,
				entry.Observation.ConflictContext, fragmentID,
				ConflictRuntimeConfig{ReviewTTLDays: r.conflictReviewTTLDays, Timezone: r.conflictReviewTimezone}, semanticResult,
			); err != nil {
				return err
			}
			if len(semanticResult.RelationshipResults) != before+1 {
				return ErrSubmissionAssessmentNonPromotable
			}
			applied := semanticResult.RelationshipResults[len(semanticResult.RelationshipResults)-1]
			if applied.Relationship == nil || applied.Relationship.Status != string(domain.RelationshipStatusActive) {
				return ErrSubmissionAssessmentNonPromotable
			}
			appliedSplits = append(appliedSplits, submissionRelationshipAppliedSplit{RelationshipRef: entry.RelationshipRef, SplitIndex: entry.SplitIndex, Result: applied})
		}

		stage = "search_documents"
		for _, item := range evidence {
			commit := common
			commit.FragmentID = item.FragmentID
			document, err := upsertSemanticEvidenceSearchDocumentWithContract(ctx, tx, commit, item.FragmentID, map[string]any{"remember_synchronous": true}, contract)
			if err != nil {
				return err
			}
			appendSemanticSearchDocument(semanticResult, document)
		}
		stage = "embeddings"
		if err := applyInlineSubmissionEmbeddings(ctx, tx, input.Commit, semanticResult, embeddings); err != nil {
			return err
		}
		stage = "relationship_results"
		if err := insertSubmissionRelationshipResults(ctx, tx, input.Commit.RememberCommitScope, input.Commit.RelationshipResults, appliedSplits); err != nil {
			return err
		}
		publicResult := rememberPublicResult(input, evidence, semanticResult, appliedSplits)
		stage = "terminal_attempt"
		if err := insertRememberAttemptInTx(ctx, tx, RememberAttemptRecordInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, AttemptID: input.IngestID,
			SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
			RequestHash: input.RequestHash, ContractVersion: domain.ContractVersion, SubmissionKind: "remember",
			Outcome: "completed", CorrelationID: rememberCorrelationID(input.Metadata), PublicResult: publicResult,
			EvidenceCount: len(evidence), RelationshipCount: len(input.Commit.RelationshipResults),
			DocumentCount: len(semanticResult.SearchDocuments), AssessorTurns: input.AssessorTurns, Duration: rememberAttemptDuration(input),
		}); err != nil {
			return err
		}
		result.PublicResult = publicResult
		result.RelationshipResults = append([]RelationshipDecisionResult(nil), semanticResult.RelationshipResults...)
		result.SearchDocuments = append([]SearchDocumentResult(nil), semanticResult.SearchDocuments...)
		result.EntityResolutionIDs = append([]string(nil), semanticResult.EntityResolutionIDs...)
		stage = "transaction_commit"
		return nil
	})
	if err != nil {
		return result, &rememberCommitStageError{stage: stage, err: err}
	}
	return result, nil
}

func validateRememberEmbeddingContractFence(embeddings []InlineEmbeddingResult) error {
	if len(embeddings) == 0 {
		return nil
	}
	first := embeddings[0]
	if _, err := uuid.Parse(strings.TrimSpace(first.EmbeddingContractID)); err != nil {
		return fmt.Errorf("%w: embedding contract fence is invalid", ErrSearchContractMismatch)
	}
	if first.EmbeddingDimensions < 1 || strings.TrimSpace(first.EmbeddingModel) == "" || first.IndexGeneration < 1 {
		return fmt.Errorf("%w: embedding contract fence is incomplete", ErrSearchContractMismatch)
	}
	if _, err := uuid.Parse(strings.TrimSpace(first.SearchIndexGenerationID)); err != nil {
		return fmt.Errorf("%w: search generation fence is invalid", ErrSearchContractMismatch)
	}
	for _, embedding := range embeddings[1:] {
		if embedding.EmbeddingContractID != first.EmbeddingContractID ||
			embedding.EmbeddingDimensions != first.EmbeddingDimensions ||
			embedding.EmbeddingModel != first.EmbeddingModel ||
			embedding.SearchIndexGenerationID != first.SearchIndexGenerationID ||
			embedding.IndexGeneration != first.IndexGeneration {
			return fmt.Errorf("%w: embedding results carry mixed contract fences", ErrSearchContractMismatch)
		}
	}
	return nil
}

func validateRememberEmbeddingContractAgainstActive(embeddings []InlineEmbeddingResult, contract *ActiveSearchContract) error {
	if len(embeddings) == 0 {
		return nil
	}
	if contract == nil || embeddings[0].EmbeddingContractID != contract.EmbeddingContractID ||
		embeddings[0].EmbeddingDimensions != contract.EmbeddingDimensions ||
		embeddings[0].EmbeddingModel != contract.EmbeddingModel ||
		embeddings[0].SearchIndexGenerationID != contract.SearchIndexGenerationID ||
		embeddings[0].IndexGeneration != contract.IndexGeneration {
		return fmt.Errorf("%w: active embedding contract changed after provider work", ErrSearchContractMismatch)
	}
	return nil
}
