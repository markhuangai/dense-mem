package postgres

import (
	"context"
	"errors"
	"fmt"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"gorm.io/gorm"
)

func RelationshipProjectionTextTx(ctx context.Context, tx *gorm.DB, relationship *knowledge.RelationshipRecord) (string, error) {
	if relationship == nil {
		return "", errors.New("relationship is required")
	}
	var names knowledge.RelationshipProjectionNames
	err := tx.WithContext(ctx).Raw(`
		SELECT
		    COALESCE(NULLIF(subject_name.display_name, ''), relationship.subject_entity_id::text) AS subject_name,
			    COALESCE(
			        NULLIF(object_name.display_name, ''),
			        NULLIF(value_record.display, ''),
			        NULLIF(value_record.canonical_value, ''),
			        relationship.object_entity_id::text,
			        relationship.object_value_id::text,
			        ''
			    ) AS object_name,
		    COALESCE(value_record.value_type, '') AS object_value_type,
		    COALESCE(value_record.canonical_value, '') AS object_value,
		    COALESCE(value_record.unit, '') AS object_unit
		FROM relationship_records AS relationship
		LEFT JOIN entity_names AS subject_name
		  ON subject_name.team_id = relationship.team_id
		 AND subject_name.entity_id = relationship.subject_entity_id
		 AND subject_name.name_kind = 'canonical'
		 AND subject_name.valid_to IS NULL
		LEFT JOIN entity_names AS object_name
		  ON object_name.team_id = relationship.team_id
		 AND object_name.entity_id = relationship.object_entity_id
		 AND object_name.name_kind = 'canonical'
		 AND object_name.valid_to IS NULL
		LEFT JOIN value_records AS value_record
		  ON value_record.team_id = relationship.team_id
		 AND value_record.value_id = relationship.object_value_id
		WHERE relationship.team_id = ?::uuid
		  AND relationship.relationship_id = ?::uuid
		LIMIT 1
	`, relationship.TeamID, relationship.RelationshipID).Row().Scan(
		&names.SubjectName,
		&names.ObjectName,
		&names.ObjectValueType,
		&names.ObjectValue,
		&names.ObjectUnit,
	)
	if err != nil {
		return "", fmt.Errorf("render relationship projection names: %w", err)
	}
	return knowledge.RelationshipProjectionText(relationship, names), nil
}
