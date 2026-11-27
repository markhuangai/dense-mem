package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/lib/pq"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) PublishAutomatic(ctx context.Context, teamID string, input topic.Publication) (topic.PublicationResult, error) {
	if err := requireAutomatic(ctx); err != nil {
		return topic.PublicationResult{}, err
	}
	return s.publish(ctx, teamID, input, "automatic", "")
}

func (s *Store) PublishManager(ctx context.Context, teamID string, input topic.Publication) (topic.PublicationResult, error) {
	actor, err := managerActor(ctx, teamID)
	if err != nil {
		return topic.PublicationResult{}, err
	}
	return s.publish(ctx, teamID, input, "manager", actor)
}

func (s *Store) publish(ctx context.Context, teamID string, input topic.Publication, origin, actor string) (result topic.PublicationResult, err error) {
	ctx, diagnostic, finish := topic.ObservePublication(ctx, s.publicationObserver, teamID, origin, input)
	defer func() {
		if diagnostic != nil && result.ID != "" {
			diagnostic.Result = &result
		}
		finish(err)
	}()
	if err := topic.ValidatePublication(input); err != nil {
		return topic.PublicationResult{}, err
	}
	hash, err := topic.RequestHash(input, origin, actor)
	if err != nil {
		return topic.PublicationResult{}, err
	}
	return s.commit(ctx, teamID, input, origin, actor, hash)
}

func (s *Store) commit(ctx context.Context, teamID string, input topic.Publication, origin, actor, hash string) (result topic.PublicationResult, err error) {
	ctx, diagnostic, finish := topic.ObservePublication(ctx, s.publicationObserver, teamID, origin, input)
	defer func() {
		if diagnostic != nil && result.ID != "" {
			diagnostic.Result = &result
		}
		finish(err)
	}()
	err = s.withScope(ctx, teamID, false, func(tx *gorm.DB, fence scope) error {
		if err := maintenancePublicationFence(ctx, tx, fence); err != nil {
			return err
		}
		var err error
		result, err = s.commitPublication(tx, fence, input, origin, actor, hash, "")
		return err
	})
	if err != nil {
		return topic.PublicationResult{}, fmt.Errorf("topics: publish: %w", err)
	}
	return result, nil
}

func (s *Store) commitPublication(tx *gorm.DB, fence scope, input topic.Publication, origin, actor, hash, assessmentID string) (topic.PublicationResult, error) {
	if err := lockOrganizationCatalog(tx, fence); err != nil {
		return topic.PublicationResult{}, err
	}
	return s.writePublication(tx, fence, input, origin, actor, hash, assessmentID, true, nil)
}

func (s *Store) writePublication(tx *gorm.DB, fence scope, input topic.Publication, origin, actor, hash, assessmentID string, checkRevision bool, aliasEnrichment map[string]bool) (topic.PublicationResult, error) {
	var names []string
	for _, change := range input.Changes {
		names = append(names, topic.DefinitionNames(change.Record)...)
	}
	if err := lockTopicNames(tx, fence, names); err != nil {
		return topic.PublicationResult{}, err
	}
	if diagnostic := topic.PublicationDiagnosticFromContext(tx.Statement.Context); diagnostic != nil {
		diagnostic.Publication = &input
		diagnostic.AssessmentID = assessmentID
	}
	existing, found, err := replayPublication(tx, fence, input.OperationKey, hash)
	if err != nil {
		return topic.PublicationResult{}, err
	}
	if found {
		return existing, nil
	}
	if checkRevision {
		revision, err := catalogRevision(tx, fence)
		if err != nil {
			return topic.PublicationResult{}, err
		}
		if revision != input.ExpectedRevision {
			return topic.PublicationResult{}, topic.PublicationVersionRejection("catalog_revision", "", input.ExpectedRevision, revision)
		}
	}
	records := make([]topic.Record, 0, len(input.Changes))
	var changedDefinitions []string
	for _, change := range input.Changes {
		if change.Record.Group != nil && change.Record.Group.AssessmentID != "" && !change.Record.Retired {
			if change.Record.Group.AssessmentID != assessmentID && origin != "rollback" {
				return topic.PublicationResult{}, fmt.Errorf("%w: assessed groups require organization publication", topic.ErrInvalid)
			}
			if change.Record.Group.AssessmentID != assessmentID {
				if err := validateAssessmentProvenance(tx, fence, change.Record); err != nil {
					return topic.PublicationResult{}, err
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
		return topic.PublicationResult{}, err
	}
	sourceCatalog := make(map[string]topic.Record, len(catalog)+len(records))
	for id, record := range catalog {
		sourceCatalog[id] = record
	}
	for _, record := range records {
		sourceCatalog[record.ID] = record
	}
	snapshots, err := sourceSnapshots(tx, fence, records, sourceCatalog)
	if err != nil {
		return topic.PublicationResult{}, err
	}
	var prepared []topic.Record
	if len(input.Changes) > 0 {
		prepared, err = topic.PreparePublication(catalog, snapshots, input, origin == "automatic")
		if err != nil {
			return topic.PublicationResult{}, err
		}
	} else {
		return topic.PublicationResult{}, topic.ErrInvalid
	}
	result := topic.PublicationResult{ID: uuid.NewString(), Records: []topic.RevisionRef{}}
	if err := tx.Raw(`SELECT nextval('topic_publication_revision')`).Row().Scan(&result.Revision); err != nil {
		return topic.PublicationResult{}, err
	}
	for _, record := range prepared {
		result.Records = append(result.Records, topic.RevisionRef{ID: record.ID, Version: record.Version})
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return topic.PublicationResult{}, err
	}
	if err := tx.Exec(`INSERT INTO topic_publications(team_id,shared_space_id,space_generation,
		publication_id,revision,operation_key,request_hash,origin,actor_id,reason,rollback_of,result)
		VALUES (?::uuid,?::uuid,?,?::uuid,?,?,?,?,NULLIF(?,'')::uuid,?,NULLIF(?,'')::uuid,?::jsonb)`,
		fence.TeamID, fence.SpaceID, fence.Generation, result.ID, result.Revision, input.OperationKey, hash, origin, actor, input.Reason, input.RollbackOf, string(encoded)).Error; err != nil {
		return topic.PublicationResult{}, err
	}
	for _, record := range prepared {
		body, err := marshalRecord(record)
		if err != nil {
			return topic.PublicationResult{}, err
		}
		if err := tx.Exec(`SELECT dense_mem_insert_topic_revision(?::uuid,?::uuid,?,?::uuid,?::jsonb)`,
			fence.TeamID, fence.SpaceID, fence.Generation, result.ID, string(body)).Error; err != nil {
			return topic.PublicationResult{}, err
		}
	}
	for _, record := range prepared {
		if err := insertDependencies(tx, fence, record); err != nil {
			return topic.PublicationResult{}, err
		}
	}
	for _, record := range prepared {
		if err := writeRelationalRecord(tx, fence, record, origin, aliasEnrichment[record.ID]); err != nil {
			return topic.PublicationResult{}, err
		}
		names := topic.DefinitionNames(record)
		if names == nil {
			names = []string{}
		}
		updated := tx.Exec(`INSERT INTO topic_record_heads(team_id,shared_space_id,space_generation,record_id,version,kind,retired,names)
			VALUES (?::uuid,?::uuid,?,?::uuid,?,?,?,?::text[])
			ON CONFLICT (team_id,shared_space_id,space_generation,record_id) DO UPDATE
			SET version=EXCLUDED.version,retired=EXCLUDED.retired,names=EXCLUDED.names
			WHERE topic_record_heads.version=?`, fence.TeamID, fence.SpaceID, fence.Generation, record.ID, record.Version,
			string(record.Kind), record.Retired, pq.Array(names), record.Version-1)
		if updated.Error != nil {
			return topic.PublicationResult{}, updated.Error
		}
		if updated.RowsAffected != 1 {
			return topic.PublicationResult{}, topic.ErrConflict
		}
	}
	var edges []communityEdge
	for _, record := range prepared {
		if record.Retired || record.Definition == nil || record.Definition.ParentID == "" {
			continue
		}
		edges = append(edges, communityEdge{TopicID: record.ID, ParentID: record.Definition.ParentID})
	}
	conflict, err := conflictingCommunityEdge(tx, fence, edges)
	if err != nil {
		return topic.PublicationResult{}, err
	}
	if conflict != nil {
		return topic.PublicationResult{}, &topic.PublicationRejection{
			Cause: topic.ErrConflict, Reason: "community_hierarchy_conflict", RecordID: conflict.TopicID, ParentID: conflict.ParentID,
		}
	}
	return result, nil
}

func replayPublication(tx *gorm.DB, fence scope, key, hash string) (topic.PublicationResult, bool, error) {
	var storedHash string
	var encoded []byte
	err := tx.Raw(`SELECT request_hash,result FROM topic_publications WHERE team_id=?::uuid
		AND shared_space_id=?::uuid AND space_generation=? AND operation_key=?`, fence.TeamID, fence.SpaceID, fence.Generation, key).Row().Scan(&storedHash, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return topic.PublicationResult{}, false, nil
	}
	if err != nil {
		return topic.PublicationResult{}, false, err
	}
	if hash != storedHash {
		return topic.PublicationResult{}, true, topic.ErrConflict
	}
	var result topic.PublicationResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return result, true, err
	}
	result.Existing = true
	return result, true, nil
}

func insertDependencies(tx *gorm.DB, fence scope, record topic.Record) error {
	for _, source := range record.Sources {
		if err := tx.Exec(`INSERT INTO topic_source_dependencies(team_id,shared_space_id,space_generation,
			record_id,record_version,source_kind,source_id,source_version,fingerprint)
			VALUES (?::uuid,?::uuid,?,?::uuid,?,?,?,?,?)`, fence.TeamID, fence.SpaceID, fence.Generation, record.ID,
			record.Version, string(source.Kind), source.ID, source.Version, source.Fingerprint).Error; err != nil {
			return err
		}
	}
	for _, ref := range record.Dependencies {
		if err := tx.Exec(`INSERT INTO topic_revision_dependencies(team_id,shared_space_id,space_generation,
			record_id,record_version,dependency_id,dependency_version)
			VALUES (?::uuid,?::uuid,?,?::uuid,?,?::uuid,?)`, fence.TeamID, fence.SpaceID, fence.Generation, record.ID, record.Version, ref.ID, ref.Version).Error; err != nil {
			return err
		}
	}
	return nil
}

func lockTopicNames(tx *gorm.DB, fence scope, names []string) error {
	slices.Sort(names)
	for _, name := range slices.Compact(names) {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?::text,25023))`, fence.SpaceID+":"+name).Error; err != nil {
			return err
		}
	}
	return nil
}
