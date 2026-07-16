package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *SemanticRepositoryImpl) TraceRelationship(ctx context.Context, teamID string, relationshipID string) (*domain.SemanticTraceResult, error) {
	teamID = strings.TrimSpace(teamID)
	relationshipID = strings.TrimSpace(relationshipID)
	if teamID == "" || relationshipID == "" {
		return nil, sql.ErrNoRows
	}
	var result domain.SemanticTraceResult
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT `+semanticRelationshipColumns()+`
			FROM semantic_relationship_records r
			LEFT JOIN semantic_values value
			  ON value.team_id = r.team_id AND value.value_id = r.object_value_id
			WHERE r.team_id = ? AND r.relationship_id = ?
			LIMIT 1
		`, teamID, relationshipID).Rows()
		if err != nil {
			return err
		}
		if err := func() error {
			defer rows.Close()
			if !rows.Next() {
				return sql.ErrNoRows
			}
			rel, err := scanSemanticRelationship(rows)
			if err != nil {
				return err
			}
			result.Relationship = &rel
			return rows.Err()
		}(); err != nil {
			return err
		}

		supportRows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, relationship_id::text, fragment_id::text,
			       evidence_index, quote, created_at
			FROM semantic_relationship_supports
			WHERE team_id = ? AND relationship_id = ?
			ORDER BY evidence_index ASC, fragment_id ASC
		`, teamID, relationshipID).Rows()
		if err != nil {
			return err
		}
		if err := func() error {
			defer supportRows.Close()
			for supportRows.Next() {
				var support domain.SemanticRelationshipSupport
				if err := supportRows.Scan(&support.TeamID, &support.RelationshipID, &support.FragmentID, &support.EvidenceIndex, &support.Quote, &support.CreatedAt); err != nil {
					return err
				}
				result.Supports = append(result.Supports, support)
			}
			return supportRows.Err()
		}(); err != nil {
			return err
		}

		evidenceRows, err := tx.WithContext(ctx).Raw(`
			SELECT `+semanticEvidenceColumns("e")+`
			FROM semantic_relationship_supports s
			JOIN semantic_evidence_fragments e
			  ON e.team_id = s.team_id
			 AND e.fragment_id = s.fragment_id
			WHERE s.team_id = ? AND s.relationship_id = ?
			ORDER BY s.evidence_index ASC, e.fragment_id ASC
		`, teamID, relationshipID).Rows()
		if err != nil {
			return err
		}
		defer evidenceRows.Close()
		for evidenceRows.Next() {
			evidence, err := scanSemanticEvidence(evidenceRows)
			if err != nil {
				return err
			}
			result.Evidence = append(result.Evidence, evidence)
		}
		return evidenceRows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("semantic relationship trace: %w", err)
	}
	return &result, nil
}
