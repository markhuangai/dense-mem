package repository

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"
)

var dreamCycleRunSelectColumns = dreamCycleRunColumns("")

func dreamCycleRunColumns(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return fmt.Sprintf(`%[1]steam_id::text, %[1]srun_id::text,
       COALESCE(%[1]sinitiated_by_profile_id::text, ''), %[1]srun_date, %[1]swindow_key,
       %[1]sstatus, %[1]sscheduled_for, COALESCE(%[1]slease_token::text, ''), %[1]slease_until,
       %[1]sattempt_count, %[1]sinput_count, %[1]screated_hypotheses, %[1]srejected_hypotheses,
       %[1]sprovider_model, %[1]sprovider_turns, %[1]sprovider_input_tokens,
       %[1]sprovider_output_tokens, %[1]sattempted_paths, %[1]sprovider_proposals,
       %[1]soutcome_summary, %[1]serror, %[1]sstarted_at, %[1]scompleted_at`, prefix)
}

func scanDreamCycleRun(rows *sql.Rows) (*DreamCycleRun, error) {
	var run DreamCycleRun
	var scheduledFor sql.NullTime
	var leaseToken sql.NullString
	var leaseUntil sql.NullTime
	var completed sql.NullTime
	var outcomeSummaryRaw []byte
	if err := rows.Scan(
		&run.TeamID,
		&run.RunID,
		&run.InitiatedByProfileID,
		&run.RunDate,
		&run.WindowKey,
		&run.Status,
		&scheduledFor,
		&leaseToken,
		&leaseUntil,
		&run.AttemptCount,
		&run.InputCount,
		&run.CreatedHypotheses,
		&run.RejectedHypotheses,
		&run.ProviderModel,
		&run.ProviderTurns,
		&run.ProviderInputTokens,
		&run.ProviderOutputTokens,
		&run.AttemptedPaths,
		&run.ProviderProposals,
		&outcomeSummaryRaw,
		&run.Error,
		&run.StartedAt,
		&completed,
	); err != nil {
		return nil, err
	}
	if completed.Valid {
		run.CompletedAt = &completed.Time
	}
	if scheduledFor.Valid {
		run.ScheduledFor = &scheduledFor.Time
	}
	if leaseToken.Valid {
		run.LeaseToken = leaseToken.String
	}
	if leaseUntil.Valid {
		run.LeaseUntil = &leaseUntil.Time
	}
	run.OutcomeSummary = map[string]int{}
	_ = json.Unmarshal(outcomeSummaryRaw, &run.OutcomeSummary)
	return &run, nil
}

func hypothesisSelectSQL(where string) string {
	return `
		SELECT team_id::text, hypothesis_id::text, COALESCE(created_by_profile_id::text, ''),
		       status, statement, rationale, likelihood, confidence,
		       COALESCE(subject_entity_id::text, ''), COALESCE(predicate_key, ''),
		       COALESCE(predicate_version, 0), COALESCE(object_entity_id::text, ''),
		       COALESCE(object_value_id::text, ''), source_refs, source_versions,
		       ARRAY(SELECT source_owner_id::text FROM unnest(source_owner_profile_ids) AS source_owner(source_owner_id)),
		       COALESCE(content_hash, ''), COALESCE(target_identity, ''), COALESCE(cycle_run_id::text, ''),
		       generator_kind, generator_version, invalidated_reason,
		       COALESCE(submitted_ingest_id::text, ''),
		       COALESCE((
		           SELECT ingest.idempotency_key
		           FROM knowledge_ingests AS ingest
		           WHERE ingest.team_id = hypotheses.team_id
		             AND ingest.ingest_id = hypotheses.submitted_ingest_id
		           LIMIT 1
		       ), ''),
		       COALESCE((
		           SELECT ingest.request_hash
		           FROM knowledge_ingests AS ingest
		           WHERE ingest.team_id = hypotheses.team_id
		             AND ingest.ingest_id = hypotheses.submitted_ingest_id
		           LIMIT 1
		       ), ''),
		       COALESCE((
		           SELECT feedback.decision
		           FROM hypothesis_feedback_events AS feedback
		           WHERE feedback.team_id = hypotheses.team_id
		             AND feedback.hypothesis_id = hypotheses.hypothesis_id
		             AND feedback.submitted_ingest_id = hypotheses.submitted_ingest_id
		             AND feedback.submitted_ingest_id IS NOT NULL
		           ORDER BY feedback.created_at DESC, feedback.feedback_event_id DESC
		           LIMIT 1
		       ), ''),
		       submitted_at,
		       payload, created_at, updated_at
		FROM hypotheses
	` + where
}

func hypothesisUpdateReturningSQL(update string) string {
	return update + `
		RETURNING team_id::text, hypothesis_id::text, COALESCE(created_by_profile_id::text, ''),
		          status, statement, rationale, likelihood, confidence,
		          COALESCE(subject_entity_id::text, ''), COALESCE(predicate_key, ''),
		          COALESCE(predicate_version, 0), COALESCE(object_entity_id::text, ''),
		          COALESCE(object_value_id::text, ''), source_refs, source_versions,
		          ARRAY(SELECT source_owner_id::text FROM unnest(source_owner_profile_ids) AS source_owner(source_owner_id)),
		          COALESCE(content_hash, ''), COALESCE(target_identity, ''), COALESCE(cycle_run_id::text, ''),
		          generator_kind, generator_version, invalidated_reason,
		          COALESCE(submitted_ingest_id::text, ''),
		          COALESCE((
		              SELECT ingest.idempotency_key
		              FROM knowledge_ingests AS ingest
		              WHERE ingest.team_id = hypotheses.team_id
		                AND ingest.ingest_id = hypotheses.submitted_ingest_id
		              LIMIT 1
		          ), ''),
		          COALESCE((
		              SELECT ingest.request_hash
		              FROM knowledge_ingests AS ingest
		              WHERE ingest.team_id = hypotheses.team_id
		                AND ingest.ingest_id = hypotheses.submitted_ingest_id
		              LIMIT 1
		          ), ''),
		          COALESCE((
		              SELECT feedback.decision
		              FROM hypothesis_feedback_events AS feedback
		              WHERE feedback.team_id = hypotheses.team_id
		                AND feedback.hypothesis_id = hypotheses.hypothesis_id
		                AND feedback.submitted_ingest_id = hypotheses.submitted_ingest_id
		                AND feedback.submitted_ingest_id IS NOT NULL
		              ORDER BY feedback.created_at DESC, feedback.feedback_event_id DESC
		              LIMIT 1
		          ), ''),
		          submitted_at,
		          payload, created_at, updated_at
	`
}

func scanHypothesisRecords(rows *sql.Rows) ([]HypothesisRecord, error) {
	records := []HypothesisRecord{}
	for rows.Next() {
		record, err := scanHypothesisRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

func scanHypothesisRecord(rows *sql.Rows) (*HypothesisRecord, error) {
	var record HypothesisRecord
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
		&record.CreatedByProfileID,
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
		&record.TargetIdentity,
		&record.CycleRunID,
		&record.GeneratorKind,
		&record.GeneratorVersion,
		&record.InvalidatedReason,
		&record.SubmittedIngestID,
		&record.SubmittedIngestIdempotencyKey,
		&record.SubmittedIngestRequestHash,
		&record.SubmittedDecision,
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
