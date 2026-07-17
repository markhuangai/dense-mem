package repository

import (
	"database/sql"
	"encoding/json"

	"github.com/lib/pq"
)

func scanV2DreamCycleRun(rows *sql.Rows) (*V2DreamCycleRun, error) {
	var run V2DreamCycleRun
	var completed sql.NullTime
	if err := rows.Scan(
		&run.TeamID,
		&run.RunID,
		&run.OwnerProfileID,
		&run.RunDate,
		&run.WindowKey,
		&run.Status,
		&run.InputCount,
		&run.CreatedHypotheses,
		&run.RejectedHypotheses,
		&run.Error,
		&run.StartedAt,
		&completed,
	); err != nil {
		return nil, err
	}
	if completed.Valid {
		run.CompletedAt = &completed.Time
	}
	return &run, nil
}

func v2HypothesisSelectSQL(where string) string {
	return `
		SELECT team_id::text, hypothesis_id::text, owner_profile_id::text,
		       status, statement, rationale, likelihood, confidence,
		       COALESCE(subject_entity_id::text, ''), COALESCE(predicate_key, ''),
		       COALESCE(predicate_version, 0), COALESCE(object_entity_id::text, ''),
		       COALESCE(object_value_id::text, ''), source_refs, source_versions,
		       ARRAY(SELECT source_owner_id::text FROM unnest(source_owner_profile_ids) AS source_owner(source_owner_id)),
		       COALESCE(content_hash, ''), COALESCE(cycle_run_id::text, ''),
		       generator_kind, generator_version, invalidated_reason,
		       COALESCE(submitted_ingest_id::text, ''), submitted_at,
		       payload, created_at, updated_at
		FROM hypotheses
	` + where
}

func v2HypothesisUpdateReturningSQL(update string) string {
	return update + `
		RETURNING team_id::text, hypothesis_id::text, owner_profile_id::text,
		          status, statement, rationale, likelihood, confidence,
		          COALESCE(subject_entity_id::text, ''), COALESCE(predicate_key, ''),
		          COALESCE(predicate_version, 0), COALESCE(object_entity_id::text, ''),
		          COALESCE(object_value_id::text, ''), source_refs, source_versions,
		          ARRAY(SELECT source_owner_id::text FROM unnest(source_owner_profile_ids) AS source_owner(source_owner_id)),
		          COALESCE(content_hash, ''), COALESCE(cycle_run_id::text, ''),
		          generator_kind, generator_version, invalidated_reason,
		          COALESCE(submitted_ingest_id::text, ''), submitted_at,
		          payload, created_at, updated_at
	`
}

func scanV2HypothesisRecords(rows *sql.Rows) ([]V2HypothesisRecord, error) {
	records := []V2HypothesisRecord{}
	for rows.Next() {
		record, err := scanV2HypothesisRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

func scanV2HypothesisRecord(rows *sql.Rows) (*V2HypothesisRecord, error) {
	var record V2HypothesisRecord
	var likelihood sql.NullFloat64
	var confidence sql.NullFloat64
	var sourceRefsRaw []byte
	var sourceVersionsRaw []byte
	var ownerIDs pq.StringArray
	var submittedAt sql.NullTime
	var payloadRaw []byte
	if err := rows.Scan(
		&record.TeamID,
		&record.HypothesisID,
		&record.OwnerProfileID,
		&record.Status,
		&record.Statement,
		&record.Rationale,
		&likelihood,
		&confidence,
		&record.SubjectEntityID,
		&record.PredicateKey,
		&record.PredicateVersion,
		&record.ObjectEntityID,
		&record.ObjectValueID,
		&sourceRefsRaw,
		&sourceVersionsRaw,
		&ownerIDs,
		&record.ContentHash,
		&record.CycleRunID,
		&record.GeneratorKind,
		&record.GeneratorVersion,
		&record.InvalidatedReason,
		&record.SubmittedIngestID,
		&submittedAt,
		&payloadRaw,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if likelihood.Valid {
		record.Likelihood = &likelihood.Float64
	}
	if confidence.Valid {
		record.Confidence = &confidence.Float64
	}
	record.SourceOwnerProfileIDs = append([]string(nil), ownerIDs...)
	record.SourceRefs = []map[string]any{}
	_ = json.Unmarshal(sourceRefsRaw, &record.SourceRefs)
	record.SourceVersions = map[string]int{}
	_ = json.Unmarshal(sourceVersionsRaw, &record.SourceVersions)
	record.Payload = map[string]any{}
	_ = json.Unmarshal(payloadRaw, &record.Payload)
	if submittedAt.Valid {
		record.SubmittedAt = &submittedAt.Time
	}
	return &record, nil
}
