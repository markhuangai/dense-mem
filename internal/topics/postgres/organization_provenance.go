package postgres

import (
	"database/sql"
	"errors"
	"reflect"

	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func validateAssessmentProvenance(tx *gorm.DB, fence scope, record topic.Record) error {
	var body []byte
	args := []any{fence.TeamID, fence.SpaceID, fence.Generation, record.Group.AssessmentID}
	scopePredicate := ` WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND assessment_id=?::uuid`
	err := tx.Raw(`SELECT body FROM evidence_equivalence_outcomes`+scopePredicate+` ORDER BY created_at,operation_key LIMIT 1`, args...).Row().Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.Raw(`SELECT body FROM historical_organization_assessments`+scopePredicate+` ORDER BY created_at,operation_key LIMIT 1`, args...).Row().Scan(&body)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return topic.ErrSourceStale
	}
	if err != nil {
		return err
	}
	receipt, err := decodeOrganization(body)
	if err != nil {
		return err
	}
	if receipt.Result.Publication == nil {
		return topic.ErrInvalid
	}
	version := int64(0)
	for _, ref := range receipt.Result.Publication.Records {
		if ref.ID == record.ID {
			version = ref.Version
		}
	}
	if version == 0 {
		return topic.ErrInvalid
	}
	err = tx.Raw(`SELECT body FROM topic_revision_records WHERE team_id=?::uuid AND shared_space_id=?::uuid
        AND space_generation=? AND record_id=?::uuid AND version=?`, fence.TeamID, fence.SpaceID, fence.Generation, record.ID, version).Row().Scan(&body)
	if err != nil {
		return err
	}
	original, err := decodeRecord(body)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(original.Group, record.Group) {
		return topic.ErrInvalid
	}
	proof := map[string]topic.SourceDependency{}
	for _, dependency := range receipt.Sources {
		proof[topic.SourceKey(dependency.SourceHandle)] = dependency
	}
	for _, source := range record.Sources {
		if proof[topic.SourceKey(source.SourceHandle)] != source {
			return topic.ErrSourceStale
		}
	}
	return nil
}
