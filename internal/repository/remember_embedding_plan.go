package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// PlanRememberEmbeddings renders the exact document batch for a
// request-owned Remember. It performs reads only; no ingest, evidence,
// semantic, search, or attempt row is written before embedding succeeds.
func (r *LedgerRepositoryImpl) PlanRememberEmbeddings(
	ctx context.Context,
	input SynchronousRememberCommitInput,
) (*InlineEmbeddingPlan, error) {
	input = normalizeSynchronousRememberCommitInput(input)
	if err := validateSynchronousRememberCommitInput(input); err != nil {
		return nil, fmt.Errorf("%w: invalid synchronous Remember input: %w", ErrInlineEmbeddingPlanMismatch, err)
	}
	plan := &InlineEmbeddingPlan{Documents: []SearchDocumentForEmbedding{}}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		contract, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		plan.EmbeddingContractID = contract.EmbeddingContractID
		plan.EmbeddingDimensions = contract.EmbeddingDimensions
		plan.EmbeddingModel = contract.EmbeddingModel
		plan.SearchIndexGenerationID = contract.SearchIndexGenerationID
		plan.IndexGeneration = contract.IndexGeneration
		entityNames, err := rememberEntityNames(ctx, tx, input)
		if err != nil {
			return err
		}
		registrations := make(map[string]SubmissionPredicateRegistrationInput, len(input.Commit.PredicateRegistrations))
		for _, registration := range input.Commit.PredicateRegistrations {
			registrations[registration.RelationshipRef] = registration
		}
		seenHashes := map[string]struct{}{}
		add := func(sourceKind, sourceID, text string) error {
			text = strings.TrimSpace(text)
			if text == "" {
				return fmt.Errorf("%w: empty %s document", ErrInlineEmbeddingPlanMismatch, sourceKind)
			}
			hash := searchDocumentHash(text)
			if _, exists := seenHashes[hash]; exists {
				return nil
			}
			seenHashes[hash] = struct{}{}
			plan.Documents = append(plan.Documents, SearchDocumentForEmbedding{
				SearchDocumentResult: SearchDocumentResult{
					TeamID: input.TeamID, SearchDocumentID: "plan:" + sourceID,
					OwnerProfileID: input.OwnerProfileID, SourceKind: sourceKind, SourceID: sourceID,
					SourceVersion: 1, ProjectionFormat: defaultProjectionFormat(sourceKind),
					EmbeddingContractID: contract.EmbeddingContractID,
					EmbeddingDimensions: contract.EmbeddingDimensions,
				},
				DocumentText: text, DocumentHash: hash,
			})
			return nil
		}
		for index, evidence := range input.Evidence {
			if rememberEvidenceDuplicateWasPreassessed(input, index) {
				continue
			}
			if err := add("evidence", evidence.FragmentID, evidence.Content); err != nil {
				return err
			}
		}
		for _, entry := range input.Commit.RelationshipObservations {
			observation := entry.Observation
			predicateKey := strings.TrimSpace(observation.PredicateKey)
			if registration, ok := registrations[observation.Ref]; ok {
				if predicateKey != "" {
					return fmt.Errorf("%w: predicate registration %q also supplied a provider predicate", ErrInlineEmbeddingPlanMismatch, observation.Ref)
				}
				predicateKey, err = previewSubmissionPredicateKey(ctx, tx, input.Commit, registration)
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
				projected, err := loadInlineEmbeddingValueProjection(ctx, tx, input.TeamID, *observation.ObjectValue)
				if err != nil {
					return err
				}
				valueType = projected.ValueType
				value = projected.Display
				if value == "" {
					value = projected.CanonicalValue
				}
				unit = projected.Unit
				objectName = value
			}
			text := relationshipProjectionText(&RelationshipRecord{
				SubjectEntityID: observation.SubjectRef, PredicateKey: predicateKey,
				ObjectEntityID: observation.ObjectRef, Polarity: observation.Polarity,
				ScopeKey: observation.ScopeKey, ValidFrom: observation.ValidFrom, ValidTo: observation.ValidTo,
			}, relationshipProjectionNames{SubjectName: subjectName, ObjectName: objectName, ObjectValueType: valueType, ObjectValue: value, ObjectUnit: unit})
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
		return nil, fmt.Errorf("repository: plan Remember embeddings: %w", err)
	}
	return plan, nil
}

func rememberEvidenceDuplicateWasPreassessed(input SynchronousRememberCommitInput, evidenceIndex int) bool {
	for _, resolution := range input.DuplicateResolutions {
		if resolution.EvidenceIndex == evidenceIndex {
			if resolution.Exact {
				return true
			}
			if evidenceIndex < 0 || evidenceIndex >= len(input.Evidence) {
				return false
			}
			return rememberEvidenceRequiresDuplicateAssessment(input.Evidence[evidenceIndex])
		}
	}
	return false
}
func rememberEntityNames(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput) (map[string]string, error) {
	names := make(map[string]string, len(input.Commit.EntityResolutions))
	for _, entry := range input.Commit.EntityResolutions {
		ref := strings.TrimSpace(entry.Resolution.MentionRef)
		if ref == "" {
			continue
		}
		name := strings.TrimSpace(entry.Resolution.CanonicalName)
		if entry.Resolution.EntityID != "" {
			var canonical string
			err := tx.WithContext(ctx).Raw(`
				SELECT display_name FROM entity_names
				WHERE team_id = ?::uuid AND entity_id = ?::uuid AND name_kind = 'canonical' AND valid_to IS NULL
				ORDER BY created_at DESC, entity_name_id DESC LIMIT 1
			`, input.TeamID, entry.Resolution.EntityID).Row().Scan(&canonical)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if strings.TrimSpace(canonical) != "" {
				name = strings.TrimSpace(canonical)
			}
		}
		if name == "" {
			name = ref
		}
		names[ref] = name
	}
	return names, nil
}
