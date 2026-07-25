package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (r *V2LedgerRepositoryImpl) ValidateV2RelationshipConflictContext(
	ctx context.Context,
	input V2ValidateRelationshipConflictContextInput,
) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.ConflictID = strings.TrimSpace(input.ConflictID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.ConflictID); err != nil {
		return fmt.Errorf("conflict_id is required: %w", err)
	}
	if input.ExpectedVersion < 1 {
		return errors.New("expected_version must be greater than zero")
	}
	return r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		return requireV2RelationshipConflictContextCurrent(ctx, tx, input.TeamID, input.ConflictID, input.ExpectedVersion)
	})
}

func ensureV2RelationshipConflictContextsCurrent(
	ctx context.Context,
	tx *gorm.DB,
	input V2CommitPlacementSemanticInput,
) error {
	for _, observation := range input.RelationshipObservations {
		if observation.ConflictContext == nil {
			continue
		}
		if err := requireV2RelationshipConflictContextCurrent(ctx, tx, input.TeamID, observation.ConflictContext.ConflictID, observation.ConflictContext.ExpectedVersion); err != nil {
			return err
		}
	}
	return nil
}

func requireV2RelationshipConflictContextCurrent(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	expectedVersion int,
) error {
	var found bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND version = ?
			  AND status IN ('open', 'overdue')
		)
	`, teamID, conflictID, expectedVersion).Scan(&found).Error; err != nil {
		return err
	}
	if !found {
		return ErrV2ConflictContextStale
	}
	return nil
}

func requireV2RelationshipConflictContextMatchesDecision(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	context V2PlacementConflictContextInput,
	decision V2ApplyRelationshipDecisionInput,
) error {
	var found bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM relationship_conflict_cases AS conflict
			JOIN team_predicate_definitions AS predicate
			  ON predicate.team_id = conflict.team_id
			 AND predicate.predicate_key = ?
			 AND predicate.version = ?
			 AND predicate.lifecycle_state = 'active'
			 AND predicate.relationship_kind = conflict.relationship_kind
			 AND predicate.current_cardinality = conflict.current_cardinality
			WHERE conflict.team_id = ?::uuid
			  AND conflict.conflict_id = ?::uuid
			  AND conflict.version = ?
			  AND conflict.status IN ('open', 'overdue')
			  AND conflict.subject_entity_id = ?::uuid
			  AND conflict.predicate_key = ?
			  AND conflict.polarity = ?
			  AND conflict.scope_key IS NOT DISTINCT FROM NULLIF(?, '')
		)
	`, decision.PredicateKey, decision.PredicateVersion, teamID, context.ConflictID,
		context.ExpectedVersion, decision.SubjectEntityID, decision.PredicateKey,
		decision.Polarity, decision.ScopeKey).Scan(&found).Error; err != nil {
		return err
	}
	if !found {
		return ErrV2ConflictContextStale
	}
	return nil
}
