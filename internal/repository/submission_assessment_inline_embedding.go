package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// PlanSubmissionAssessmentEmbeddings renders the bounded document set without
// mutating semantic state or invoking a provider. The subsequent commit maps
// provider vectors to the final rows by document hash and version fences.
func (r *LedgerRepositoryImpl) PlanSubmissionAssessmentEmbeddings(
	ctx context.Context,
	input CommitSubmissionAssessmentInput,
) (*InlineEmbeddingPlan, error) {
	input = normalizeCommitSubmissionAssessmentInput(input)
	if err := validateCommitSubmissionAssessmentInput(input); err != nil {
		return nil, err
	}
	plan := &InlineEmbeddingPlan{Documents: []SearchDocumentForEmbedding{}}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := validateSubmissionAssessmentCommitScope(ctx, tx, input); err != nil {
			return err
		}
		items, err := loadLockedSubmissionAssessmentItems(ctx, tx, input.SubmissionAssessmentRunScope)
		if err != nil {
			return fmt.Errorf("load submission assessment items for embedding plan: %w", err)
		}
		applyRememberSourceReferencesToObservations(input.RelationshipObservations, items)
		if err := validateSubmissionCommitItems(input.Items, items); err != nil {
			return err
		}
		deletionOnly := items[0].DeletionOnly
		for _, item := range items[1:] {
			if item.DeletionOnly != deletionOnly {
				return ErrSubmissionAssessmentNonPromotable
			}
		}
		if deletionOnly {
			return nil
		}

		contract, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		entityNames, err := loadInlineEmbeddingEntityNames(ctx, tx, input.TeamID, input.EntityResolutions)
		if err != nil {
			return err
		}
		registrations := make(map[string]SubmissionPredicateRegistrationInput, len(input.PredicateRegistrations))
		for _, registration := range input.PredicateRegistrations {
			registrations[registration.RelationshipRef] = registration
		}
		seenHashes := make(map[string]struct{}, len(items)+len(input.RelationshipObservations))
		add := func(sourceKind, sourceKey, text string) error {
			text = strings.TrimSpace(text)
			if text == "" {
				return fmt.Errorf("%w: empty %s document", ErrInlineEmbeddingPlanMismatch, sourceKind)
			}
			documentHash := searchDocumentHash(text)
			if _, exists := seenHashes[documentHash]; exists {
				return nil
			}
			seenHashes[documentHash] = struct{}{}
			plan.Documents = append(plan.Documents, SearchDocumentForEmbedding{
				SearchDocumentResult: SearchDocumentResult{
					TeamID: input.TeamID, SearchDocumentID: "plan:" + sourceKey,
					OwnerProfileID: input.OwnerProfileID, SourceKind: sourceKind,
					SourceID: uuid.NewString(), SourceVersion: 1,
					ProjectionFormat:    defaultProjectionFormat(sourceKind),
					EmbeddingContractID: contract.EmbeddingContractID,
					EmbeddingDimensions: contract.EmbeddingDimensions,
				},
				DocumentText: text, DocumentHash: documentHash,
			})
			return nil
		}
		for _, item := range items {
			if err := add("evidence", item.FragmentID, item.Fragment.Content); err != nil {
				return err
			}
		}
		for _, entry := range input.RelationshipObservations {
			observation := entry.Observation
			predicateKey := strings.TrimSpace(observation.PredicateKey)
			if registration, registered := registrations[observation.Ref]; registered {
				if predicateKey != "" {
					return fmt.Errorf("%w: predicate registration %q also supplied a provider predicate", ErrInlineEmbeddingPlanMismatch, observation.Ref)
				}
				predicateKey, err = previewSubmissionPredicateKey(ctx, tx, input, registration)
				if err != nil {
					return err
				}
			}
			if predicateKey == "" {
				return fmt.Errorf("%w: predicate for relationship %q is unresolved", ErrInlineEmbeddingPlanMismatch, entry.RelationshipRef)
			}
			subjectName := entityNames[observation.SubjectRef]
			if subjectName == "" {
				subjectName = observation.SubjectRef
			}
			objectName := ""
			if observation.ObjectRef != "" {
				objectName = entityNames[observation.ObjectRef]
				if objectName == "" {
					objectName = observation.ObjectRef
				}
			}
			valueType, value, unit := "", "", ""
			if observation.ObjectValue != nil {
				valueType = observation.ObjectValue.ValueType
				value = observation.ObjectValue.Display
				if value == "" {
					value = observation.ObjectValue.CanonicalValue
				}
				unit = observation.ObjectValue.Unit
				objectName = value
			}
			text := relationshipProjectionText(&RelationshipRecord{
				SubjectEntityID: observation.SubjectRef, PredicateKey: predicateKey,
				ObjectEntityID: observation.ObjectRef, Polarity: observation.Polarity,
				ScopeKey: observation.ScopeKey, ValidFrom: observation.ValidFrom, ValidTo: observation.ValidTo,
			}, relationshipProjectionNames{
				SubjectName: subjectName, ObjectName: objectName,
				ObjectValueType: valueType, ObjectValue: value, ObjectUnit: unit,
			})
			if err := add("relationship", fmt.Sprintf("%s:%d", entry.RelationshipRef, entry.SplitIndex), text); err != nil {
				return err
			}
		}
		if len(plan.Documents) > 256 {
			return fmt.Errorf("%w: more than 256 unique search documents", ErrInlineEmbeddingPlanTooLarge)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repository: plan submission embeddings: %w", err)
	}
	return plan, nil
}

func loadInlineEmbeddingEntityNames(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	resolutions []SubmissionAssessmentEntityResolutionInput,
) (map[string]string, error) {
	names := make(map[string]string, len(resolutions))
	for _, entry := range resolutions {
		ref := strings.TrimSpace(entry.Resolution.MentionRef)
		if ref == "" {
			continue
		}
		name := strings.TrimSpace(entry.Resolution.CanonicalName)
		if entry.Resolution.EntityID != "" {
			name = entry.Resolution.EntityID
			var canonical string
			err := tx.WithContext(ctx).Raw(`
				SELECT display_name
				FROM entity_names
				WHERE team_id = ?::uuid
				  AND entity_id = ?::uuid
				  AND name_kind = 'canonical'
				  AND valid_to IS NULL
				ORDER BY created_at DESC, entity_name_id DESC
				LIMIT 1
			`, teamID, entry.Resolution.EntityID).Row().Scan(&canonical)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if strings.TrimSpace(canonical) != "" {
				name = strings.TrimSpace(canonical)
			}
		}
		if previous, exists := names[ref]; exists && previous != name {
			return nil, fmt.Errorf("%w: entity %q has conflicting names", ErrInlineEmbeddingPlanMismatch, ref)
		}
		names[ref] = name
	}
	return names, nil
}

func searchDocumentHash(text string) string {
	return strings.TrimPrefix(sha256Hex(strings.TrimSpace(text)), "sha256:")
}

func applyInlineSubmissionEmbeddings(
	ctx context.Context,
	tx *gorm.DB,
	input CommitSubmissionAssessmentInput,
	semanticResult *submissionSemanticCommitState,
	inlineEmbeddings []InlineEmbeddingResult,
) error {
	searchDocumentIDs := make([]string, 0, len(semanticResult.SearchDocuments))
	for _, document := range semanticResult.SearchDocuments {
		if document.SearchState == string(domain.SearchProjectionPending) || document.SearchState == string(domain.SearchProjectionFailed) {
			searchDocumentIDs = append(searchDocumentIDs, document.SearchDocumentID)
		}
	}
	if len(searchDocumentIDs) == 0 {
		return nil
	}
	completedIDs, err := completeInlineEmbeddingResultsInTx(ctx, tx, input.TeamID, input.OwnerProfileID, searchDocumentIDs, inlineEmbeddings)
	if err != nil {
		return err
	}
	for index := range semanticResult.SearchDocuments {
		if _, ok := completedIDs[semanticResult.SearchDocuments[index].SearchDocumentID]; ok {
			semanticResult.SearchDocuments[index].SearchState = string(domain.SearchProjectionCurrent)
		}
	}
	return nil
}

// previewSubmissionPredicateKey mirrors the commit resolver without creating
// a predicate definition or registration event. A concurrent registration
// change is still fenced by the final document hash during commit.
func previewSubmissionPredicateKey(
	ctx context.Context,
	tx *gorm.DB,
	input CommitSubmissionAssessmentInput,
	registration SubmissionPredicateRegistrationInput,
) (string, error) {
	requestedKey := strings.TrimSpace(registration.PredicateKey)
	canonicalKey := canonicalGeneratedPredicateKey(requestedKey)
	loaded, err := loadLatestSubmissionPredicate(ctx, tx, input.TeamID, requestedKey, canonicalKey)
	if err == nil && loaded != nil {
		if loaded.LifecycleState != string(domain.PredicateLifecycleActive) ||
			!placementPredicateKindAllowed(loaded.AllowedSubjectKinds, registration.SubjectKind) ||
			!placementPredicateKindAllowed(loaded.AllowedObjectKinds, registration.ObjectKind) ||
			loaded.RelationshipKind != registration.RelationshipKind ||
			loaded.CurrentCardinality != registration.CurrentCardinality {
			return "", ErrSubmissionPredicateRegistrationHeld
		}
		return loaded.PredicateKey, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return canonicalKey, nil
}

func completeInlineEmbeddingResultsInTx(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerID string,
	searchDocumentIDs []string,
	inlineEmbeddings []InlineEmbeddingResult,
) (map[string]struct{}, error) {
	byHash := make(map[string][]float32, len(inlineEmbeddings))
	for _, embedding := range inlineEmbeddings {
		hash := strings.TrimSpace(embedding.DocumentHash)
		if hash == "" || len(embedding.Embedding) == 0 {
			return nil, fmt.Errorf("%w: provider result has no document hash or vector", ErrInlineEmbeddingPlanMismatch)
		}
		if _, exists := byHash[hash]; exists {
			return nil, fmt.Errorf("%w: duplicate provider result for document hash", ErrInlineEmbeddingPlanMismatch)
		}
		byHash[hash] = append([]float32(nil), embedding.Embedding...)
	}
	documents, err := loadSearchDocumentsForEmbeddingTx(ctx, tx, LoadSearchDocumentsForEmbeddingInput{
		TeamID: teamID, OwnerProfileID: ownerID, SearchDocumentIDs: searchDocumentIDs,
	})
	if err != nil {
		return nil, err
	}
	if len(documents) > 256 {
		return nil, fmt.Errorf("%w: more than 256 rendered search documents", ErrInlineEmbeddingPlanMismatch)
	}
	completed := make([]SearchDocumentEmbedding, 0, len(documents))
	for _, document := range documents {
		vector, ok := byHash[document.DocumentHash]
		if !ok {
			return nil, fmt.Errorf("%w: rendered document hash %q was not embedded", ErrInlineEmbeddingPlanMismatch, document.DocumentHash)
		}
		if len(vector) != document.EmbeddingDimensions {
			return nil, fmt.Errorf("%w: vector dimensions do not match rendered document", ErrInlineEmbeddingPlanMismatch)
		}
		completed = append(completed, SearchDocumentEmbedding{
			SearchDocumentID: document.SearchDocumentID, DocumentHash: document.DocumentHash,
			SourceVersion: document.SourceVersion, ProjectionFormat: document.ProjectionFormat,
			ProjectionGenerationID: document.ProjectionGenerationID, DocumentVersion: document.DocumentVersion,
			EmbeddingContractID: document.EmbeddingContractID, EmbeddingDimensions: document.EmbeddingDimensions,
			Embedding: vector, SpaceID: document.SpaceID, SpaceGeneration: document.SpaceGeneration,
		})
	}
	if err := completeSearchDocumentsWithEmbeddingsInTx(ctx, tx, CompleteSearchDocumentsWithEmbeddingsInput{
		TeamID: teamID, OwnerProfileID: ownerID, Documents: completed,
	}); err != nil {
		return nil, err
	}
	completedIDs := make(map[string]struct{}, len(completed))
	for _, embedding := range completed {
		completedIDs[embedding.SearchDocumentID] = struct{}{}
	}
	return completedIDs, nil
}
