package postgres

import (
	"database/sql"
	"errors"
	"reflect"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func validateAssessmentProvenance(tx *gorm.DB, fence scope, record ontology.Record) error {
	var body []byte
	err := tx.Raw(`SELECT body FROM ontology_assessments WHERE team_id=?::uuid AND shared_space_id=?::uuid
        AND space_generation=? AND assessment_id=?::uuid ORDER BY created_at,operation_key LIMIT 1`,
		fence.TeamID, fence.SpaceID, fence.Generation, record.Group.AssessmentID).Row().Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return ontology.ErrSourceStale
	}
	if err != nil {
		return err
	}
	receipt, err := decodeOrganization(body)
	if err != nil {
		return err
	}
	if receipt.Result.Publication == nil {
		return ontology.ErrInvalid
	}
	version := int64(0)
	for _, ref := range receipt.Result.Publication.Records {
		if ref.ID == record.ID {
			version = ref.Version
		}
	}
	if version == 0 {
		return ontology.ErrInvalid
	}
	err = tx.Raw(`SELECT body FROM ontology_record_revisions WHERE team_id=?::uuid AND shared_space_id=?::uuid
        AND space_generation=? AND record_id=?::uuid AND version=?`, fence.TeamID, fence.SpaceID, fence.Generation, record.ID, version).Row().Scan(&body)
	if err != nil {
		return err
	}
	original, err := decodeRecord(body)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(original.Group, record.Group) {
		return ontology.ErrInvalid
	}
	proof := map[string]ontology.SourceDependency{}
	for _, dependency := range receipt.Sources {
		proof[ontology.SourceKey(dependency.SourceHandle)] = dependency
	}
	for _, source := range record.Sources {
		if proof[ontology.SourceKey(source.SourceHandle)] != source {
			return ontology.ErrSourceStale
		}
	}
	return nil
}
