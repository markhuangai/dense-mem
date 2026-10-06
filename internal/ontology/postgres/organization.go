package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) ReadOrganization(ctx context.Context, teamID string, handles []ontology.SourceHandle) (ontology.OrganizationContext, error) {
	if err := requireAutomatic(ctx); err != nil {
		return ontology.OrganizationContext{}, err
	}
	input, err := ontology.PrepareOrganizationInput(ontology.OrganizationInput{OperationKey: "read-context", Sources: handles})
	if err != nil {
		return ontology.OrganizationContext{}, err
	}
	var result ontology.OrganizationContext
	err = s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		var err error
		result, err = s.organizationContext(tx, fence, input.Sources)
		return err
	})
	return result, err
}

func (s *Store) ReadDefinitionHeads(ctx context.Context, teamID string, definition ontology.Record) ([]ontology.RecordView, error) {
	if err := requireAutomatic(ctx); err != nil {
		return nil, err
	}
	if err := ontology.ValidateRecord(definition); err != nil {
		return nil, err
	}
	if definition.Definition == nil || definition.Retired {
		return nil, ontology.ErrInvalid
	}
	var result []ontology.RecordView
	err := s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		heads, err := loadHeads(tx, fence, []string{definition.ID}, ontology.DefinitionNames(definition), nil)
		if err != nil {
			return err
		}
		for _, record := range heads {
			if record.Definition == nil || record.Retired || record.Kind != definition.Kind {
				continue
			}
			view, err := s.currentView(tx, fence, record)
			if err != nil {
				return err
			}
			result = append(result, view)
		}
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, err
}

func (s *Store) organizationContext(tx *gorm.DB, fence scope, handles []ontology.SourceHandle) (ontology.OrganizationContext, error) {
	var result ontology.OrganizationContext
	var err error
	result.Sources, err = organizationSources(tx, fence, handles)
	if err != nil {
		return result, err
	}
	keys := []string{}
	for _, handle := range handles {
		keys = append(keys, ontology.SourceKey(handle))
	}
	rows, err := tx.Raw(`SELECT DISTINCT revision.body, head.record_id
            FROM ontology_record_heads AS head JOIN ontology_record_revisions AS revision
              USING (team_id,shared_space_id,space_generation,record_id,version)
            JOIN ontology_source_dependencies AS dependency
              ON dependency.team_id=head.team_id AND dependency.shared_space_id=head.shared_space_id
              AND dependency.space_generation=head.space_generation AND dependency.record_id=head.record_id
              AND dependency.record_version=head.version
            WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
              AND NOT head.retired AND dependency.source_kind || ':' || dependency.source_id=ANY(?::text[])
            ORDER BY head.record_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(keys), ontology.MaxDependencyRecords+1).Rows()
	if err != nil {
		return result, err
	}
	var records []ontology.Record
	for rows.Next() {
		var body []byte
		var id string
		if err := rows.Scan(&body, &id); err != nil {
			rows.Close()
			return result, err
		}
		record, err := decodeRecord(body)
		if err != nil {
			rows.Close()
			return result, err
		}
		records = append(records, record)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return result, err
	}
	if closeErr != nil {
		return result, closeErr
	}
	if len(records) > ontology.MaxDependencyRecords {
		return result, ontology.ErrContextBound
	}
	forced := []string{}
	for _, record := range records {
		view, err := s.currentView(tx, fence, record)
		if err != nil {
			return result, err
		}
		result.Records = append(result.Records, view)
		if view.Current && record.Assignment != nil {
			forced = append(forced, record.Assignment.DefinitionID)
		}
		if record.Override != nil && record.Override.DefinitionID != "" {
			forced = append(forced, record.Override.DefinitionID)
		}
	}
	result.Candidates, err = s.organizationVocabulary(tx, fence, result.Sources, forced)
	if err != nil {
		return result, err
	}
	result.Revision, err = catalogRevision(tx, fence)
	return result, err
}

func organizationSources(tx *gorm.DB, fence scope, handles []ontology.SourceHandle) ([]ontology.SourceSnapshot, error) {
	result := make([]ontology.SourceSnapshot, 0, len(handles))
	for _, handle := range handles {
		snapshot, err := readSource(tx, fence, handle)
		if errors.Is(err, ontology.ErrSourceStale) {
			snapshot = ontology.SourceSnapshot{SourceHandle: handle, TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation, State: map[string]string{}}
		} else if err != nil {
			return nil, err
		}
		result = append(result, snapshot)
	}
	return result, nil
}

func (s *Store) organizationVocabulary(tx *gorm.DB, fence scope, sources []ontology.SourceSnapshot, forced []string) ([]ontology.RecordView, error) {
	_, words := ontology.VocabularyQuery(sources)
	query := strings.Join(words, " OR ")
	rows, err := tx.Raw(`SELECT revision.body FROM ontology_record_heads AS head
        JOIN ontology_record_revisions AS revision USING(team_id,shared_space_id,space_generation,record_id,version)
        WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
          AND NOT head.retired AND head.kind IN ('entity_class','predicate_concept','topic')
          AND (head.record_id=ANY(?::uuid[]) OR head.names && ?::text[]
            OR to_tsvector('simple',array_to_string(head.names,' ') || ' ' || COALESCE(revision.body->'definition'->>'description',''))
              @@ websearch_to_tsquery('simple',?))
        ORDER BY (head.record_id=ANY(?::uuid[])) DESC,(head.names && ?::text[]) DESC,
          ts_rank_cd(to_tsvector('simple',array_to_string(head.names,' ') || ' ' || COALESCE(revision.body->'definition'->>'description','')),
            websearch_to_tsquery('simple',?)) DESC,head.record_id LIMIT ?`,
		fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(forced), pq.Array(words), query, pq.Array(forced), pq.Array(words), query, ontology.MaxVocabularyCandidates).Rows()
	if err != nil {
		return nil, err
	}
	var records []ontology.Record
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			rows.Close()
			return nil, err
		}
		record, err := decodeRecord(body)
		if err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	result := []ontology.RecordView{}
	for _, record := range records {
		view, err := s.currentView(tx, fence, record)
		if err != nil {
			return nil, err
		}
		if view.Current {
			result = append(result, view)
		}
	}
	return result, nil
}

func decodeOrganization(body []byte) (ontology.OrganizationReceipt, error) {
	var receipt ontology.OrganizationReceipt
	if err := jsonstrict.Decode(strings.NewReader(string(body)), &receipt, ontology.MaxPublicationBytes); err != nil {
		return receipt, err
	}
	return receipt, ontology.ValidateOrganizationReceipt(receipt)
}

func lockOrganizationCatalog(tx *gorm.DB, fence scope) error {
	if err := tx.Exec(`INSERT INTO ontology_catalog_heads(team_id,shared_space_id,space_generation)
        VALUES (?::uuid,?::uuid,?) ON CONFLICT DO NOTHING`, fence.TeamID, fence.SpaceID, fence.Generation).Error; err != nil {
		return err
	}
	var revision int64
	return tx.Raw(`SELECT revision FROM ontology_catalog_heads WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? FOR UPDATE`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&revision)
}

func organizationByKey(tx *gorm.DB, fence scope, key string) (ontology.OrganizationReceipt, bool, error) {
	var body []byte
	err := tx.Raw(`SELECT body FROM ontology_assessments WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND operation_key=?`, fence.TeamID, fence.SpaceID, fence.Generation, key).Row().Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return ontology.OrganizationReceipt{}, false, nil
	}
	if err != nil {
		return ontology.OrganizationReceipt{}, false, err
	}
	receipt, err := decodeOrganization(body)
	return receipt, true, err
}

func (s *Store) FindOrganization(ctx context.Context, teamID string, input ontology.OrganizationInput, identity string) (ontology.OrganizationResult, bool, error) {
	if err := requireAutomatic(ctx); err != nil {
		return ontology.OrganizationResult{}, false, err
	}
	input, err := ontology.PrepareOrganizationInput(input)
	if err != nil {
		return ontology.OrganizationResult{}, false, err
	}
	hash, err := ontology.OrganizationInputHash(input, identity)
	if err != nil {
		return ontology.OrganizationResult{}, false, err
	}
	var result ontology.OrganizationResult
	found := false
	err = s.withScope(ctx, teamID, false, func(tx *gorm.DB, fence scope) error {
		if err := lockOrganizationCatalog(tx, fence); err != nil {
			return err
		}
		receipt, exists, err := organizationByKey(tx, fence, input.OperationKey)
		if err != nil {
			return err
		}
		if exists {
			if receipt.InputHash != hash {
				return ontology.ErrConflict
			}
			current, err := s.organizationCurrent(tx, fence, receipt)
			if err != nil {
				return err
			}
			result = receipt.Result
			result.Existing = true
			result.Current = current
			found = true
			return nil
		}
		contextData, err := s.organizationContext(tx, fence, input.Sources)
		if errors.Is(err, ontology.ErrContextBound) {
			return nil
		}
		if err != nil {
			return err
		}
		contextHash, err := ontology.OrganizationContextHash(contextData)
		if err != nil {
			return err
		}
		sources := make([]ontology.SourceDependency, 0, len(contextData.Sources))
		for _, source := range contextData.Sources {
			fingerprint, err := ontology.SourceFingerprint(source)
			if err != nil {
				return err
			}
			sources = append(sources, ontology.SourceDependency{SourceHandle: source.SourceHandle, Fingerprint: fingerprint})
		}
		batchHash, err := ontology.OrganizationBatchHash(sources, identity)
		if err != nil {
			return err
		}
		var before *time.Time
		var previousKey string
		for {
			var body []byte
			var createdAt time.Time
			err = tx.Raw(`SELECT body,created_at,operation_key FROM ontology_assessments
            WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND input_hash=?
              AND batch_hash=? AND body->>'context_hash'=? AND COALESCE(body->'result'->>'failure_code','')=''
              AND (?::timestamptz IS NULL OR created_at<?::timestamptz OR (created_at=?::timestamptz AND operation_key>?))
            ORDER BY created_at DESC,operation_key LIMIT 1`, fence.TeamID, fence.SpaceID, fence.Generation, hash,
				batchHash, contextHash, before, before, before, previousKey).Row().Scan(&body, &createdAt, &previousKey)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			before = &createdAt
			receipt, err = decodeOrganization(body)
			if err != nil {
				return err
			}
			current, err := s.organizationCurrent(tx, fence, receipt)
			if err != nil {
				return err
			}
			if !current {
				continue
			}
			receipt.OperationKey = input.OperationKey
			if err := insertOrganization(tx, fence, receipt); err != nil {
				return err
			}
			result = receipt.Result
			result.Existing = true
			result.Current = true
			found = true
			return nil
		}
	})
	return result, found, err
}

func (s *Store) organizationCurrent(tx *gorm.DB, fence scope, receipt ontology.OrganizationReceipt) (bool, error) {
	if receipt.Result.FailureCode != "" {
		return false, nil
	}
	return s.organizationInputsCurrent(tx, fence, receipt)
}

func (s *Store) organizationInputsCurrent(tx *gorm.DB, fence scope, receipt ontology.OrganizationReceipt) (bool, error) {
	for _, dependency := range receipt.Sources {
		sources, err := organizationSources(tx, fence, []ontology.SourceHandle{dependency.SourceHandle})
		if err != nil {
			return false, err
		}
		fingerprint, err := ontology.SourceFingerprint(sources[0])
		if err != nil {
			return false, err
		}
		if fingerprint != dependency.Fingerprint {
			return false, nil
		}
	}
	ids := []string{}
	for _, dependency := range receipt.Dependencies {
		ids = append(ids, dependency.ID)
	}
	catalog, err := loadHeads(tx, fence, ids, nil, nil)
	if errors.Is(err, ontology.ErrContextBound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, dependency := range receipt.Dependencies {
		record, ok := catalog[dependency.ID]
		if !ok || record.Version != dependency.Version || record.Retired {
			return false, nil
		}
		view, err := s.currentView(tx, fence, record)
		if errors.Is(err, ontology.ErrContextBound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !view.Current {
			return false, nil
		}
	}
	handles := []ontology.SourceHandle{}
	for _, source := range receipt.Sources {
		handles = append(handles, source.SourceHandle)
	}
	contextData, err := s.organizationContext(tx, fence, handles)
	if errors.Is(err, ontology.ErrContextBound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	hash, err := ontology.OrganizationContextHash(contextData)
	return hash == receipt.ContextHash, err
}

func insertOrganization(tx *gorm.DB, fence scope, receipt ontology.OrganizationReceipt) error {
	if err := ontology.ValidateOrganizationReceipt(receipt); err != nil {
		return err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if len(encoded) > ontology.MaxPublicationBytes {
		return ontology.ErrInvalid
	}
	return tx.Exec(`INSERT INTO ontology_assessments(team_id,shared_space_id,space_generation,operation_key,assessment_id,input_hash,batch_hash,body)
        VALUES (?::uuid,?::uuid,?, ?,?::uuid,?,?,?::jsonb)`, fence.TeamID, fence.SpaceID, fence.Generation, receipt.OperationKey, receipt.ID, receipt.InputHash, receipt.BatchHash, string(encoded)).Error
}

func (s *Store) CommitOrganization(ctx context.Context, teamID string, receipt ontology.OrganizationReceipt, input ontology.Publication) (ontology.OrganizationResult, error) {
	if err := requireAutomatic(ctx); err != nil {
		return ontology.OrganizationResult{}, err
	}
	if err := ontology.ValidateOrganizationReceipt(receipt); err != nil {
		return ontology.OrganizationResult{}, err
	}
	var result ontology.OrganizationResult
	err := s.withScope(ctx, teamID, false, func(tx *gorm.DB, fence scope) error {
		if receipt.Result.FailureCode == "" {
			if err := maintenancePublicationFence(ctx, tx, fence); err != nil {
				return err
			}
		}
		if err := lockOrganizationCatalog(tx, fence); err != nil {
			return err
		}
		existing, found, err := organizationByKey(tx, fence, receipt.OperationKey)
		if err != nil {
			return err
		}
		if found {
			if existing.InputHash != receipt.InputHash {
				return ontology.ErrConflict
			}
			result = existing.Result
			result.Existing = true
			return nil
		}
		if receipt.Result.FailureCode == "" {
			current, err := s.organizationCurrent(tx, fence, receipt)
			if err != nil {
				return err
			}
			if !current {
				return ontology.ErrSourceStale
			}
			if len(input.Changes) > 0 {
				if input.OperationKey != receipt.OperationKey {
					return ontology.ErrInvalid
				}
				if err := ontology.ValidatePublication(input); err != nil {
					return err
				}
				publication, err := s.commitPublication(tx, fence, input, "automatic", "", receipt.InputHash, "", receipt.ID)
				if err != nil {
					return err
				}
				receipt.Result.Publication = &publication
				versions := map[string]int64{}
				for _, dependency := range receipt.Dependencies {
					versions[dependency.ID] = dependency.Version
				}
				for _, ref := range publication.Records {
					versions[ref.ID] = ref.Version
				}
				receipt.Dependencies = nil
				ids := []string{}
				for id := range versions {
					ids = append(ids, id)
				}
				catalog, err := loadHeads(tx, fence, ids, nil, nil)
				if err != nil {
					return err
				}
				for id, version := range versions {
					if record := catalog[id]; !record.Retired {
						receipt.Dependencies = append(receipt.Dependencies, ontology.RevisionRef{ID: id, Version: version})
					}
				}
			}
		}
		if receipt.Result.FailureCode == "" {
			handles := []ontology.SourceHandle{}
			for _, source := range receipt.Sources {
				handles = append(handles, source.SourceHandle)
			}
			contextData, err := s.organizationContext(tx, fence, handles)
			if err != nil {
				return err
			}
			receipt.ContextHash, err = ontology.OrganizationContextHash(contextData)
			if err != nil {
				return err
			}
		}
		receipt.Result.Current = receipt.Result.FailureCode == ""
		if err := insertOrganization(tx, fence, receipt); err != nil {
			return err
		}
		result = receipt.Result
		return nil
	})
	return result, err
}

var _ ontology.OrganizationRepository = (*Store)(nil)
