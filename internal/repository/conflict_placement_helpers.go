package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func loadRelationshipConflictSpace(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	source *RelationshipRecord,
) (string, string, error) {
	var spaceID, spaceKind string
	var err error
	if source.SpaceID != "" {
		err = tx.WithContext(ctx).Raw(`
			SELECT id::text, kind FROM memory_spaces
			WHERE team_id = ?::uuid AND id = ?::uuid
		`, teamID, source.SpaceID).Row().Scan(&spaceID, &spaceKind)
	} else if source.RelationshipID != "" {
		err = tx.WithContext(ctx).Raw(`
			SELECT relationship.space_id::text, memory_space.kind
			FROM relationship_records AS relationship
			JOIN memory_spaces AS memory_space
			  ON memory_space.team_id = relationship.team_id
			 AND memory_space.id = relationship.space_id
			WHERE relationship.team_id = ?::uuid
			  AND relationship.relationship_id = ?::uuid
		`, teamID, source.RelationshipID).Row().Scan(&spaceID, &spaceKind)
	} else {
		return "", "", errors.New("relationship conflict placement requires a relationship or space identifier")
	}
	return spaceID, spaceKind, err
}

func conflictPlacementHasConflict(rows []conflictPlacementRow) bool {
	positions := map[string]struct{}{}
	owners := map[string]struct{}{}
	for _, row := range rows {
		if row.PositionKey != "" {
			positions[row.PositionKey] = struct{}{}
		}
		if row.OwnerProfileID != "" {
			owners[row.OwnerProfileID] = struct{}{}
		}
	}
	return len(positions) >= 2 && len(owners) >= 2
}

func lockRelationshipConflictSnapshotScope(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	semanticScopeKey string,
) error {
	// Remember placement and review refresh this scope before locking case, position, or member rows.
	if strings.TrimSpace(semanticScopeKey) == "" {
		return errors.New("relationship conflict snapshot requires a semantic scope key")
	}
	return tx.WithContext(ctx).Exec(
		"SELECT pg_advisory_xact_lock(hashtextextended(?::text, 0))",
		teamID+":relationship-conflict-snapshot:"+semanticScopeKey,
	).Error
}

func lockRelationshipConflictCaseSnapshotScope(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
) error {
	var semanticScopeKey string
	err := tx.WithContext(ctx).Raw(`
		SELECT semantic_scope_key
		FROM relationship_conflict_cases
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
	`, teamID, conflictID).Row().Scan(&semanticScopeKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return lockRelationshipConflictSnapshotScope(ctx, tx, teamID, semanticScopeKey)
}

func relationshipConflictScopeKey(record *RelationshipRecord, spaceID, spaceKind string) string {
	parts := []string{
		"cross_profile_current_state", record.TeamID, record.SubjectEntityID,
		record.PredicateKey, record.RelationshipKind, record.CurrentCardinality,
		record.Polarity, record.ScopeKey,
	}
	if spaceKind != string(domain.MemorySpaceTeamShared) {
		parts = append(parts, "space", spaceID)
	}
	return "rc:" + strings.TrimPrefix(sha256Hex(strings.Join(parts, "\x00")), "sha256:")
}
