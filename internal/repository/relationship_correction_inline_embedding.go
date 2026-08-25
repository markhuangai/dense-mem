package repository

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// RelationshipCorrectionEmbeddingPlan is rendered before the correction
// transaction. Awaiting-confirmation, rejected, and replayed corrections may
// return an empty plan; the caller can then execute the deterministic result
// without a provider call.
type RelationshipCorrectionEmbeddingPlan struct {
	Documents []SearchDocumentForEmbedding
}

// PlanRelationshipCorrectionEmbeddings renders the successor Relationship
// projection without applying the correction or invoking a provider.
func (r *SemanticRepositoryImpl) PlanRelationshipCorrectionEmbeddings(
	ctx context.Context,
	input CorrectRelationshipInput,
) (*RelationshipCorrectionEmbeddingPlan, error) {
	input = normalizeCorrectRelationshipInput(input)
	if err := validateCorrectRelationshipInput(input); err != nil {
		return nil, err
	}
	plan := &RelationshipCorrectionEmbeddingPlan{Documents: []SearchDocumentForEmbedding{}}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		effective := input
		var pendingConfirmation *relationshipCorrectionSubmissionRow
		if input.Action == "submit" {
			requestHash, err := relationshipCorrectionRequestHash(input)
			if err != nil {
				return err
			}
			existing, err := loadRelationshipCorrectionByIdempotency(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey)
			if err == nil {
				if existing.RequestHash != requestHash || existing.ProcessingState != "processing" {
					return nil
				}
				effective.RelationshipID = existing.RelationshipID
				effective.ExpectedVersion = existing.ExpectedVersion
				effective.Patch = existing.Patch
				effective.Supports = append([]RelationshipCorrectionSupport(nil), existing.Supports...)
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		} else {
			row, err := loadRelationshipCorrectionSubmission(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, false)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if row.ProcessingState != "awaiting_confirmation" {
				return nil
			}
			pendingConfirmation = row
			effective.RelationshipID = row.RelationshipID
			effective.ExpectedVersion = row.ExpectedVersion
			effective.Patch = row.Patch
			effective.Supports = append([]RelationshipCorrectionSupport(nil), row.Supports...)
			effective.Selection = mergeRelationshipCorrectionSelection(row.Selection, input.Selection)
		}

		source, err := loadRelationshipRecord(ctx, tx, input.TeamID, effective.RelationshipID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if source.OwnerProfileID != input.OwnerProfileID || source.Status != string(domain.RelationshipStatusActive) || source.SupportCount == 0 || source.IdentityAliasOfID != "" {
			return nil
		}

		resolution, err := resolveRelationshipCorrectionPatch(ctx, tx, effective, source)
		if err != nil {
			return err
		}
		if resolution.RejectionCode != "" {
			return nil
		}
		if input.Action == "submit" && len(resolution.Candidates) > 0 {
			return nil
		}
		if pendingConfirmation != nil {
			if pendingConfirmation.ConfirmationExpiresAt == nil || !time.Now().UTC().Before(*pendingConfirmation.ConfirmationExpiresAt) {
				return nil
			}
			if subtle.ConstantTimeCompare([]byte(pendingConfirmation.ConfirmationToken), []byte(input.ConfirmationToken)) != 1 {
				return nil
			}
			if _, err := validateRelationshipCorrectionSelection(pendingConfirmation, effective.Selection); err != nil {
				return nil
			}
			resolution.Selection = mergeRelationshipCorrectionSelection(resolution.Selection, effective.Selection)
			if err := validateSelectedCorrectionEntities(ctx, tx, input.TeamID, pendingConfirmation.Candidates, resolution.Selection); err != nil {
				if errors.Is(err, errRelationshipCorrectionSelectionUnavailable) {
					return nil
				}
				return err
			}
		}

		projectionNames, err := loadCorrectionProjectionNames(ctx, tx, input.TeamID, source)
		if err != nil {
			return err
		}
		subjectPatch := effective.Patch.SubjectEntity
		if effective.Action == "confirm" && effective.Selection.SubjectEntityID != "" {
			subjectPatch = &RelationshipCorrectionEntityPatch{EntityID: effective.Selection.SubjectEntityID}
		}
		objectPatch := effective.Patch.ObjectEntity
		if effective.Action == "confirm" && effective.Selection.ObjectEntityID != "" {
			objectPatch = &RelationshipCorrectionEntityPatch{EntityID: effective.Selection.ObjectEntityID}
		}
		subjectID, subjectName, err := correctionEndpointProjection(ctx, tx, input.TeamID, "subject_entity", source.SubjectEntityID, projectionNames.SubjectName, subjectPatch)
		if err != nil {
			return err
		}
		objectID, objectName, objectValueID, objectValue, objectUnit, err := correctionObjectProjection(ctx, tx, input.TeamID, source, projectionNames, objectPatch)
		if err != nil {
			return err
		}
		if resolution.Predicate == nil {
			return nil
		}
		predicateKey := resolution.Predicate.Key
		if subjectID == source.SubjectEntityID && objectID == source.ObjectEntityID &&
			objectValueID == source.ObjectValueID && predicateKey == source.PredicateKey &&
			resolution.SubjectCreate == nil && resolution.ObjectCreate == nil {
			return nil
		}
		contract, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		successor := &RelationshipRecord{
			SubjectEntityID: subjectID, PredicateKey: predicateKey, ObjectEntityID: objectID,
			ObjectValueID: objectValueID, Polarity: source.Polarity, ScopeKey: source.ScopeKey,
			ValidFrom: source.ValidFrom, ValidTo: source.ValidTo,
		}
		text := relationshipProjectionText(successor, relationshipProjectionNames{
			SubjectName: subjectName, ObjectName: objectName, ObjectValue: objectValue, ObjectUnit: objectUnit,
		})
		plan.Documents = append(plan.Documents, SearchDocumentForEmbedding{
			SearchDocumentResult: SearchDocumentResult{
				TeamID: input.TeamID, SearchDocumentID: "plan:correction", OwnerProfileID: input.OwnerProfileID,
				SourceKind: "relationship", SourceID: uuid.NewString(), SourceVersion: int64(source.Version + 1),
				ProjectionFormat: 2, EmbeddingContractID: contract.EmbeddingContractID,
				EmbeddingDimensions: contract.EmbeddingDimensions,
			},
			DocumentText: text, DocumentHash: searchDocumentHash(text),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repository: plan relationship correction embeddings: %w", err)
	}
	return plan, nil
}

func loadCorrectionProjectionNames(ctx context.Context, tx *gorm.DB, teamID string, source *RelationshipRecord) (relationshipProjectionNames, error) {
	var names relationshipProjectionNames
	err := tx.WithContext(ctx).Raw(`
		SELECT
		    COALESCE(NULLIF(subject_name.display_name, ''), relationship.subject_entity_id::text),
		    COALESCE(
		        NULLIF(object_name.display_name, ''),
		        NULLIF(value_record.display, ''),
		        NULLIF(value_record.canonical_value, ''),
		        relationship.object_entity_id::text,
		        relationship.object_value_id::text,
		        ''
		    ),
		    COALESCE(value_record.value_type, ''),
		    COALESCE(value_record.canonical_value, ''),
		    COALESCE(value_record.unit, '')
		FROM relationship_records AS relationship
		LEFT JOIN entity_names AS subject_name
		  ON subject_name.team_id = relationship.team_id
		 AND subject_name.entity_id = relationship.subject_entity_id
		 AND subject_name.name_kind = 'canonical'
		 AND subject_name.valid_to IS NULL
		LEFT JOIN entity_names AS object_name
		  ON object_name.team_id = relationship.team_id
		 AND object_name.entity_id = relationship.object_entity_id
		 AND object_name.name_kind = 'canonical'
		 AND object_name.valid_to IS NULL
		LEFT JOIN value_records AS value_record
		  ON value_record.team_id = relationship.team_id
		 AND value_record.value_id = relationship.object_value_id
		WHERE relationship.team_id = ?::uuid
		  AND relationship.relationship_id = ?::uuid
		LIMIT 1
	`, teamID, source.RelationshipID).Row().Scan(
		&names.SubjectName, &names.ObjectName, &names.ObjectValueType, &names.ObjectValue, &names.ObjectUnit,
	)
	return names, err
}

func correctionEndpointProjection(
	ctx context.Context,
	tx *gorm.DB,
	teamID, endpoint, currentID, currentName string,
	patch *RelationshipCorrectionEntityPatch,
) (string, string, error) {
	if patch == nil {
		return currentID, currentName, nil
	}
	if patch.EntityID != "" {
		candidate, err := loadActiveCorrectionEntity(ctx, tx, teamID, patch.EntityID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", patch.EntityID, nil
		}
		if err != nil {
			return "", "", err
		}
		return candidate.EntityID, candidate.CanonicalName, nil
	}
	candidates, err := listExactCorrectionEntityCandidates(ctx, tx, teamID, endpoint, patch.Name, patch.EntityKind)
	if err != nil {
		return "", "", err
	}
	if len(candidates) == 1 {
		return candidates[0].EntityID, candidates[0].CanonicalName, nil
	}
	return "", patch.Name, nil
}

func correctionObjectProjection(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	source *RelationshipRecord,
	names relationshipProjectionNames,
	patch *RelationshipCorrectionEntityPatch,
) (string, string, string, string, string, error) {
	if patch != nil {
		id, name, err := correctionEndpointProjection(ctx, tx, teamID, "object_entity", source.ObjectEntityID, names.ObjectName, patch)
		return id, name, "", "", "", err
	}
	if source.ObjectEntityID != "" {
		return source.ObjectEntityID, names.ObjectName, "", "", "", nil
	}
	return "", firstNonEmpty(names.ObjectName, names.ObjectValue), source.ObjectValueID, names.ObjectValue, names.ObjectUnit, nil
}
