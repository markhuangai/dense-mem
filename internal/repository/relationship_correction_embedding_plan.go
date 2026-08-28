package repository

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// PlanRelationshipCorrectionEmbeddings renders only the provider work that a
// currently valid correction needs. It never writes semantic or search state.
func (r *SemanticRepositoryImpl) PlanRelationshipCorrectionEmbeddings(
	ctx context.Context,
	input CorrectRelationshipInput,
) (*RelationshipCorrectionEmbeddingPlan, error) {
	input = normalizeCorrectRelationshipInput(input)
	if err := validateCorrectRelationshipInput(input); err != nil {
		return nil, err
	}
	plan := &RelationshipCorrectionEmbeddingPlan{Documents: []RelationshipCorrectionEmbeddingDocument{}}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		effective := input
		var pending *relationshipCorrectionSubmissionRow
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
			if err != nil || row.ProcessingState != "awaiting_confirmation" {
				return err
			}
			pending = row
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
		if source.OwnerProfileID != input.OwnerProfileID || source.Status != string(domain.RelationshipStatusActive) ||
			source.SupportCount == 0 || source.IdentityAliasOfID != "" || source.Version != effective.ExpectedVersion {
			return nil
		}
		supports, err := loadEffectiveRelationshipCorrectionSupports(ctx, tx, input.TeamID, source.RelationshipID)
		if err != nil {
			return err
		}
		if !relationshipCorrectionSupportsEqual(effective.Supports, supports) {
			return nil
		}
		for _, support := range supports {
			if requireSemanticSpaceMatch(source.SpaceID, support.SpaceID) != nil {
				return nil
			}
		}

		resolution, err := resolveRelationshipCorrectionPatch(ctx, tx, effective, source)
		if err != nil || resolution.RejectionCode != "" || (input.Action == "submit" && len(resolution.Candidates) > 0) {
			return err
		}
		if pending != nil {
			if pending.ConfirmationExpiresAt == nil || !time.Now().UTC().Before(*pending.ConfirmationExpiresAt) ||
				subtle.ConstantTimeCompare([]byte(pending.ConfirmationToken), []byte(input.ConfirmationToken)) != 1 {
				return nil
			}
			if _, err := validateRelationshipCorrectionSelection(pending, input.Selection); err != nil {
				return nil
			}
			resolution.Selection = mergeRelationshipCorrectionSelection(resolution.Selection, effective.Selection)
			if err := validateSelectedCorrectionEntities(ctx, tx, input.TeamID, pending.Candidates, resolution.Selection); err != nil {
				if errors.Is(err, errRelationshipCorrectionSelectionUnavailable) {
					return nil
				}
				return err
			}
		}

		names, err := loadCorrectionProjectionNames(ctx, tx, input.TeamID, source)
		if err != nil {
			return err
		}
		subjectPatch := effective.Patch.SubjectEntity
		objectPatch := effective.Patch.ObjectEntity
		if resolution.Selection.SubjectEntityID != "" {
			subjectPatch = &RelationshipCorrectionEntityPatch{EntityID: resolution.Selection.SubjectEntityID}
		}
		if resolution.Selection.ObjectEntityID != "" {
			objectPatch = &RelationshipCorrectionEntityPatch{EntityID: resolution.Selection.ObjectEntityID}
		}
		subjectID, subjectName, err := correctionEndpointProjection(ctx, tx, input.TeamID, source.SubjectEntityID, names.SubjectName, subjectPatch)
		if err != nil {
			return err
		}
		objectID, objectName, objectValueID, objectValue, objectUnit, err := correctionObjectProjection(ctx, tx, input.TeamID, source, names, objectPatch)
		if err != nil {
			return err
		}
		if resolution.Predicate == nil {
			return nil
		}
		if subjectID == source.SubjectEntityID && objectID == source.ObjectEntityID && objectValueID == source.ObjectValueID &&
			resolution.Predicate.Key == source.PredicateKey && resolution.Predicate.Version == source.PredicateVersion &&
			resolution.SubjectCreate == nil && resolution.ObjectCreate == nil {
			return nil
		}
		contract, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		plan.EmbeddingContractID = contract.EmbeddingContractID
		plan.EmbeddingDimensions = contract.EmbeddingDimensions
		plan.EmbeddingModel = contract.EmbeddingModel
		plan.SearchIndexGenerationID = contract.SearchIndexGenerationID
		plan.IndexGeneration = contract.IndexGeneration
		successor := &RelationshipRecord{SubjectEntityID: subjectID, PredicateKey: resolution.Predicate.Key, ObjectEntityID: objectID, ObjectValueID: objectValueID, Polarity: source.Polarity, ScopeKey: source.ScopeKey, ValidFrom: source.ValidFrom, ValidTo: source.ValidTo}
		appendCorrectionPlanDocument(plan, relationshipProjectionText(source, names))
		appendCorrectionPlanDocument(plan, relationshipProjectionText(successor, relationshipProjectionNames{SubjectName: subjectName, ObjectName: objectName, ObjectValue: objectValue, ObjectUnit: objectUnit}))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repository: plan relationship correction embeddings: %w", err)
	}
	return plan, nil
}

func appendCorrectionPlanDocument(plan *RelationshipCorrectionEmbeddingPlan, text string) {
	text = strings.TrimSpace(text)
	if plan == nil || text == "" {
		return
	}
	hash := strings.TrimPrefix(sha256Hex(text), "sha256:")
	for _, document := range plan.Documents {
		if document.DocumentHash == hash {
			return
		}
	}
	plan.Documents = append(plan.Documents, RelationshipCorrectionEmbeddingDocument{DocumentHash: hash, DocumentText: text})
}

func loadCorrectionProjectionNames(ctx context.Context, tx *gorm.DB, teamID string, source *RelationshipRecord) (relationshipProjectionNames, error) {
	var names relationshipProjectionNames
	err := tx.WithContext(ctx).Raw(`
		SELECT COALESCE(NULLIF(subject_name.display_name, ''), relationship.subject_entity_id::text),
		       COALESCE(NULLIF(object_name.display_name, ''), NULLIF(value_record.display, ''), NULLIF(value_record.canonical_value, ''), relationship.object_entity_id::text, relationship.object_value_id::text, ''),
		       COALESCE(value_record.value_type, ''), COALESCE(value_record.canonical_value, ''), COALESCE(value_record.unit, '')
		FROM relationship_records AS relationship
		LEFT JOIN entity_names AS subject_name ON subject_name.team_id = relationship.team_id AND subject_name.entity_id = relationship.subject_entity_id AND subject_name.name_kind = 'canonical' AND subject_name.valid_to IS NULL
		LEFT JOIN entity_names AS object_name ON object_name.team_id = relationship.team_id AND object_name.entity_id = relationship.object_entity_id AND object_name.name_kind = 'canonical' AND object_name.valid_to IS NULL
		LEFT JOIN value_records AS value_record ON value_record.team_id = relationship.team_id AND value_record.value_id = relationship.object_value_id
		WHERE relationship.team_id = ?::uuid AND relationship.relationship_id = ?::uuid LIMIT 1
	`, teamID, source.RelationshipID).Row().Scan(&names.SubjectName, &names.ObjectName, &names.ObjectValueType, &names.ObjectValue, &names.ObjectUnit)
	return names, err
}

func correctionEndpointProjection(ctx context.Context, tx *gorm.DB, teamID, currentID, currentName string, patch *RelationshipCorrectionEntityPatch) (string, string, error) {
	if patch == nil {
		return currentID, currentName, nil
	}
	if patch.EntityID == "" {
		return "", patch.Name, nil
	}
	candidate, err := loadActiveCorrectionEntity(ctx, tx, teamID, patch.EntityID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", patch.EntityID, nil
	}
	if err != nil {
		return "", "", err
	}
	return candidate.EntityID, candidate.CanonicalName, nil
}

func correctionObjectProjection(ctx context.Context, tx *gorm.DB, teamID string, source *RelationshipRecord, names relationshipProjectionNames, patch *RelationshipCorrectionEntityPatch) (string, string, string, string, string, error) {
	if patch != nil {
		id, name, err := correctionEndpointProjection(ctx, tx, teamID, source.ObjectEntityID, names.ObjectName, patch)
		return id, name, "", "", "", err
	}
	if source.ObjectEntityID != "" {
		return source.ObjectEntityID, names.ObjectName, "", "", "", nil
	}
	return "", firstNonEmpty(names.ObjectName, names.ObjectValue), source.ObjectValueID, names.ObjectValue, names.ObjectUnit, nil
}
