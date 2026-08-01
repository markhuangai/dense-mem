package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func insertHypothesisDerivations(ctx context.Context, tx *gorm.DB, teamID, hypothesisID string, derivations []DreamDerivationSource) error {
	for _, derivation := range derivations {
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO hypothesis_derivation_sources (
			    team_id, hypothesis_id, premise_position, relationship_id,
			    relationship_version, support_id, observation_id, fragment_id,
			    source_id, source_revision_id, source_group_key, span_start,
			    span_end, quote, authority
			) VALUES (
			    ?::uuid, ?::uuid, ?, ?::uuid, ?, NULLIF(?, '')::uuid,
			    NULLIF(?, '')::uuid, ?::uuid, NULLIF(?, '')::uuid,
			    NULLIF(?, '')::uuid, ?, ?, ?, ?, ?
			)
		`, teamID, hypothesisID, derivation.PremisePosition, derivation.RelationshipID,
			derivation.RelationshipVersion, derivation.SupportID, derivation.ObservationID,
			derivation.FragmentID, derivation.SourceID, derivation.SourceRevisionID,
			derivation.SourceGroupKey, derivation.SpanStart, derivation.SpanEnd,
			derivation.Quote, derivation.Authority).Error; err != nil {
			return err
		}
	}
	return nil
}

func validateHypothesisSources(ctx context.Context, tx *gorm.DB, input UpsertHypothesisInput) error {
	if input.GeneratorKind == "evaluation_seed" {
		return nil
	}
	if len(input.SourceVersions) != 2 {
		return errors.New("dream hypotheses require exactly two source relationships")
	}
	available := make(map[string][]DreamEvidence, len(input.SourceVersions))
	for relationshipID, wantVersion := range input.SourceVersions {
		if _, err := uuid.Parse(relationshipID); err != nil {
			return fmt.Errorf("source relationship %q is invalid: %w", relationshipID, err)
		}
		var source DreamInput
		err := tx.WithContext(ctx).Raw(`
			SELECT relationship_id::text, version, status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
			  AND identity_alias_of_relationship_id IS NULL
		`, input.TeamID, relationshipID).Row().Scan(&source.RelationshipID, &source.Version, &source.Status)
		if err != nil {
			return staleDreamSourceError(err, relationshipID)
		}
		if source.Version != wantVersion {
			return fmt.Errorf("%w: %s", ErrDreamSourceStale, relationshipID)
		}
		eligible, err := dreamSourceRelationshipEligible(ctx, tx, input.TeamID, relationshipID, source.Status)
		if err != nil {
			return err
		}
		if !eligible {
			return fmt.Errorf("%w: %s", ErrDreamSourceStale, relationshipID)
		}
		evidence, err := listDreamInputEvidence(ctx, tx, input.TeamID, source)
		if err != nil {
			return err
		}
		if len(evidence) == 0 {
			return fmt.Errorf("%w: %s", ErrDreamSourceStale, relationshipID)
		}
		available[relationshipID] = evidence
	}

	covered := make(map[string]struct{}, len(input.SourceVersions))
	for index, derivation := range input.Derivations {
		if wantVersion, ok := input.SourceVersions[derivation.RelationshipID]; !ok || wantVersion != derivation.RelationshipVersion {
			return fmt.Errorf("derivations[%d] does not match a current source relationship", index)
		}
		if !dreamDerivationMatchesEvidence(derivation, available[derivation.RelationshipID]) {
			return fmt.Errorf("%w: %s", ErrDreamSourceStale, derivation.RelationshipID)
		}
		covered[derivation.RelationshipID] = struct{}{}
	}
	if len(covered) != len(input.SourceVersions) {
		return errors.New("dream derivations must cite evidence from both source relationships")
	}
	return nil
}

func dreamSourceRelationshipEligible(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
	status string,
) (bool, error) {
	if status != "active" && status != "pending_evidence" {
		return false, nil
	}
	var eligible bool
	err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM relationship_records relationship
			JOIN entity_records subject_entity
			  ON subject_entity.team_id = relationship.team_id
			 AND subject_entity.entity_id = relationship.subject_entity_id
			 AND subject_entity.status = 'active'
			LEFT JOIN entity_records object_entity
			  ON object_entity.team_id = relationship.team_id
			 AND object_entity.entity_id = relationship.object_entity_id
			 AND object_entity.status = 'active'
			WHERE relationship.team_id = ?::uuid
			  AND relationship.relationship_id = ?::uuid
			  AND relationship.identity_alias_of_relationship_id IS NULL
			  AND relationship.status = ?
			  AND (relationship.object_entity_id IS NULL OR object_entity.entity_id IS NOT NULL)
			  AND NOT EXISTS (
			      SELECT 1
			      FROM relationship_cross_references cross_reference
			      WHERE cross_reference.team_id = relationship.team_id
			        AND cross_reference.target_relationship_id = relationship.relationship_id
			        AND cross_reference.kind = 'challenges'
			  )
			  AND NOT EXISTS (
			      SELECT 1
			      FROM review_tasks review
			      WHERE review.team_id = relationship.team_id
			        AND review.relationship_id = relationship.relationship_id
			        AND review.status IN ('open', 'acknowledged')
			  )
		)
	`, teamID, relationshipID, status).Scan(&eligible).Error
	return eligible, err
}

func staleDreamSourceError(err error, relationshipID string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrDreamSourceStale, relationshipID)
	}
	return err
}

func dreamDerivationMatchesEvidence(derivation DreamDerivationSource, available []DreamEvidence) bool {
	for _, excerpt := range available {
		if strings.TrimSpace(derivation.SupportID) != strings.TrimSpace(excerpt.SupportID) ||
			strings.TrimSpace(derivation.ObservationID) != strings.TrimSpace(excerpt.ObservationID) ||
			strings.TrimSpace(derivation.FragmentID) != strings.TrimSpace(excerpt.FragmentID) ||
			strings.TrimSpace(derivation.SourceID) != strings.TrimSpace(excerpt.SourceID) ||
			strings.TrimSpace(derivation.SourceRevisionID) != strings.TrimSpace(excerpt.SourceRevisionID) ||
			strings.TrimSpace(derivation.SourceGroupKey) != strings.TrimSpace(excerpt.SourceGroupKey) ||
			derivation.SpanStart != excerpt.SpanStart ||
			derivation.SpanEnd != excerpt.SpanEnd ||
			derivation.Quote != excerpt.Content ||
			strings.TrimSpace(derivation.Authority) != strings.TrimSpace(excerpt.Authority) {
			continue
		}
		return true
	}
	return false
}
