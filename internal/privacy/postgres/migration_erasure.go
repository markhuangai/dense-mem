package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type privateErasureDestination struct {
	ID         uuid.UUID
	Generation int64
	State      domain.MemorySpaceLifecycleState
}

func lockPrivateMemoryLineageTx(ctx context.Context, tx *gorm.DB, team, space uuid.UUID, legacy *uuid.UUID) ([]privateErasureDestination, error) {
	if err := tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?::text,573))`, team.String()).Error; err != nil {
		return nil, err
	}
	rows, err := tx.Raw(`SELECT id,generation,lifecycle_state FROM memory_spaces
 WHERE team_id=? AND kind='private' AND id IN (SELECT space_id FROM dense_mem_private_lineage_spaces(?,?))
 ORDER BY id FOR UPDATE`, team, space, legacy).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var destinations []privateErasureDestination
	for rows.Next() {
		var d privateErasureDestination
		if err := rows.Scan(&d.ID, &d.Generation, &d.State); err != nil {
			return nil, err
		}
		destinations = append(destinations, d)
	}
	return destinations, rows.Err()
}

func purgePrivateMemoryLineageTx(ctx context.Context, tx *gorm.DB, space *domain.MemorySpace, operationID uuid.UUID, legacy *uuid.UUID, destinations []privateErasureDestination, ordered []string) (map[string]int64, error) {
	tables, err := inspectPrivateMigrationSchema(tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Exec(`CREATE TEMP TABLE private_memory_purge_rows(table_name TEXT NOT NULL,row_key JSONB NOT NULL,space_id UUID NOT NULL,PRIMARY KEY(table_name,row_key)) ON COMMIT DROP`).Error; err != nil {
		return nil, err
	}
	var originOwner *uuid.UUID
	if err := tx.Raw(`SELECT CASE WHEN migration_archive THEN NULL ELSE owner_membership_id END FROM memory_spaces WHERE id=?`, space.ID).Row().Scan(&originOwner); err != nil {
		return nil, err
	}

	for _, name := range ordered {
		table := tables[name]
		key := migrationKeySQL("row", table.Key)
		query := `INSERT INTO private_memory_purge_rows SELECT ?,` + key + `,row.space_id FROM ` + pq.QuoteIdentifier(name) + ` row WHERE row.team_id=? AND row.space_id IS NOT NULL AND (row.space_id=? OR EXISTS(
   SELECT 1 FROM private_memory_migration_records mapping WHERE mapping.team_id=row.team_id AND mapping.source_table=? AND mapping.destination_key=` + key + ` AND mapping.purged_at IS NULL AND mapping.completed AND (mapping.original_membership_ids @> ARRAY[?::uuid] OR mapping.source_space_id=?::uuid))) ON CONFLICT DO NOTHING`
		if err := tx.Exec(query, name, space.TeamID, space.ID, name, originOwner, legacy).Error; err != nil {
			return nil, err
		}
	}
	for {
		changed := int64(0)
		for _, name := range ordered {
			child := tables[name]
			for _, fk := range child.ForeignKeys {
				parent := tables[fk.Parent]
				if parent == nil {
					continue
				}
				join := ""
				for i, column := range fk.Columns {
					if i > 0 {
						join += " AND "
					}
					join += "child." + pq.QuoteIdentifier(column) + "=parent." + pq.QuoteIdentifier(fk.ParentColumns[i])
				}
				result := tx.Exec(`INSERT INTO private_memory_purge_rows SELECT ?,`+migrationKeySQL("child", child.Key)+`,child.space_id FROM `+pq.QuoteIdentifier(name)+` child
     JOIN `+pq.QuoteIdentifier(parent.Name)+` parent ON `+join+` JOIN private_memory_purge_rows target ON target.table_name=? AND target.row_key=`+migrationKeySQL("parent", parent.Key)+`
     JOIN memory_spaces space ON space.team_id=child.team_id AND space.id=child.space_id AND space.kind='private'
     WHERE child.team_id=? ON CONFLICT DO NOTHING`, name, parent.Name, space.TeamID)
				if result.Error != nil {
					return nil, result.Error
				}
				changed += result.RowsAffected
			}
		}
		// Observations and provider diagnostics can contain quotes from a removed support.
		for _, statement := range []string{
			`INSERT INTO private_memory_purge_rows SELECT 'relationship_observations',jsonb_build_array(observation.team_id,observation.observation_id),observation.space_id
    FROM relationship_observations observation JOIN relationship_evidence_supports support ON support.team_id=observation.team_id AND support.observation_id=observation.observation_id
    JOIN private_memory_purge_rows target ON target.table_name='relationship_evidence_supports' AND target.row_key=jsonb_build_array(support.team_id,support.support_id) ON CONFLICT DO NOTHING`,
			`INSERT INTO private_memory_purge_rows SELECT 'semantic_assessments',jsonb_build_array(assessment.team_id,assessment.semantic_assessment_id),assessment.space_id
    FROM semantic_assessments assessment JOIN relationship_observations observation ON observation.team_id=assessment.team_id AND observation.remember_attempt_id=assessment.attempt_id
    JOIN private_memory_purge_rows target ON target.table_name='relationship_observations' AND target.row_key=jsonb_build_array(observation.team_id,observation.observation_id) WHERE assessment.space_id IS NOT NULL ON CONFLICT DO NOTHING`,
			`INSERT INTO private_memory_purge_rows SELECT 'remember_attempt_diagnostics',jsonb_build_array(diagnostic.team_id,diagnostic.diagnostic_id),diagnostic.space_id
    FROM remember_attempt_diagnostics diagnostic JOIN semantic_assessments assessment ON assessment.team_id=diagnostic.team_id AND assessment.attempt_id=diagnostic.attempt_id
    JOIN private_memory_purge_rows target ON target.table_name='semantic_assessments' AND target.row_key=jsonb_build_array(assessment.team_id,assessment.semantic_assessment_id) WHERE diagnostic.space_id IS NOT NULL ON CONFLICT DO NOTHING`,
			`INSERT INTO private_memory_purge_rows SELECT 'remember_invocation_diagnostics',jsonb_build_array(diagnostic.team_id,diagnostic.invocation_id),diagnostic.space_id
    FROM remember_invocation_diagnostics diagnostic JOIN semantic_assessments assessment ON assessment.team_id=diagnostic.team_id AND assessment.attempt_id=diagnostic.canonical_attempt_id
    JOIN private_memory_purge_rows target ON target.table_name='semantic_assessments' AND target.row_key=jsonb_build_array(assessment.team_id,assessment.semantic_assessment_id) ON CONFLICT DO NOTHING`,
		} {
			result := tx.Exec(statement)
			if result.Error != nil {
				return nil, result.Error
			}
			changed += result.RowsAffected
		}
		if changed == 0 {
			break
		}
	}
	if err := tx.Exec(`CREATE TEMP TABLE private_memory_purge_relationships ON COMMIT DROP AS SELECT DISTINCT support.team_id,support.relationship_id,support.space_id
  FROM relationship_evidence_supports support JOIN private_memory_purge_rows target ON target.table_name='relationship_evidence_supports' AND target.row_key=jsonb_build_array(support.team_id,support.support_id)`).Error; err != nil {
		return nil, err
	}
	if err := privateMemoryRetryTombstonesTx(tx); err != nil {
		return nil, err
	}
	// Search projections are content-bearing copies even though their source references are polymorphic.
	if err := tx.Exec(`INSERT INTO private_memory_purge_rows SELECT 'search_documents',jsonb_build_array(document.team_id,document.search_document_id),document.space_id FROM search_documents document
  JOIN private_memory_purge_rows target ON target.space_id=document.space_id AND target.table_name=CASE document.source_kind WHEN 'evidence' THEN 'evidence_fragments' WHEN 'relationship' THEN 'relationship_records' ELSE '' END AND target.row_key=jsonb_build_array(document.team_id,document.source_id)
  ON CONFLICT DO NOTHING`).Error; err != nil {
		return nil, err
	}

	var actual pq.StringArray
	if err := tx.Raw(`SELECT COALESCE(array_agg(DISTINCT space_id::text),ARRAY[]::text[]) FROM private_memory_purge_rows`).Row().Scan(&actual); err != nil {
		return nil, err
	}
	if err := tx.Exec(`INSERT INTO private_memory_purge_rows SELECT 'session_extraction_checkpoints',jsonb_build_array(checkpoint.team_id,checkpoint.space_id,checkpoint.submission_id,checkpoint.window_index),checkpoint.space_id
  FROM session_extraction_checkpoints checkpoint JOIN session_submissions submission USING(team_id,space_id,submission_id)
  WHERE checkpoint.space_id=ANY(?::uuid[]) AND (submission.result IS NULL OR submission.result->>'processing_state'<>'completed') ON CONFLICT DO NOTHING`, pq.Array(actual)).Error; err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, id := range actual {
		locked := false
		for _, destination := range destinations {
			if destination.ID.String() == id {
				locked = true
				break
			}
		}
		if !locked {
			return nil, fmt.Errorf("%w: dependent destination was not locked", ErrPrivateMemoryManifest)
		}
	}
	for _, name := range ordered {
		for _, destination := range destinations {
			if err := tx.Exec(`SELECT set_config('app.private_erasure_space_id',?,true)`, destination.ID.String()).Error; err != nil {
				return nil, err
			}
			result := tx.Exec(`DELETE FROM `+pq.QuoteIdentifier(name)+` row USING private_memory_purge_rows target WHERE target.table_name=? AND target.space_id=? AND target.row_key=`+migrationKeySQL("row", tables[name].Key), name, destination.ID)
			if result.Error != nil {
				return nil, fmt.Errorf("purge private lineage %s: %w", name, result.Error)
			}
			counts[name] += result.RowsAffected
		}
	}
	for _, name := range ordered {
		var remaining int64
		if err := tx.Raw(`SELECT count(*) FROM `+pq.QuoteIdentifier(name)+` row JOIN private_memory_purge_rows target ON target.table_name=? AND target.row_key=`+migrationKeySQL("row", tables[name].Key), name).Row().Scan(&remaining); err != nil {
			return nil, err
		}
		if remaining != 0 {
			return nil, fmt.Errorf("%w: lineage purge retained %s rows", ErrPrivateMemoryManifest, name)
		}
	}

	if err := tx.Exec(`UPDATE private_memory_migration_records mapping SET purged_at=now() FROM private_memory_purge_rows target WHERE mapping.source_table=target.table_name AND mapping.destination_key=target.row_key AND mapping.destination_space_id=target.space_id`).Error; err != nil {
		return nil, err
	}
	if err := invalidatePrivateLineageRelationshipsTx(tx); err != nil {
		return nil, err
	}
	counts["audit_log"], err = scrubPrivateMemoryLineageAuditTx(ctx, tx, space, originOwner, legacy, destinations)
	if err != nil {
		return nil, err
	}
	if err := tx.Exec(`SELECT set_config('app.private_erasure_operation_id',?,true)`, operationID.String()).Error; err != nil {
		return nil, err
	}
	for _, destination := range destinations {
		if destination.ID == space.ID || destination.State != domain.MemorySpaceActive {
			continue
		}
		affected := false
		for _, id := range actual {
			if destination.ID.String() == id {
				affected = true
				break
			}
		}
		if !affected {
			continue
		}
		if err := tx.Exec(`UPDATE memory_spaces SET generation=generation+1,updated_at=now() WHERE id=? AND generation=? AND lifecycle_state='active'`, destination.ID, destination.Generation).Error; err != nil {
			return nil, err
		}
		if err := tx.Exec(`SELECT set_config('app.private_erasure_rebase_space_id',?,true)`, destination.ID.String()).Error; err != nil {
			return nil, err
		}
		for _, name := range ordered {
			if err := tx.Exec(`UPDATE `+pq.QuoteIdentifier(name)+` SET space_generation=space_generation+1 WHERE space_id=? AND space_generation=?`, destination.ID, destination.Generation).Error; err != nil {
				return nil, err
			}
		}
		if err := tx.Exec(`UPDATE private_memory_migration_records SET destination_generation=destination_generation+1 WHERE destination_space_id=? AND destination_generation=? AND purged_at IS NULL`, destination.ID, destination.Generation).Error; err != nil {
			return nil, err
		}
	}
	if err := tx.Exec(`SELECT set_config('app.private_erasure_rebase_space_id','',true),set_config('app.private_erasure_space_id',?,true)`, space.ID.String()).Error; err != nil {
		return nil, err
	}
	return counts, nil
}

func scrubPrivateMemoryLineageAuditTx(ctx context.Context, tx *gorm.DB, space *domain.MemorySpace, owner, legacy *uuid.UUID, destinations []privateErasureDestination) (int64, error) {
	var count int64
	for _, destination := range destinations {
		if err := tx.Exec(`SELECT set_config('app.private_erasure_space_id',?,true)`, destination.ID.String()).Error; err != nil {
			return 0, err
		}
		predicate := `audit.team_id=? AND audit.memory_space_id=? AND (audit.memory_space_id=? OR EXISTS(
 SELECT 1 FROM private_memory_migration_records mapping WHERE mapping.team_id=audit.team_id AND mapping.source_table='audit_log'
 AND mapping.destination_key=jsonb_build_array(audit.id) AND mapping.destination_space_id=audit.memory_space_id AND mapping.completed AND mapping.purged_at IS NULL
 AND (mapping.original_membership_ids @> ARRAY[?::uuid] OR mapping.source_space_id=?::uuid)))`
		args := []any{space.TeamID, destination.ID, space.ID, owner, legacy}
		result := tx.WithContext(ctx).Exec(`UPDATE audit_log audit SET before_payload=NULL,after_payload=NULL,metadata=?::jsonb WHERE `+predicate+`
 AND (before_payload IS NOT NULL OR after_payload IS NOT NULL OR metadata IS DISTINCT FROM ?::jsonb)`, append(append([]any{privateMemoryAuditMetadataJSON}, args...), privateMemoryAuditMetadataJSON)...)
		if result.Error != nil {
			return 0, fmt.Errorf("scrub private audit lineage: %w", result.Error)
		}
		count += result.RowsAffected
		if err := tx.Exec(`UPDATE private_memory_migration_records mapping SET purged_at=now() WHERE mapping.source_table='audit_log' AND mapping.purged_at IS NULL
 AND EXISTS(SELECT 1 FROM audit_log audit WHERE `+predicate+` AND mapping.destination_key=jsonb_build_array(audit.id) AND mapping.destination_space_id=audit.memory_space_id)`, args...).Error; err != nil {
			return 0, err
		}
	}
	return count, nil
}

func privateMemoryRetryTombstonesTx(tx *gorm.DB) error {
	for _, entry := range []struct{ Table, Kind, ID string }{{"knowledge_ingests", "remember", "ingest_id"}, {"remember_attempts", "remember", "attempt_id"}, {"session_submissions", "session", "submission_id"}} {
		key := `jsonb_build_array(row.team_id,row.` + entry.ID + `)`
		if entry.Kind == "session" {
			key = `jsonb_build_array(row.team_id,row.space_id,row.submission_id)`
		}
		if err := tx.Exec(`INSERT INTO private_memory_erased_operations(team_id,owner_profile_id,operation_kind,key_hash)
   SELECT row.team_id,row.owner_profile_id,?,encode(sha256(convert_to(row.idempotency_key,'UTF8')),'hex') FROM `+pq.QuoteIdentifier(entry.Table)+` row
   JOIN private_memory_purge_rows target ON target.table_name=? AND target.row_key=`+key+` WHERE row.idempotency_key<>'' ON CONFLICT DO NOTHING`, entry.Kind, entry.Table).Error; err != nil {
			return err
		}
	}
	return nil
}

func invalidatePrivateLineageRelationshipsTx(tx *gorm.DB) error {
	rows, err := tx.Raw(`SELECT relationship.team_id::text,relationship.relationship_id::text,relationship.owner_profile_id::text,relationship.space_id::text,relationship.space_generation,
  relationship.status,relationship.version,relationship.support_count,relationship.source_group_count,COALESCE(relationship.identity_alias_of_relationship_id::text,''),
  relationship.subject_entity_id::text,relationship.predicate_key,COALESCE(relationship.object_entity_id::text,''),COALESCE(relationship.object_value_id::text,''),relationship.polarity,COALESCE(relationship.scope_key,''),relationship.valid_from,relationship.valid_to
  FROM relationship_records relationship JOIN private_memory_purge_relationships affected USING(team_id,relationship_id) ORDER BY relationship.relationship_id FOR UPDATE OF relationship`).Rows()
	if err != nil {
		return err
	}
	var records []knowledge.RelationshipRecord
	for rows.Next() {
		var record knowledge.RelationshipRecord
		if err = rows.Scan(&record.TeamID, &record.RelationshipID, &record.OwnerProfileID, &record.SpaceID, &record.SpaceGeneration, &record.Status, &record.Version, &record.SupportCount, &record.SourceGroupCount, &record.IdentityAliasOfID, &record.SubjectEntityID, &record.PredicateKey, &record.ObjectEntityID, &record.ObjectValueID, &record.Polarity, &record.ScopeKey, &record.ValidFrom, &record.ValidTo); err != nil {
			rows.Close()
			return err
		}
		records = append(records, record)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := refreshPrivateRelationshipTx(tx, record, "private supporting memory erased"); err != nil {
			return err
		}
	}
	return nil
}

func refreshPrivateRelationshipTx(tx *gorm.DB, record knowledge.RelationshipRecord, reason string) error {
	counts, err := storage.RelationshipSupportCountsTx(tx.Statement.Context, tx, record.TeamID, record.RelationshipID)
	if err != nil {
		return err
	}
	next := knowledge.StatusForEffectiveSupport(record.Status, counts.SupportCount)
	if record.IdentityAliasOfID != "" {
		next = record.Status
	}
	if next != record.Status {
		if err := tx.Exec(`INSERT INTO relationship_transition_events(team_id,relationship_id,owner_profile_id,from_status,to_status,reason,space_id,space_generation)
   VALUES(?::uuid,?::uuid,?::uuid,?,?,?,?::uuid,?)`, record.TeamID, record.RelationshipID, record.OwnerProfileID, record.Status, next, reason, record.SpaceID, record.SpaceGeneration).Error; err != nil {
			return err
		}
	}
	if counts.SupportCount != record.SupportCount || counts.SourceGroupCount != record.SourceGroupCount || next != record.Status {
		record.Version++
	}
	record.Status = next
	if err := tx.Exec(`UPDATE relationship_records SET status=?,support_count=?,source_group_count=?,version=?,updated_at=now() WHERE team_id=?::uuid AND relationship_id=?::uuid`, next, counts.SupportCount, counts.SourceGroupCount, record.Version, record.TeamID, record.RelationshipID).Error; err != nil {
		return err
	}
	text, err := storage.RelationshipProjectionTextTx(tx.Statement.Context, tx, &record)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(text))
	hash := hex.EncodeToString(sum[:])
	if err := tx.Exec(`UPDATE search_documents SET source_version=?,document_version=document_version+1,
   search_state=CASE WHEN ? THEN CASE WHEN document_hash=? AND embedding IS NOT NULL AND projection_format_version=2 THEN 'current' ELSE 'pending' END ELSE 'not_required' END,
   embedding=CASE WHEN ? AND document_hash=? AND projection_format_version=2 THEN embedding ELSE NULL END,
   embedding_updated_at=CASE WHEN ? AND document_hash=? AND projection_format_version=2 THEN embedding_updated_at ELSE NULL END,
   document_text=?,document_hash=?,embedding_error='',updated_at=now()
   WHERE team_id=?::uuid AND space_id=?::uuid AND source_kind='relationship' AND source_id=?::uuid`, record.Version, next == "active", hash, next == "active", hash, next == "active", hash, text, hash, record.TeamID, record.SpaceID, record.RelationshipID).Error; err != nil {
		return err
	}
	return nil
}
