package repository

import (
	"context"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

func hydrateDreamHypothesisDerivations(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	records []HypothesisRecord,
) error {
	if len(records) == 0 {
		return nil
	}
	indices := make(map[string]int, len(records))
	ids := make([]string, 0, len(records))
	for index := range records {
		if records[index].HypothesisID == "" {
			continue
		}
		indices[records[index].HypothesisID] = index
		ids = append(ids, records[index].HypothesisID)
		records[index].Derivations = []DreamDerivationSource{}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT hypothesis_id::text, premise_position, relationship_id::text,
		       relationship_version, COALESCE(support_id::text, ''),
		       COALESCE(observation_id::text, ''), fragment_id::text,
		       COALESCE(source_id::text, ''), COALESCE(source_revision_id::text, ''),
		       source_group_key, span_start, span_end, quote, authority
		FROM hypothesis_derivation_sources
		WHERE team_id = ?::uuid
		  AND space_id = dense_mem_team_shared_space(team_id)
		  AND space_generation = dense_mem_team_shared_generation(team_id)
		  AND hypothesis_id = ANY(?::uuid[])
		ORDER BY hypothesis_id, premise_position, derivation_source_id
	`, teamID, pq.Array(ids)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hypothesisID string
		var derivation DreamDerivationSource
		if err := rows.Scan(
			&hypothesisID,
			&derivation.PremisePosition,
			&derivation.RelationshipID,
			&derivation.RelationshipVersion,
			&derivation.SupportID,
			&derivation.ObservationID,
			&derivation.FragmentID,
			&derivation.SourceID,
			&derivation.SourceRevisionID,
			&derivation.SourceGroupKey,
			&derivation.SpanStart,
			&derivation.SpanEnd,
			&derivation.Quote,
			&derivation.Authority,
		); err != nil {
			return err
		}
		if index, ok := indices[hypothesisID]; ok {
			records[index].Derivations = append(records[index].Derivations, derivation)
		}
	}
	return rows.Err()
}
