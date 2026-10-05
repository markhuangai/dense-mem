package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/lib/pq"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) PublishAutomatic(ctx context.Context, teamID string, input ontology.Publication) (ontology.PublicationResult, error) {
	if err := requireAutomatic(ctx); err != nil {
		return ontology.PublicationResult{}, err
	}
	return s.publish(ctx, teamID, input, "automatic", "")
}

func (s *Store) PublishManager(ctx context.Context, teamID string, input ontology.Publication) (ontology.PublicationResult, error) {
	actor, err := managerActor(ctx, teamID)
	if err != nil {
		return ontology.PublicationResult{}, err
	}
	return s.publish(ctx, teamID, input, "manager", actor)
}

func (s *Store) publish(ctx context.Context, teamID string, input ontology.Publication, origin, actor string) (ontology.PublicationResult, error) {
	if err := ontology.ValidatePublication(input); err != nil {
		return ontology.PublicationResult{}, err
	}
	hash, err := ontology.RequestHash(input, origin, actor)
	if err != nil {
		return ontology.PublicationResult{}, err
	}
	return s.commit(ctx, teamID, input, origin, actor, hash, "")
}

func (s *Store) commit(ctx context.Context, teamID string, input ontology.Publication, origin, actor, hash, nextPredicate string) (ontology.PublicationResult, error) {
	var result ontology.PublicationResult
	err := s.withScope(ctx, teamID, false, func(tx *gorm.DB, fence scope) error {
		var err error
		result, err = s.commitPublication(tx, fence, input, origin, actor, hash, nextPredicate, "")
		return err
	})
	if err != nil {
		return ontology.PublicationResult{}, fmt.Errorf("ontology: publish: %w", err)
	}
	return result, nil
}

func (s *Store) commitPublication(tx *gorm.DB, fence scope, input ontology.Publication, origin, actor, hash, nextPredicate, assessmentID string) (ontology.PublicationResult, error) {
	if err := tx.Exec(`INSERT INTO ontology_catalog_heads(team_id,shared_space_id,space_generation)
		VALUES (?::uuid,?::uuid,?) ON CONFLICT DO NOTHING`, fence.TeamID, fence.SpaceID, fence.Generation).Error; err != nil {
		return ontology.PublicationResult{}, err
	}
	var revision int64
	if err := tx.Raw(`SELECT revision FROM ontology_catalog_heads WHERE team_id=?::uuid
		AND shared_space_id=?::uuid AND space_generation=? FOR UPDATE`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&revision); err != nil {
		return ontology.PublicationResult{}, err
	}
	existing, found, err := replayPublication(tx, fence, input.OperationKey, hash)
	if err != nil {
		return ontology.PublicationResult{}, err
	}
	if found {
		return existing, nil
	}
	if revision != input.ExpectedRevision {
		return ontology.PublicationResult{}, ontology.ErrConflict
	}
	records := make([]ontology.Record, 0, len(input.Changes))
	var changedDefinitions []string
	for _, change := range input.Changes {
		if change.Record.Group != nil && change.Record.Group.AssessmentID != "" && !change.Record.Retired {
			if change.Record.Group.AssessmentID != assessmentID && origin != "rollback" {
				return ontology.PublicationResult{}, fmt.Errorf("%w: assessed groups require organization publication", ontology.ErrInvalid)
			}
			if change.Record.Group.AssessmentID != assessmentID {
				if err := validateAssessmentProvenance(tx, fence, change.Record); err != nil {
					return ontology.PublicationResult{}, err
				}
			}
		}
		records = append(records, change.Record)
		if change.Record.Definition != nil {
			changedDefinitions = append(changedDefinitions, change.Record.ID)
		}
	}
	catalog, err := validationCatalog(tx, fence, records, changedDefinitions)
	if err != nil {
		return ontology.PublicationResult{}, err
	}
	sourceCatalog := make(map[string]ontology.Record, len(catalog)+len(records))
	for id, record := range catalog {
		sourceCatalog[id] = record
	}
	for _, record := range records {
		sourceCatalog[record.ID] = record
	}
	snapshots, err := sourceSnapshots(tx, fence, records, sourceCatalog)
	if err != nil {
		return ontology.PublicationResult{}, err
	}
	prepared := []ontology.Record{}
	if len(input.Changes) > 0 {
		prepared, err = ontology.PreparePublication(catalog, snapshots, input, origin == "automatic" || origin == "seed")
		if err != nil {
			return ontology.PublicationResult{}, err
		}
	} else if origin != "seed" {
		return ontology.PublicationResult{}, ontology.ErrInvalid
	}
	result := ontology.PublicationResult{ID: uuid.NewString(), Revision: revision + 1, Records: []ontology.RevisionRef{}, NextPredicate: nextPredicate}
	for _, record := range prepared {
		result.Records = append(result.Records, ontology.RevisionRef{ID: record.ID, Version: record.Version})
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return ontology.PublicationResult{}, err
	}
	if err := tx.Exec(`INSERT INTO ontology_publications(team_id,shared_space_id,space_generation,
		publication_id,revision,operation_key,request_hash,origin,actor_id,reason,rollback_of,result)
		VALUES (?::uuid,?::uuid,?,?::uuid,?,?,?,?,NULLIF(?,'')::uuid,?,NULLIF(?,'')::uuid,?::jsonb)`,
		fence.TeamID, fence.SpaceID, fence.Generation, result.ID, result.Revision, input.OperationKey, hash, origin, actor, input.Reason, input.RollbackOf, string(encoded)).Error; err != nil {
		return ontology.PublicationResult{}, err
	}
	for _, record := range prepared {
		body, err := marshalRecord(record)
		if err != nil {
			return ontology.PublicationResult{}, err
		}
		if err := tx.Exec(`INSERT INTO ontology_record_revisions(team_id,shared_space_id,space_generation,
			record_id,version,kind,publication_id,retired,body,fingerprint)
			VALUES (?::uuid,?::uuid,?,?::uuid,?,?,?::uuid,?,?::jsonb,?)`, fence.TeamID, fence.SpaceID, fence.Generation,
			record.ID, record.Version, string(record.Kind), result.ID, record.Retired, string(body), record.Fingerprint).Error; err != nil {
			return ontology.PublicationResult{}, err
		}
	}
	for _, record := range prepared {
		if err := insertDependencies(tx, fence, record); err != nil {
			return ontology.PublicationResult{}, err
		}
	}
	for _, record := range prepared {
		names := ontology.DefinitionNames(record)
		if names == nil {
			names = []string{}
		}
		updated := tx.Exec(`INSERT INTO ontology_record_heads(team_id,shared_space_id,space_generation,record_id,version,kind,retired,names)
			VALUES (?::uuid,?::uuid,?,?::uuid,?,?,?,?::text[])
			ON CONFLICT (team_id,shared_space_id,space_generation,record_id) DO UPDATE
			SET version=EXCLUDED.version,retired=EXCLUDED.retired,names=EXCLUDED.names
			WHERE ontology_record_heads.version=?`, fence.TeamID, fence.SpaceID, fence.Generation, record.ID, record.Version,
			string(record.Kind), record.Retired, pq.Array(names), record.Version-1)
		if updated.Error != nil {
			return ontology.PublicationResult{}, updated.Error
		}
		if updated.RowsAffected != 1 {
			return ontology.PublicationResult{}, ontology.ErrConflict
		}
	}
	updated := tx.Exec(`UPDATE ontology_catalog_heads SET revision=? WHERE team_id=?::uuid
		AND shared_space_id=?::uuid AND space_generation=? AND revision=?`, result.Revision, fence.TeamID, fence.SpaceID, fence.Generation, revision)
	if updated.Error != nil {
		return ontology.PublicationResult{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return ontology.PublicationResult{}, ontology.ErrConflict
	}
	return result, nil
}

func replayPublication(tx *gorm.DB, fence scope, key, hash string) (ontology.PublicationResult, bool, error) {
	var storedHash string
	var encoded []byte
	err := tx.Raw(`SELECT request_hash,result FROM ontology_publications WHERE team_id=?::uuid
		AND shared_space_id=?::uuid AND space_generation=? AND operation_key=?`, fence.TeamID, fence.SpaceID, fence.Generation, key).Row().Scan(&storedHash, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return ontology.PublicationResult{}, false, nil
	}
	if err != nil {
		return ontology.PublicationResult{}, false, err
	}
	if hash != storedHash {
		return ontology.PublicationResult{}, true, ontology.ErrConflict
	}
	var result ontology.PublicationResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return result, true, err
	}
	result.Existing = true
	return result, true, nil
}

func insertDependencies(tx *gorm.DB, fence scope, record ontology.Record) error {
	for _, source := range record.Sources {
		if err := tx.Exec(`INSERT INTO ontology_source_dependencies(team_id,shared_space_id,space_generation,
			record_id,record_version,source_kind,source_id,source_version,fingerprint)
			VALUES (?::uuid,?::uuid,?,?::uuid,?,?,?,?,?)`, fence.TeamID, fence.SpaceID, fence.Generation, record.ID,
			record.Version, string(source.Kind), source.ID, source.Version, source.Fingerprint).Error; err != nil {
			return err
		}
	}
	for _, ref := range record.Dependencies {
		if err := tx.Exec(`INSERT INTO ontology_revision_dependencies(team_id,shared_space_id,space_generation,
			record_id,record_version,dependency_id,dependency_version)
			VALUES (?::uuid,?::uuid,?,?::uuid,?,?::uuid,?)`, fence.TeamID, fence.SpaceID, fence.Generation, record.ID, record.Version, ref.ID, ref.Version).Error; err != nil {
			return err
		}
	}
	return nil
}
