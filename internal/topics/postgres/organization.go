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
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) ReadOrganization(ctx context.Context, teamID string, handles []topic.SourceHandle) (topic.OrganizationContext, error) {
	return s.readOrganization(ctx, teamID, handles, false)
}

func (s *Store) ReadEquivalence(ctx context.Context, teamID string, handles []topic.SourceHandle) (topic.OrganizationContext, error) {
	return s.readOrganization(ctx, teamID, handles, true)
}

func (s *Store) readOrganization(ctx context.Context, teamID string, handles []topic.SourceHandle, equivalenceOnly bool) (topic.OrganizationContext, error) {
	if err := requireAutomatic(ctx); err != nil {
		return topic.OrganizationContext{}, err
	}
	input, err := topic.PrepareOrganizationInput(topic.OrganizationInput{OperationKey: "read-context", Sources: handles})
	if err != nil {
		return topic.OrganizationContext{}, err
	}
	var result topic.OrganizationContext
	err = s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		var err error
		result, err = s.assessmentContext(tx, fence, input.Sources, equivalenceOnly)
		return err
	})
	return result, err
}

func (s *Store) ReadDefinitionHeads(ctx context.Context, teamID string, definition topic.Record) ([]topic.RecordView, error) {
	if err := requireAutomatic(ctx); err != nil {
		return nil, err
	}
	if err := topic.ValidateRecord(definition); err != nil {
		return nil, err
	}
	if definition.Definition == nil || definition.Retired {
		return nil, topic.ErrInvalid
	}
	var result []topic.RecordView
	err := s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		heads, err := validationCatalog(tx, fence, []topic.Record{definition}, nil)
		if err != nil {
			return err
		}
		var records []topic.Record
		for _, record := range heads {
			if record.Definition == nil || record.Retired || record.Kind != definition.Kind {
				continue
			}
			records = append(records, record)
		}
		result, err = s.organizationViews(tx, fence, records)
		return err
	})
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, err
}

func (s *Store) organizationContext(tx *gorm.DB, fence scope, handles []topic.SourceHandle) (topic.OrganizationContext, error) {
	return s.assessmentContext(tx, fence, handles, false)
}

func (s *Store) assessmentContext(tx *gorm.DB, fence scope, handles []topic.SourceHandle, equivalenceOnly bool) (topic.OrganizationContext, error) {
	var result topic.OrganizationContext
	var err error
	result.Sources, err = organizationSources(tx, fence, handles)
	if err != nil {
		return result, err
	}
	records, err := organizationSourceRecords(tx, fence, handles, equivalenceOnly)
	if err != nil {
		return result, err
	}
	result.Records, err = s.organizationViews(tx, fence, records)
	if err != nil {
		return result, err
	}
	if equivalenceOnly {
		result.Revision, err = catalogRevision(tx, fence)
		return result, err
	}
	forced := []string{}
	unclassified := []topic.SourceSnapshot{}
	for _, source := range result.Sources {
		locked, preferred := topic.OrganizationClassification(source, result.Records, nil)
		if locked != "" {
			forced = append(forced, locked)
		}
		if preferred != "" {
			forced = append(forced, preferred)
		}
		if locked == "" {
			unclassified = append(unclassified, source)
		}
	}
	result.Candidates, err = s.organizationVocabulary(tx, fence, unclassified, forced)
	if err != nil {
		return result, err
	}
	for _, source := range result.Sources {
		_, preferred := topic.OrganizationClassification(source, result.Records, result.Candidates)
		if preferred != "" {
			forced = append(forced, preferred)
		}
	}
	if err := s.organizationDefinitionContext(tx, fence, &result, forced); err != nil {
		return result, err
	}
	result.Revision, err = catalogRevision(tx, fence)
	return result, err
}

func organizationSourceRecords(tx *gorm.DB, fence scope, handles []topic.SourceHandle, groupingOnly bool) ([]topic.Record, error) {
	kinds := make([]string, 0, len(handles))
	ids := make([]string, 0, len(handles))
	for _, handle := range handles {
		kinds = append(kinds, string(handle.Kind))
		ids = append(ids, handle.ID)
	}
	filter := "(NOT head.retired OR head.kind IN ('evidence_group','relationship_group'))"
	if groupingOnly {
		filter = "head.kind='evidence_group'"
	}
	rows, err := tx.Raw(`WITH matched AS MATERIALIZED (
		SELECT DISTINCT head.team_id,head.shared_space_id,head.space_generation,head.record_id,head.version,head.kind
		FROM unnest(?::text[],?::text[]) AS source(kind,id)
		JOIN topic_source_dependencies AS dependency ON dependency.source_kind=source.kind AND dependency.source_id=source.id
		JOIN topic_record_heads AS head ON (head.team_id,head.shared_space_id,head.space_generation,head.record_id,head.version)=
			(dependency.team_id,dependency.shared_space_id,dependency.space_generation,dependency.record_id,dependency.record_version)
		WHERE dependency.team_id=?::uuid AND dependency.shared_space_id=?::uuid AND dependency.space_generation=?
		  AND `+filter+`
		ORDER BY head.record_id LIMIT ?
	)
	SELECT revision.body FROM matched
	JOIN LATERAL (
		SELECT body FROM topic_revision_records AS record
		WHERE (record.team_id,record.shared_space_id,record.space_generation,record.record_id,record.version,record.kind)=
			(matched.team_id,matched.shared_space_id,matched.space_generation,matched.record_id,matched.version,matched.kind)
		LIMIT 1
	) AS revision ON true
	ORDER BY matched.record_id`, pq.Array(kinds), pq.Array(ids), fence.TeamID, fence.SpaceID, fence.Generation, topic.MaxDependencyRecords+1).Rows()
	if err != nil {
		return nil, err
	}
	var records []topic.Record
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
	if len(records) > topic.MaxDependencyRecords {
		return nil, topic.ErrContextBound
	}
	return records, nil
}

func organizationSources(tx *gorm.DB, fence scope, handles []topic.SourceHandle) ([]topic.SourceSnapshot, error) {
	snapshots, err := readSources(tx, fence, handles)
	if err != nil {
		return nil, err
	}
	result := make([]topic.SourceSnapshot, 0, len(handles))
	for _, handle := range handles {
		snapshot := snapshots[handle]
		if !snapshot.Eligible || snapshot.SourceHandle != handle {
			snapshot = topic.SourceSnapshot{SourceHandle: handle, TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation, State: map[string]string{}}
		}
		result = append(result, snapshot)
	}
	return result, nil
}

type vocabularyCandidate struct {
	record        topic.Record
	forced, exact bool
	rank          float32
}

func (s *Store) organizationVocabulary(tx *gorm.DB, fence scope, sources []topic.SourceSnapshot, forced []string) ([]topic.RecordView, error) {
	if forced == nil {
		forced = []string{}
	}
	_, words := topic.VocabularyQuery(sources)
	if words == nil {
		words = []string{}
	}
	query := topic.LooseVocabularyQuery(words)
	stopWords := pq.Array(topic.VocabularyStopWords())
	result := []topic.RecordView{}
	after := vocabularyCandidate{}
	examined := 0
	for {
		limit := min(topic.MaxVocabularyCandidates, topic.MaxDependencyRecords-examined+1)
		rows, err := tx.Raw(`WITH ranked AS (
   SELECT revision.body,head.record_id,
    head.record_id=ANY(?::uuid[]) AS forced,head.names && ?::text[] AS exact,
    ts_rank_cd(`+vocabularyVectorSQL+`,
     websearch_to_tsquery('simple',?)) AS rank
   FROM topic_record_heads AS head
   JOIN topic_revision_records AS revision USING(team_id,shared_space_id,space_generation,record_id,version)
   WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
    AND NOT head.retired AND head.kind IN ('topic')
    AND (head.record_id=ANY(?::uuid[]) OR head.names && ?::text[]
     OR `+vocabularyVectorSQL+`
       @@ websearch_to_tsquery('simple',?)))
   SELECT body,forced,exact,rank,record_id::text FROM ranked
   WHERE ?='' OR (forced,exact,rank)<(?::boolean,?::boolean,?::real)
    OR ((forced,exact,rank)=(?::boolean,?::boolean,?::real) AND record_id>NULLIF(?,'')::uuid)
   ORDER BY forced DESC,exact DESC,rank DESC,record_id LIMIT ?`,
			pq.Array(forced), pq.Array(words), stopWords, query, fence.TeamID, fence.SpaceID, fence.Generation,
			pq.Array(forced), pq.Array(words), stopWords, query, after.record.ID, after.forced, after.exact, after.rank,
			after.forced, after.exact, after.rank, after.record.ID, limit).Rows()
		if err != nil {
			return nil, err
		}
		candidates := []vocabularyCandidate{}
		for rows.Next() {
			var body []byte
			var candidate vocabularyCandidate
			var id string
			if err := rows.Scan(&body, &candidate.forced, &candidate.exact, &candidate.rank, &id); err != nil {
				rows.Close()
				return nil, err
			}
			candidate.record, err = decodeRecord(body)
			if err != nil {
				rows.Close()
				return nil, err
			}
			candidates = append(candidates, candidate)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		for offset := 0; offset < len(candidates); {
			if examined == topic.MaxDependencyRecords {
				return nil, topic.ErrContextBound
			}
			end := min(len(candidates), offset+topic.MaxVocabularyCandidates-len(result), offset+topic.MaxDependencyRecords-examined)
			records := make([]topic.Record, 0, end-offset)
			for _, candidate := range candidates[offset:end] {
				records = append(records, candidate.record)
			}
			views, err := s.organizationViews(tx, fence, records)
			if err != nil {
				return nil, err
			}
			for i, view := range views {
				examined++
				after = candidates[offset+i]
				if view.Current {
					result = append(result, view)
					if len(result) == topic.MaxVocabularyCandidates {
						return result, nil
					}
				}
			}
			offset = end
		}
		if len(candidates) < limit {
			return result, nil
		}
	}
}

func decodeOrganization(body []byte) (topic.OrganizationReceipt, error) {
	var receipt topic.OrganizationReceipt
	if err := jsonstrict.Decode(strings.NewReader(string(body)), &receipt, topic.MaxPublicationBytes); err != nil {
		return receipt, err
	}
	return receipt, topic.ValidateOrganizationReceipt(receipt)
}

const organizationCatalogLockClass = 25021

func lockOrganizationCatalog(tx *gorm.DB, fence scope) error {
	return tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?::text,?))`, fence.SpaceID, organizationCatalogLockClass).Error
}

func tryLockOrganizationCatalog(tx *gorm.DB, fence scope) (bool, error) {
	var locked bool
	err := tx.Raw(`SELECT pg_try_advisory_xact_lock(hashtextextended(?::text,?))`, fence.SpaceID, organizationCatalogLockClass).Row().Scan(&locked)
	return locked, err
}

func organizationByKey(tx *gorm.DB, fence scope, key string, equivalenceOnly bool) (topic.OrganizationReceipt, bool, error) {
	var body []byte
	err := tx.Raw(`SELECT body FROM `+organizationAssessmentTable(equivalenceOnly)+` WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND operation_key=?`, fence.TeamID, fence.SpaceID, fence.Generation, key).Row().Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return topic.OrganizationReceipt{}, false, nil
	}
	if err != nil {
		return topic.OrganizationReceipt{}, false, err
	}
	receipt, err := decodeOrganization(body)
	return receipt, true, err
}

func organizationAssessmentTable(equivalenceOnly bool) string {
	if equivalenceOnly {
		return "evidence_equivalence_outcomes"
	}
	return "historical_organization_assessments"
}

func (s *Store) FindOrganization(ctx context.Context, teamID string, input topic.OrganizationInput, identity string) (topic.OrganizationResult, bool, error) {
	if err := requireAutomatic(ctx); err != nil {
		return topic.OrganizationResult{}, false, err
	}
	input, err := topic.PrepareOrganizationInput(input)
	if err != nil {
		return topic.OrganizationResult{}, false, err
	}
	hash, err := topic.OrganizationInputHash(input, identity)
	if err != nil {
		return topic.OrganizationResult{}, false, err
	}
	var result topic.OrganizationResult
	found := false
	err = s.withScope(ctx, teamID, false, func(tx *gorm.DB, fence scope) error {
		if input.EquivalenceOnly {
			if err := maintenancePublicationFence(ctx, tx, fence); err != nil {
				return err
			}
		}
		if err := lockOrganizationCatalog(tx, fence); err != nil {
			return err
		}
		receipt, exists, err := organizationByKey(tx, fence, input.OperationKey, input.EquivalenceOnly)
		if err != nil {
			return err
		}
		if exists {
			if receipt.InputHash != hash {
				return topic.ErrConflict
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
		contextData, err := s.assessmentContext(tx, fence, input.Sources, input.EquivalenceOnly)
		if errors.Is(err, topic.ErrContextBound) {
			return nil
		}
		if err != nil {
			return err
		}
		contextHash, err := topic.OrganizationContextHash(contextData)
		if err != nil {
			return err
		}
		sources := make([]topic.SourceDependency, 0, len(contextData.Sources))
		for _, source := range contextData.Sources {
			fingerprint, err := topic.SourceFingerprint(source)
			if err != nil {
				return err
			}
			sources = append(sources, topic.SourceDependency{SourceHandle: source.SourceHandle, Fingerprint: fingerprint})
		}
		batchHash, err := topic.OrganizationBatchHash(sources, identity)
		if err != nil {
			return err
		}
		var before *time.Time
		var previousKey string
		for {
			var body []byte
			var createdAt time.Time
			err = tx.Raw(`SELECT body,created_at,operation_key FROM `+organizationAssessmentTable(input.EquivalenceOnly)+`
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

func (s *Store) organizationCurrent(tx *gorm.DB, fence scope, receipt topic.OrganizationReceipt) (bool, error) {
	if receipt.Result.FailureCode != "" {
		return false, nil
	}
	return s.organizationInputsCurrent(tx, fence, receipt)
}

func insertOrganization(tx *gorm.DB, fence scope, receipt topic.OrganizationReceipt) error {
	if err := topic.ValidateOrganizationReceipt(receipt); err != nil {
		return err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if len(encoded) > topic.MaxPublicationBytes {
		return topic.ErrInvalid
	}
	return tx.Exec(`INSERT INTO `+organizationAssessmentTable(receipt.EquivalenceOnly)+`(team_id,shared_space_id,space_generation,operation_key,assessment_id,input_hash,batch_hash,body)
        VALUES (?::uuid,?::uuid,?, ?,?::uuid,?,?,?::jsonb)`, fence.TeamID, fence.SpaceID, fence.Generation, receipt.OperationKey, receipt.ID, receipt.InputHash, receipt.BatchHash, string(encoded)).Error
}

func (s *Store) CommitOrganization(ctx context.Context, teamID string, receipt topic.OrganizationReceipt, input topic.Publication) (result topic.OrganizationResult, err error) {
	if err := requireAutomatic(ctx); err != nil {
		return topic.OrganizationResult{}, err
	}
	ctx, diagnostic, finish := topic.ObservePublication(ctx, s.publicationObserver, teamID, "automatic", input)
	defer func() {
		if diagnostic != nil {
			diagnostic.Result = result.Publication
			diagnostic.AssessmentID = receipt.ID
			diagnostic.ProviderIdentity = receipt.ProviderIdentity
		}
		finish(err)
	}()
	if err := topic.ValidateOrganizationReceipt(receipt); err != nil {
		return topic.OrganizationResult{}, err
	}
	err = s.withScope(ctx, teamID, false, func(tx *gorm.DB, fence scope) error {
		if receipt.Result.FailureCode == "" {
			if err := maintenancePublicationFence(ctx, tx, fence); err != nil {
				return err
			}
		}
		if err := lockOrganizationCatalog(tx, fence); err != nil {
			return err
		}
		existing, found, err := organizationByKey(tx, fence, receipt.OperationKey, receipt.EquivalenceOnly)
		if err != nil {
			return err
		}
		if found {
			if existing.InputHash != receipt.InputHash {
				return topic.ErrConflict
			}
			result = existing.Result
			result.Existing = true
			return nil
		}
		if receipt.Result.FailureCode == "" {
			rejection, err := s.organizationInputRejection(tx, fence, receipt)
			if err != nil {
				return err
			}
			if rejection != nil {
				return &topic.FreshnessError{Rejection: *rejection}
			}
			if len(input.Changes) > 0 {
				if receipt.EquivalenceOnly {
					for _, change := range input.Changes {
						if change.Record.Kind != topic.EvidenceGroup {
							return topic.ErrInvalid
						}
					}
				}
				if input.OperationKey != receipt.OperationKey {
					return topic.ErrInvalid
				}
				if err := topic.ValidatePublication(input); err != nil {
					return err
				}
				input.ExpectedRevision, err = catalogRevision(tx, fence)
				if err != nil {
					return err
				}
				publication, err := s.writePublication(tx, fence, input, "automatic", "", receipt.InputHash, receipt.ID, false)
				if err != nil {
					return err
				}
				receipt.Result.Publication = &publication
			}
		}
		if receipt.Result.FailureCode == "" {
			handles := []topic.SourceHandle{}
			for _, source := range receipt.Sources {
				handles = append(handles, source.SourceHandle)
			}
			contextData, err := s.assessmentContext(tx, fence, handles, receipt.EquivalenceOnly)
			if err != nil {
				return err
			}
			receipt.Dependencies, err = topic.OrganizationDependencies(contextData)
			if err != nil {
				return err
			}
			receipt.ContextHash, err = topic.OrganizationContextHash(contextData)
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

var _ topic.OrganizationRepository = (*Store)(nil)
