package postgres

import (
	"context"
	"encoding/json"

	"fmt"

	"github.com/lib/pq"
	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"

	"gorm.io/gorm"
)

func (r *Store) ListCommunityInputs(ctx context.Context, input CommunityInputListInput) ([]CommunityInput, error) {
	input = communitycontract.NormalizeCommunityInputListInput(input)
	if err := communitycontract.ValidateCommunityInputListInput(input); err != nil {
		return nil, err
	}
	var out []CommunityInput
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		fence, err := loadTeamSharedSpaceFence(ctx, tx, input.TeamID)
		if err != nil {
			return err
		}
		out, err = listCommunityInputsTx(ctx, tx, fence, input)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("community: list inputs: %w", err)
	}
	return out, nil
}

func listCommunityInputsTx(ctx context.Context, tx *gorm.DB, fence semanticSpaceFence, input CommunityInputListInput) ([]CommunityInput, error) {
	out := []CommunityInput{}

	rows, err := tx.WithContext(ctx).Raw(`
		WITH canonical_names AS (
			SELECT DISTINCT ON (team_id, entity_id)
			       team_id, entity_id, space_id, space_generation, display_name
			FROM entity_names
			WHERE team_id = ?::uuid
			  AND space_id = ?::uuid
			  AND space_generation = ?
			  AND name_kind = 'canonical'
			  AND valid_to IS NULL
			ORDER BY team_id, entity_id, created_at DESC, entity_name_id DESC
		), latest_support AS (
			SELECT DISTINCT ON (team_id, support_id)
			       team_id, support_id, decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			  AND space_id = ?::uuid
			  AND space_generation = ?
			ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
		), effective_support AS (
			SELECT support.team_id, support.relationship_id,
			       array_agg(DISTINCT support.fragment_id::text ORDER BY support.fragment_id::text) AS evidence_ids,
			       jsonb_agg(jsonb_build_object('evidence_id', support.fragment_id::text, 'quote', support.quote)
			                 ORDER BY support.fragment_id::text) AS evidence_quotes
			FROM relationship_evidence_supports AS support
			JOIN latest_support AS latest
			  ON latest.team_id = support.team_id
			 AND latest.support_id = support.support_id
			 AND latest.decision IN ('grant', 'reinstate')
			LEFT JOIN evidence_quarantines AS quarantine
			  ON quarantine.team_id = support.team_id
			 AND quarantine.fragment_id = support.fragment_id
			 AND quarantine.status = 'active'
			LEFT JOIN evidence_sources AS source
			  ON source.team_id = support.team_id
			 AND source.source_id = support.source_id
			LEFT JOIN evidence_lifecycle_events AS lifecycle
			  ON lifecycle.team_id = support.team_id
			 AND lifecycle.target_fragment_id = support.fragment_id
			WHERE support.team_id = ?::uuid
			  AND support.space_id = ?::uuid
			  AND support.space_generation = ?
			  AND quarantine.quarantine_id IS NULL
			  AND lifecycle.lifecycle_event_id IS NULL
			  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
			GROUP BY support.team_id, support.relationship_id
		)
		SELECT relationship.relationship_id::text,
		       relationship.owner_profile_id::text,
		       relationship.version,
		       relationship.subject_entity_id::text,
		       COALESCE(subject_name.display_name, relationship.subject_entity_id::text) AS subject_name,
		       relationship.predicate_key,
		       relationship.predicate_version,
		       COALESCE(relationship.object_entity_id::text, ''),
		       COALESCE(object_name.display_name, value_record.display, value_record.canonical_value, relationship.object_value_id::text, '') AS object_name,
		       COALESCE(relationship.object_value_id::text, ''),
		       COALESCE(value_record.value_type, ''),
		       COALESCE(value_record.canonical_value, ''),
		       relationship.semantic_group_key,
		       effective_support.evidence_ids,
		       effective_support.evidence_quotes
		FROM relationship_records AS relationship
		JOIN entity_records AS subject
		  ON subject.team_id = relationship.team_id
		 AND subject.entity_id = relationship.subject_entity_id
		 AND subject.space_id = relationship.space_id
		 AND subject.space_generation = relationship.space_generation
		 AND subject.status = 'active'
		LEFT JOIN entity_records AS object
		  ON object.team_id = relationship.team_id
		 AND object.entity_id = relationship.object_entity_id
		 AND object.space_id = relationship.space_id
		 AND object.space_generation = relationship.space_generation
		 AND object.status = 'active'
		LEFT JOIN value_records AS value_record
		  ON value_record.team_id = relationship.team_id
		 AND value_record.value_id = relationship.object_value_id
		 AND value_record.space_id = relationship.space_id
		 AND value_record.space_generation = relationship.space_generation
		LEFT JOIN canonical_names AS subject_name
		  ON subject_name.team_id = relationship.team_id
		 AND subject_name.entity_id = relationship.subject_entity_id
		 AND subject_name.space_id = relationship.space_id
		 AND subject_name.space_generation = relationship.space_generation
		LEFT JOIN canonical_names AS object_name
			  ON object_name.team_id = relationship.team_id
			 AND object_name.entity_id = relationship.object_entity_id
			 AND object_name.space_id = relationship.space_id
			 AND object_name.space_generation = relationship.space_generation
		JOIN effective_support
		  ON effective_support.team_id = relationship.team_id
		 AND effective_support.relationship_id = relationship.relationship_id
		 AND relationship.space_id = ?::uuid
		 AND relationship.space_generation = ?
		WHERE relationship.team_id = ?::uuid
		  AND relationship.space_id = ?::uuid
		  AND relationship.space_generation = ?
		  AND (?::uuid[] IS NULL OR relationship.relationship_id=ANY(?::uuid[]))
		  AND relationship.identity_alias_of_relationship_id IS NULL
		  AND relationship.status = 'active'
		  AND (relationship.object_entity_id IS NULL OR object.entity_id IS NOT NULL)
		  AND (relationship.object_entity_id IS NOT NULL OR relationship.object_value_id IS NOT NULL)
		ORDER BY relationship.updated_at ASC, relationship.relationship_id ASC
		LIMIT ?
	`, input.TeamID, fence.ID, fence.Generation,
		input.TeamID, fence.ID, fence.Generation,
		input.TeamID, fence.ID, fence.Generation,
		fence.ID, fence.Generation, input.TeamID, fence.ID, fence.Generation, pq.Array(input.RelationshipIDs), pq.Array(input.RelationshipIDs), input.Limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item := CommunityInput{}
		var evidenceIDs pq.StringArray
		var evidenceQuotesJSON []byte
		if err := rows.Scan(&item.RelationshipID, &item.OwnerProfileID, &item.Version,
			&item.SubjectEntityID, &item.SubjectName, &item.PredicateKey,
			&item.PredicateVersion, &item.ObjectEntityID,
			&item.ObjectName, &item.ObjectValueID, &item.ObjectValueType,
			&item.ObjectValue, &item.SemanticGroupKey, &evidenceIDs, &evidenceQuotesJSON); err != nil {
			return nil, err
		}
		item.EvidenceIDs = []string(evidenceIDs)
		if len(evidenceQuotesJSON) > 0 {
			if err := json.Unmarshal(evidenceQuotesJSON, &item.EvidenceQuotes); err != nil {
				return nil, err
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
