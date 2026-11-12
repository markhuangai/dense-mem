package postgres

import (
	"context"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

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
