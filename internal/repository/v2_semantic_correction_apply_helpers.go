package repository

import (
	"context"
	"strings"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func lockV2EntityCorrectionRelationships(
	ctx context.Context,
	tx *gorm.DB,
	plan *v2EntityCorrectionPlanRow,
) (map[string]int, error) {
	relationshipIDs := make([]string, 0, len(plan.AffectedRelationships))
	for _, impact := range plan.AffectedRelationships {
		relationshipIDs = append(relationshipIDs, impact.RelationshipID)
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT relationship_id::text, version
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND relationship_id = ANY(?::uuid[])
		FOR UPDATE
	`, plan.TeamID, plan.OwnerProfileID, pq.Array(relationshipIDs)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := map[string]int{}
	for rows.Next() {
		var relationshipID string
		var version int
		if err := rows.Scan(&relationshipID, &version); err != nil {
			return nil, err
		}
		versions[relationshipID] = version
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(versions) != len(relationshipIDs) {
		return nil, ErrV2SemanticCorrectionPlanStale
	}
	return versions, nil
}

func rejectV2MergeConflictsAtApply(
	ctx context.Context,
	tx *gorm.DB,
	plan *v2EntityCorrectionPlanRow,
	replacementEntityID string,
) error {
	observations, _, err := loadV2EntityCorrectionObservations(ctx, tx, V2CorrectEntityResolutionInput{
		TeamID:         plan.TeamID,
		OwnerProfileID: plan.OwnerProfileID,
		Action:         plan.Action,
		SourceEntityID: plan.SourceEntityID,
		TargetEntityID: replacementEntityID,
	})
	if err != nil {
		return err
	}
	impactSet := map[string]struct{}{}
	for _, impact := range plan.AffectedRelationships {
		impactSet[impact.RelationshipID] = struct{}{}
	}
	for _, observation := range observations {
		if _, ok := impactSet[observation.RelationshipID]; !ok {
			continue
		}
		_, conflict, err := v2MergeConflictKey(ctx, tx, V2CorrectEntityResolutionInput{
			TeamID:         plan.TeamID,
			OwnerProfileID: plan.OwnerProfileID,
			Action:         plan.Action,
			SourceEntityID: plan.SourceEntityID,
			TargetEntityID: replacementEntityID,
		}, observation)
		if err != nil {
			return err
		}
		if conflict {
			return ErrV2SemanticCorrectionPlanStale
		}
	}
	return nil
}

func updateV2EntityCorrectionRelationships(
	ctx context.Context,
	tx *gorm.DB,
	plan *v2EntityCorrectionPlanRow,
	replacementEntityID string,
) error {
	metadata, err := marshalV2JSON(map[string]any{
		"entity_correction_plan":   plan.PlanToken,
		"entity_correction_action": plan.Action,
	})
	if err != nil {
		return err
	}
	for _, impact := range plan.AffectedRelationships {
		result := tx.WithContext(ctx).Exec(`
			UPDATE relationship_records
			SET subject_entity_id = CASE
			        WHEN subject_entity_id = ?::uuid THEN ?::uuid
			        ELSE subject_entity_id
			    END,
			    object_entity_id = CASE
			        WHEN object_entity_id = ?::uuid THEN ?::uuid
			        ELSE object_entity_id
			    END,
			    semantic_group_key = 'sg:' || encode(digest(
			        concat_ws(chr(31),
			            CASE WHEN subject_entity_id = ?::uuid THEN ?::text ELSE subject_entity_id::text END,
			            predicate_key,
			            CASE
			                WHEN object_entity_id = ?::uuid THEN 'entity:' || ?::text
			                WHEN object_entity_id IS NOT NULL THEN 'entity:' || object_entity_id::text
			                ELSE 'value:' || object_value_id::text
			            END,
			            polarity,
			            COALESCE(scope_key, ''),
			            COALESCE(to_json(valid_from)::text, ''),
			            COALESCE(to_json(valid_to)::text, '')
			        ),
			        'sha256'
			    ), 'hex'),
			    metadata = metadata || ?::jsonb,
			    version = version + 1,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND relationship_id = ?::uuid
			  AND version = ?
		`, plan.SourceEntityID, replacementEntityID, plan.SourceEntityID, replacementEntityID,
			plan.SourceEntityID, replacementEntityID, plan.SourceEntityID, replacementEntityID,
			string(metadata), plan.TeamID, plan.OwnerProfileID,
			impact.RelationshipID, impact.Version)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrV2SemanticCorrectionPlanStale
		}
	}
	return nil
}

func createV2EntityCorrectionSplitEntity(
	ctx context.Context,
	tx *gorm.DB,
	plan *v2EntityCorrectionPlanRow,
) (string, error) {
	entityKind, fallbackName, err := loadV2EntityKindAndName(ctx, tx, plan.TeamID, plan.SourceEntityID)
	if err != nil {
		return "", err
	}
	displayName, err := loadV2CorrectionSplitDisplayName(ctx, tx, plan)
	if err != nil {
		return "", err
	}
	if displayName == "" {
		displayName = fallbackName
	}
	if displayName == "" {
		displayName = "Split entity " + plan.PlanToken[:8]
	}
	identityContext, err := marshalV2JSON(map[string]any{
		"source_entity_id": plan.SourceEntityID,
		"correction_plan":  plan.PlanToken,
	})
	if err != nil {
		return "", err
	}
	metadata, err := marshalV2JSON(map[string]any{
		"created_by": "correct_entity_resolution",
	})
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO entity_records (team_id, entity_kind, identity_context, metadata)
		VALUES (?::uuid, ?, ?::jsonb, ?::jsonb)
		RETURNING entity_id::text
	`, plan.TeamID, entityKind, string(identityContext), string(metadata)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var entityID string
	if err := rows.Scan(&entityID); err != nil {
		return "", err
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if _, err := insertV2EntityName(ctx, tx, V2AddEntityNameInput{
		TeamID:         plan.TeamID,
		OwnerProfileID: plan.OwnerProfileID,
		EntityID:       entityID,
		DisplayName:    displayName,
		NameKind:       "canonical",
	}); err != nil {
		return "", err
	}
	return entityID, nil
}

func loadV2EntityKindAndName(ctx context.Context, tx *gorm.DB, teamID, entityID string) (string, string, error) {
	row := tx.WithContext(ctx).Raw(`
		SELECT e.entity_kind,
		       COALESCE((
		           SELECT n.display_name
		           FROM entity_names AS n
		           WHERE n.team_id = e.team_id
		             AND n.entity_id = e.entity_id
		             AND n.name_kind = 'canonical'
		           ORDER BY n.created_at, n.entity_name_id
		           LIMIT 1
		       ), '')
		FROM entity_records AS e
		WHERE e.team_id = ?::uuid
		  AND e.entity_id = ?::uuid
	`, teamID, entityID).Row()
	var kind, name string
	if err := row.Scan(&kind, &name); err != nil {
		return "", "", err
	}
	return kind, name, nil
}

func ensureV2EntityExists(ctx context.Context, tx *gorm.DB, teamID, entityID string) error {
	var exists bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
		    SELECT 1 FROM entity_records
		    WHERE team_id = ?::uuid
		      AND entity_id = ?::uuid
		)
	`, teamID, entityID).Row().Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func loadV2CorrectionSplitDisplayName(ctx context.Context, tx *gorm.DB, plan *v2EntityCorrectionPlanRow) (string, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT subject_ref, object_ref, subject_entity_id::text, COALESCE(object_entity_id::text, '')
		FROM relationship_observations
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND observation_id = ANY(?::uuid[])
		ORDER BY created_at, observation_id
	`, plan.TeamID, plan.OwnerProfileID, pq.Array(plan.SelectedObservationIDs)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var subjectRef, objectRef, subjectEntityID, objectEntityID string
		if err := rows.Scan(&subjectRef, &objectRef, &subjectEntityID, &objectEntityID); err != nil {
			return "", err
		}
		switch {
		case subjectEntityID == plan.SourceEntityID && strings.TrimSpace(subjectRef) != "":
			return strings.TrimSpace(subjectRef), rows.Err()
		case objectEntityID == plan.SourceEntityID && strings.TrimSpace(objectRef) != "":
			return strings.TrimSpace(objectRef), rows.Err()
		}
	}
	return "", rows.Err()
}

func insertV2EntityCorrectionEvent(
	ctx context.Context,
	tx *gorm.DB,
	plan *v2EntityCorrectionPlanRow,
	replacementEntityID string,
) (string, error) {
	metadata, err := marshalV2JSON(map[string]any{
		"plan_token":             plan.PlanToken,
		"affected_relationships": plan.AffectedRelationships,
	})
	if err != nil {
		return "", err
	}
	survivorEntityID := ""
	newEntityID := ""
	if plan.Action == string(domain.V2EntityCorrectionMerge) {
		survivorEntityID = replacementEntityID
	} else {
		newEntityID = replacementEntityID
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO entity_correction_events (
		    team_id, owner_profile_id, action, survivor_entity_id, new_entity_id,
		    selected_observation_ids, reason, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid,
		    ?::uuid[], ?, ?::jsonb
		)
		RETURNING correction_event_id::text
	`, plan.TeamID, plan.OwnerProfileID, plan.Action, survivorEntityID, newEntityID,
		pq.Array(plan.SelectedObservationIDs), "correct_entity_resolution", string(metadata)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var correctionEventID string
	if err := rows.Scan(&correctionEventID); err != nil {
		return "", err
	}
	return correctionEventID, rows.Err()
}

func markV2EntityCorrectionPlanApplied(
	ctx context.Context,
	tx *gorm.DB,
	plan *v2EntityCorrectionPlanRow,
	correctionEventID string,
	newEntityID string,
) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE entity_correction_plans
		SET status = 'applied',
		    correction_event_id = ?::uuid,
		    new_entity_id = NULLIF(?, '')::uuid,
		    applied_at = now(),
		    impact_summary = ?
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND plan_token = ?::uuid
		  AND status = 'planned'
	`, correctionEventID, newEntityID,
		v2EntityCorrectionSummary(plan.Action, len(plan.AffectedRelationships), len(plan.SelectedObservationIDs), len(plan.BlockedObservationIDs), true),
		plan.TeamID, plan.OwnerProfileID, plan.PlanToken)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrV2SemanticCorrectionPlanStale
	}
	return nil
}
