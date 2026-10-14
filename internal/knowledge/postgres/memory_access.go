package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func authorizeRelationshipCorrection(ctx context.Context, tx *gorm.DB, input CorrectRelationshipInput) error {
	var spaceID string
	var err error
	if input.Action == "confirm" {
		err = tx.WithContext(ctx).Raw(`
			SELECT space_id::text FROM relationship_correction_submissions
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
		`, input.TeamID, input.OwnerProfileID, input.SubmissionID).Row().Scan(&spaceID)
	} else {
		input.RelationshipID, err = storagepostgres.ResolveMemoryHandleTx(ctx, tx, input.TeamID, "relationship_records", input.RelationshipID, "")
		if err != nil {
			return err
		}
		err = tx.WithContext(ctx).Raw(`
			SELECT space_id::text FROM relationship_records
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND relationship_id = ?::uuid
		`, input.TeamID, input.OwnerProfileID, input.RelationshipID).Row().Scan(&spaceID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSemanticOwnerMismatch
	}
	if err != nil {
		return err
	}
	return storagepostgres.AuthorizeMemoryMutationTx(ctx, tx, input.TeamID, input.OwnerProfileID, spaceID, 0)
}

func authorizeEvidenceRetraction(ctx context.Context, tx *gorm.DB, input RetractEvidenceInput) error {
	var spaces pq.StringArray
	var count int
	if err := tx.WithContext(ctx).Raw(`
		SELECT count(*), COALESCE(array_agg(DISTINCT space_id::text), ARRAY[]::text[])
		FROM evidence_fragments WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
		  AND fragment_id = ANY(?::uuid[])
	`, input.TeamID, input.OwnerProfileID, pq.Array(input.EvidenceIDs)).Row().Scan(&count, &spaces); err != nil {
		return err
	}
	if count != len(input.EvidenceIDs) {
		return ErrEvidenceLifecycleNotFound
	}
	if len(spaces) != 1 {
		return ErrEvidenceLifecycleConflict
	}
	return storagepostgres.AuthorizeMemoryMutationTx(ctx, tx, input.TeamID, input.OwnerProfileID, spaces[0], 0)
}

func (r *Store) ResolveMemoryHandles(ctx context.Context, teamID, ownerID, spaceID string, handles map[string][]string) (map[string]map[string]string, error) {
	resolved := map[string]map[string]string{}
	err := r.withTeamProfileTx(ctx, teamID, ownerID, func(tx *gorm.DB) error {
		if err := storagepostgres.AuthorizeMemoryMutationTx(ctx, tx, teamID, ownerID, spaceID, 0); err != nil {
			return err
		}
		for kind, ids := range handles {
			table := ""
			switch kind {
			case "evidence":
				table = "evidence_fragments"
			case "relationship":
				table = "relationship_records"
			case "entity":
				table = "entity_records"
			default:
				return domain.ErrMemoryWriteDenied
			}
			resolved[kind] = map[string]string{}
			for _, id := range ids {
				value, err := storagepostgres.ResolveMemoryHandleTx(ctx, tx, teamID, table, id, spaceID)
				if err != nil {
					return err
				}
				resolved[kind][id] = value
			}
		}
		return nil
	})
	return resolved, err
}

func resolveRelationshipCorrectionHandles(ctx context.Context, tx *gorm.DB, input CorrectRelationshipInput) (CorrectRelationshipInput, error) {
	var err error
	input.RelationshipID, err = storagepostgres.ResolveMemoryHandleTx(ctx, tx, input.TeamID, "relationship_records", input.RelationshipID, "")
	if err != nil {
		return input, err
	}
	var space string
	if err := tx.Raw(`SELECT space_id::text FROM relationship_records WHERE team_id=?::uuid AND relationship_id=?::uuid AND owner_profile_id=?::uuid`, input.TeamID, input.RelationshipID, input.OwnerProfileID).Row().Scan(&space); err != nil {
		return input, err
	}
	for _, patch := range []**RelationshipCorrectionEntityPatch{&input.Patch.SubjectEntity, &input.Patch.ObjectEntity} {
		if *patch == nil {
			continue
		}
		copy := **patch
		*patch = &copy
		copy.EntityID, err = storagepostgres.ResolveMemoryHandleTx(ctx, tx, input.TeamID, "entity_records", copy.EntityID, space)
		if err != nil {
			return input, err
		}
	}
	input.Supports = append([]RelationshipCorrectionSupport(nil), input.Supports...)
	for i := range input.Supports {
		input.Supports[i].EvidenceID, err = storagepostgres.ResolveMemoryHandleTx(ctx, tx, input.TeamID, "evidence_fragments", input.Supports[i].EvidenceID, space)
		if err != nil {
			return input, err
		}
	}
	for _, id := range []*string{&input.Selection.SubjectEntityID, &input.Selection.ObjectEntityID} {
		*id, err = storagepostgres.ResolveMemoryHandleTx(ctx, tx, input.TeamID, "entity_records", *id, space)
		if err != nil {
			return input, err
		}
	}
	return normalizeCorrectRelationshipInput(input), nil
}
