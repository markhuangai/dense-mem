package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrSubmissionDiagnosticNotFound = errors.New("submission diagnostic not found")

type SubmissionDiagnosticsRepository interface {
	ListSubmissionDiagnostics(ctx context.Context, filter SubmissionDiagnosticFilter) (*SubmissionDiagnosticRecordPage, error)
	GetSubmissionDiagnostic(ctx context.Context, teamID, submissionID string) (*SubmissionDiagnosticRecord, error)
}

type SubmissionDiagnosticFilter struct {
	TeamID          string
	ProcessingState string
	Limit           int
	Offset          int
}

type SubmissionDiagnosticRecord struct {
	TeamName            string
	SourceTypes         []string
	EvidenceCount       int
	Placement           CreateIngestResult
	OperatorDiagnostic  map[string]any
	OperatorDiagnostics []SubmissionDiagnosticOperatorDiagnostic
}

// SubmissionDiagnosticOperatorDiagnostic is the bounded, append-only failure
// context retained for the control portal. Payload is populated from an SQL
// allowlist; it must never contain evidence, prompts, provider responses, or
// storage error text.
type SubmissionDiagnosticOperatorDiagnostic struct {
	ID              string
	PlacementItemID string
	OutcomeKind     string
	Status          string
	CreatedAt       time.Time
	Payload         map[string]any
}

type SubmissionDiagnosticRecordPage struct {
	Records []SubmissionDiagnosticRecord
	Total   int64
}

var _ SubmissionDiagnosticsRepository = (*LedgerRepositoryImpl)(nil)

const submissionDiagnosticOutcomeLimit = 200

const submissionDiagnosticSourceTypesSQL = `
	COALESCE((
		SELECT jsonb_agg(source_type_row.source_type ORDER BY source_type_row.source_type)
		FROM (
			SELECT DISTINCT fragment.source_type
			FROM evidence_fragments AS fragment
			WHERE fragment.team_id = run.team_id
			  AND fragment.ingest_id = run.ingest_id
		) AS source_type_row
	), '[]'::jsonb)`

const submissionDiagnosticProcessingStateSQL = `
	CASE
		WHEN run.semantic_hold_state IN ('active', 'expired') THEN 'awaiting_review'
		WHEN run.semantic_hold_state = 'superseded' THEN 'rejected'
		WHEN run.status IN ('queued', 'guarded') THEN 'queued'
		WHEN run.status = 'processing' THEN 'processing'
		WHEN run.status = 'completed' THEN 'completed'
		WHEN run.status = 'awaiting_review' THEN 'awaiting_review'
		WHEN run.status = 'quarantined' THEN 'quarantined'
		ELSE 'failed'
	END`

// Keep this projection in SQL so a future payload field cannot accidentally
// cross the operator diagnostics boundary by being selected wholesale.
const submissionDiagnosticSafePayloadSQL = `
	jsonb_strip_nulls(jsonb_build_object(
		'failure_reason_code', {{payload}} -> 'failure_reason_code',
		'failure_stage', {{payload}} -> 'failure_stage',
		'failure_class', {{payload}} -> 'failure_class',
		'validation_stage', {{payload}} -> 'validation_stage',
		'validation_field_families', {{payload}} -> 'validation_field_families',
		'failure_measurement', CASE
			WHEN jsonb_typeof({{payload}} -> 'failure_measurement') = 'object' THEN
				jsonb_strip_nulls(jsonb_build_object(
					'unit', ({{payload}} -> 'failure_measurement') -> 'unit',
					'observed', ({{payload}} -> 'failure_measurement') -> 'observed',
					'observed_at_least', ({{payload}} -> 'failure_measurement') -> 'observed_at_least',
					'limit', ({{payload}} -> 'failure_measurement') -> 'limit'
				))
		END,
		'provider_status', {{payload}} -> 'provider_status',
		'assessor_turns', {{payload}} -> 'assessor_turns',
		'assessor_provider_attempted', {{payload}} -> 'assessor_provider_attempted',
		'hold_issues', {{payload}} -> 'hold_issues',
		'hold_issues_truncated', {{payload}} -> 'hold_issues_truncated',
		'search_document_ids', {{payload}} -> 'search_document_ids',
		'embedding_job_ids', {{payload}} -> 'embedding_job_ids'
	))`

const submissionDiagnosticOperatorPayloadSQL = `
	jsonb_strip_nulls(jsonb_build_object(
		'failure_reason_code', {{payload}} -> 'failure_reason_code',
		'failure_stage', {{payload}} -> 'failure_stage',
		'failure_class', {{payload}} -> 'failure_class',
		'validation_stage', {{payload}} -> 'validation_stage',
		'validation_field_families', {{payload}} -> 'validation_field_families',
		'failure_measurement', CASE
			WHEN jsonb_typeof({{payload}} -> 'failure_measurement') = 'object' THEN
				jsonb_strip_nulls(jsonb_build_object(
					'unit', ({{payload}} -> 'failure_measurement') -> 'unit',
					'observed', ({{payload}} -> 'failure_measurement') -> 'observed',
					'observed_at_least', ({{payload}} -> 'failure_measurement') -> 'observed_at_least',
					'limit', ({{payload}} -> 'failure_measurement') -> 'limit'
				))
		END,
		'provider_status', {{payload}} -> 'provider_status',
		'assessor_turns', {{payload}} -> 'assessor_turns',
		'assessor_provider_attempted', {{payload}} -> 'assessor_provider_attempted'
	))`

func submissionDiagnosticSafePayload(alias string) string {
	return strings.ReplaceAll(submissionDiagnosticSafePayloadSQL, "{{payload}}", alias)
}

func submissionDiagnosticOperatorPayload(alias string) string {
	return strings.ReplaceAll(submissionDiagnosticOperatorPayloadSQL, "{{payload}}", alias)
}

func (r *LedgerRepositoryImpl) ListSubmissionDiagnostics(
	ctx context.Context,
	filter SubmissionDiagnosticFilter,
) (*SubmissionDiagnosticRecordPage, error) {
	filter = normalizeSubmissionDiagnosticFilter(filter)
	if err := validateSubmissionDiagnosticFilter(filter); err != nil {
		return nil, err
	}
	var page SubmissionDiagnosticRecordPage
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		teamID := nullableSubmissionDiagnosticTeamID(filter.TeamID)
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(*)
			FROM placement_runs AS run
			WHERE (?::uuid IS NULL OR run.team_id = ?::uuid)
			  AND (? = '' OR `+submissionDiagnosticProcessingStateSQL+` = ?)
		`, teamID, teamID, filter.ProcessingState, filter.ProcessingState).Scan(&page.Total).Error; err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT run.team_id::text, team.name, `+submissionDiagnosticSourceTypesSQL+`, run.owner_profile_id::text, run.ingest_id::text,
			       run.status, COALESCE(ingest.metadata #>> '{actor,correlation_id}', ''),
			       run.attempts, run.max_attempts, ingest.created_at,
			       CASE
			           WHEN run.status IN ('queued', 'guarded')
			            AND run.attempts > 0
			            AND run.available_at > now()
			           THEN run.available_at
			       END,
			       run.started_at, run.updated_at, run.completed_at,
			       run.semantic_hold_state, run.quarantine_expires_at, hold.expires_at,
			       (SELECT count(*) FROM evidence_fragments AS fragment
			        WHERE fragment.team_id = run.team_id AND fragment.ingest_id = run.ingest_id),
			       COALESCE(failure.status, ''), COALESCE(failure.result, '{}'::jsonb)
			FROM placement_runs AS run
			JOIN knowledge_ingests AS ingest
			  ON ingest.team_id = run.team_id AND ingest.ingest_id = run.ingest_id
			JOIN teams AS team ON team.id = run.team_id
			LEFT JOIN submission_holds AS hold
			  ON hold.team_id = run.team_id AND hold.placement_run_id = run.placement_run_id
			LEFT JOIN LATERAL (
				SELECT item.status, `+submissionDiagnosticOperatorPayload("item.result")+` AS result
				FROM placement_items AS item
				WHERE item.team_id = run.team_id
				  AND item.placement_run_id = run.placement_run_id
				ORDER BY
				  CASE WHEN COALESCE(item.result ->> 'failure_reason_code', '') <> ''
				         OR COALESCE(item.result ->> 'failure_stage', '') <> ''
				         OR COALESCE(item.result ->> 'failure_class', '') <> ''
				         OR COALESCE(item.result ->> 'validation_stage', '') <> ''
				         OR jsonb_typeof(item.result -> 'failure_measurement') = 'object'
				         OR COALESCE(item.result ->> 'provider_status', '') <> ''
				         OR COALESCE(item.result ->> 'assessor_turns', '') <> '' THEN 0 ELSE 1 END,
				  item.evidence_index ASC,
				  item.placement_item_id ASC
				LIMIT 1
			) AS failure ON true
			WHERE (?::uuid IS NULL OR run.team_id = ?::uuid)
			  AND (? = '' OR `+submissionDiagnosticProcessingStateSQL+` = ?)
			ORDER BY run.created_at DESC, run.placement_run_id DESC
			LIMIT ? OFFSET ?
		`, teamID, teamID, filter.ProcessingState, filter.ProcessingState, filter.Limit, filter.Offset).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		page.Records = make([]SubmissionDiagnosticRecord, 0)
		for rows.Next() {
			record, err := scanSubmissionDiagnosticRecord(rows)
			if err != nil {
				return err
			}
			page.Records = append(page.Records, record)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list submission diagnostics: %w", err)
	}
	return &page, nil
}

func (r *LedgerRepositoryImpl) GetSubmissionDiagnostic(
	ctx context.Context,
	teamID string,
	submissionID string,
) (*SubmissionDiagnosticRecord, error) {
	teamID, submissionID = strings.TrimSpace(teamID), strings.TrimSpace(submissionID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(submissionID); err != nil {
		return nil, fmt.Errorf("submission_id is required: %w", err)
	}
	var record *SubmissionDiagnosticRecord
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		loaded, err := loadSubmissionDiagnostic(ctx, tx, teamID, submissionID)
		if err != nil {
			return err
		}
		record = loaded
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrSubmissionDiagnosticNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("get submission diagnostic: %w", err)
	}
	return record, nil
}

func loadSubmissionDiagnostic(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	submissionID string,
) (*SubmissionDiagnosticRecord, error) {
	var record SubmissionDiagnosticRecord
	var semanticHoldState sql.NullString
	var submittedAt, nextAttemptAt, startedAt, updatedAt, completedAt sql.NullTime
	var quarantineExpiresAt, replacementWindowExpiresAt sql.NullTime
	var sourceTypesRaw []byte
	err := tx.WithContext(ctx).Raw(`
		SELECT run.team_id::text, team.name, `+submissionDiagnosticSourceTypesSQL+`, run.owner_profile_id::text, run.ingest_id::text,
		       run.placement_run_id::text, run.status,
		       COALESCE(ingest.metadata ->> 'contract_version', ''),
		       COALESCE(ingest.metadata #>> '{actor,correlation_id}', ''),
		       run.attempts, run.max_attempts, ingest.created_at,
		       CASE
		           WHEN run.status IN ('queued', 'guarded')
		            AND run.attempts > 0
		            AND run.available_at > now()
		           THEN run.available_at
		       END,
		       run.started_at, run.updated_at, run.completed_at,
		       run.semantic_hold_state, run.quarantine_expires_at, hold.expires_at
		FROM placement_runs AS run
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = run.team_id AND ingest.ingest_id = run.ingest_id
		JOIN teams AS team ON team.id = run.team_id
		LEFT JOIN submission_holds AS hold
		  ON hold.team_id = run.team_id AND hold.placement_run_id = run.placement_run_id
		WHERE run.team_id = ?::uuid AND run.ingest_id = ?::uuid
	`, teamID, submissionID).Row().Scan(
		&record.Placement.TeamID,
		&record.TeamName,
		&sourceTypesRaw,
		&record.Placement.OwnerProfileID,
		&record.Placement.IngestID,
		&record.Placement.PlacementRunID,
		&record.Placement.Status,
		&record.Placement.ContractVersion,
		&record.Placement.CorrelationID,
		&record.Placement.Attempts,
		&record.Placement.MaxAttempts,
		&submittedAt,
		&nextAttemptAt,
		&startedAt,
		&updatedAt,
		&completedAt,
		&semanticHoldState,
		&quarantineExpiresAt,
		&replacementWindowExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSubmissionDiagnosticNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(sourceTypesRaw, &record.SourceTypes); err != nil {
		return nil, fmt.Errorf("decode submission diagnostic source types: %w", err)
	}
	record.Placement.SubmittedAt = nullableStatusTime(submittedAt)
	record.Placement.NextAttemptAt = nullableStatusTime(nextAttemptAt)
	record.Placement.StartedAt = nullableStatusTime(startedAt)
	record.Placement.UpdatedAt = nullableStatusTime(updatedAt)
	record.Placement.CompletedAt = nullableStatusTime(completedAt)
	record.Placement.QuarantineExpiresAt = nullableStatusTime(quarantineExpiresAt)
	record.Placement.ReplacementWindowExpiresAt = nullableStatusTime(replacementWindowExpiresAt)
	if semanticHoldState.Valid {
		record.Placement.SemanticHoldState = strings.TrimSpace(semanticHoldState.String)
	}
	evidenceRows, err := tx.WithContext(ctx).Raw(`
		SELECT fragment_id::text, evidence_index
		FROM evidence_fragments
		WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		ORDER BY evidence_index ASC
	`, teamID, submissionID).Rows()
	if err != nil {
		return nil, err
	}
	for evidenceRows.Next() {
		var evidence EvidenceFragment
		if err := evidenceRows.Scan(&evidence.FragmentID, &evidence.EvidenceIndex); err != nil {
			_ = evidenceRows.Close()
			return nil, err
		}
		record.Placement.Evidence = append(record.Placement.Evidence, evidence)
	}
	if err := evidenceRows.Err(); err != nil {
		_ = evidenceRows.Close()
		return nil, err
	}
	if err := evidenceRows.Close(); err != nil {
		return nil, err
	}
	record.EvidenceCount = len(record.Placement.Evidence)
	itemRows, err := tx.WithContext(ctx).Raw(`
		SELECT placement_item_id::text, fragment_id::text, evidence_index, status, category,
		       `+submissionDiagnosticSafePayload("result")+`
		FROM placement_items
		WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		ORDER BY evidence_index ASC
	`, teamID, submissionID).Rows()
	if err != nil {
		return nil, err
	}
	for itemRows.Next() {
		var item PlacementItem
		var resultRaw []byte
		if err := itemRows.Scan(&item.PlacementItemID, &item.FragmentID, &item.EvidenceIndex, &item.Status, &item.Category, &resultRaw); err != nil {
			_ = itemRows.Close()
			return nil, err
		}
		if err := json.Unmarshal(resultRaw, &item.Result); err != nil {
			_ = itemRows.Close()
			return nil, err
		}
		record.Placement.Items = append(record.Placement.Items, item)
		if len(record.OperatorDiagnostic) == 0 && hasSubmissionDiagnosticPayload(item.Result) {
			record.OperatorDiagnostic = submissionDiagnosticOperatorPayloadMap(item.Result)
		}
	}
	if err := itemRows.Err(); err != nil {
		_ = itemRows.Close()
		return nil, err
	}
	if err := itemRows.Close(); err != nil {
		return nil, err
	}
	if err := hydratePlacementItemSearchStates(ctx, tx, teamID, record.Placement.OwnerProfileID, record.Placement.Items); err != nil {
		return nil, err
	}
	if err := hydrateEvidenceLifecycleLineage(ctx, tx, teamID, record.Placement.Evidence); err != nil {
		return nil, err
	}
	if err := loadSubmissionDiagnosticOutcomes(ctx, tx, teamID, record.Placement.PlacementRunID, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func loadSubmissionDiagnosticOutcomes(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	placementRunID string,
	record *SubmissionDiagnosticRecord,
) error {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH projected_outcomes AS (
			SELECT outcome_id, placement_item_id, outcome_kind, status, created_at,
			       `+submissionDiagnosticOperatorPayload("payload")+` AS operator_payload
			FROM placement_outcomes
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		), diagnostic_outcomes AS (
			SELECT outcome_id, placement_item_id, outcome_kind, status, created_at, operator_payload
			FROM projected_outcomes
			WHERE operator_payload <> '{}'::jsonb
		), deduplicated_outcomes AS (
			SELECT DISTINCT ON (created_at, outcome_kind, status, operator_payload)
			       outcome_id, placement_item_id, outcome_kind, status, created_at, operator_payload
			FROM diagnostic_outcomes
			ORDER BY created_at DESC, outcome_kind ASC, status ASC, operator_payload ASC, outcome_id DESC
		), bounded_outcomes AS (
			SELECT outcome_id, placement_item_id, outcome_kind, status, created_at, operator_payload
			FROM deduplicated_outcomes
			ORDER BY created_at DESC, outcome_id DESC
			LIMIT `+fmt.Sprint(submissionDiagnosticOutcomeLimit)+`
		)
		SELECT outcome_id::text, COALESCE(placement_item_id::text, ''), outcome_kind, status,
		       created_at, operator_payload
		FROM bounded_outcomes
		ORDER BY created_at ASC, outcome_id ASC
	`, teamID, placementRunID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var diagnostic SubmissionDiagnosticOperatorDiagnostic
		var payloadRaw []byte
		if err := rows.Scan(
			&diagnostic.ID,
			&diagnostic.PlacementItemID,
			&diagnostic.OutcomeKind,
			&diagnostic.Status,
			&diagnostic.CreatedAt,
			&payloadRaw,
		); err != nil {
			return err
		}
		diagnostic.Payload = map[string]any{}
		if err := json.Unmarshal(payloadRaw, &diagnostic.Payload); err != nil {
			return err
		}
		if !hasSubmissionDiagnosticPayload(diagnostic.Payload) {
			continue
		}
		record.OperatorDiagnostics = append(record.OperatorDiagnostics, diagnostic)
		if len(record.OperatorDiagnostic) == 0 {
			record.OperatorDiagnostic = submissionDiagnosticOperatorPayloadMap(diagnostic.Payload)
		}
	}
	return rows.Err()
}

func hasSubmissionDiagnosticPayload(payload map[string]any) bool {
	for _, key := range []string{"failure_reason_code", "failure_stage", "failure_class", "validation_stage", "failure_measurement", "provider_status", "assessor_turns"} {
		if value, ok := payload[key]; ok && value != nil {
			return true
		}
	}
	return false
}

func submissionDiagnosticOperatorPayloadMap(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	operatorPayload := make(map[string]any)
	for _, key := range []string{
		"failure_reason_code",
		"failure_stage",
		"failure_class",
		"validation_stage",
		"validation_field_families",
		"failure_measurement",
		"provider_status",
		"assessor_turns",
		"assessor_provider_attempted",
	} {
		if value, ok := payload[key]; ok && value != nil {
			operatorPayload[key] = value
		}
	}
	if len(operatorPayload) == 0 {
		return nil
	}
	return operatorPayload
}

func scanSubmissionDiagnosticRecord(rows *sql.Rows) (SubmissionDiagnosticRecord, error) {
	var record SubmissionDiagnosticRecord
	var sourceTypesRaw []byte
	var submittedAt, nextAttemptAt, startedAt, updatedAt, completedAt sql.NullTime
	var semanticHoldState sql.NullString
	var quarantineExpiresAt, replacementWindowExpiresAt sql.NullTime
	var itemStatus string
	var resultRaw []byte
	if err := rows.Scan(
		&record.Placement.TeamID,
		&record.TeamName,
		&sourceTypesRaw,
		&record.Placement.OwnerProfileID,
		&record.Placement.IngestID,
		&record.Placement.Status,
		&record.Placement.CorrelationID,
		&record.Placement.Attempts,
		&record.Placement.MaxAttempts,
		&submittedAt,
		&nextAttemptAt,
		&startedAt,
		&updatedAt,
		&completedAt,
		&semanticHoldState,
		&quarantineExpiresAt,
		&replacementWindowExpiresAt,
		&record.EvidenceCount,
		&itemStatus,
		&resultRaw,
	); err != nil {
		return SubmissionDiagnosticRecord{}, err
	}
	if err := json.Unmarshal(sourceTypesRaw, &record.SourceTypes); err != nil {
		return SubmissionDiagnosticRecord{}, fmt.Errorf("decode submission diagnostic source types: %w", err)
	}
	record.Placement.SubmittedAt = nullableStatusTime(submittedAt)
	record.Placement.NextAttemptAt = nullableStatusTime(nextAttemptAt)
	record.Placement.StartedAt = nullableStatusTime(startedAt)
	record.Placement.UpdatedAt = nullableStatusTime(updatedAt)
	record.Placement.CompletedAt = nullableStatusTime(completedAt)
	record.Placement.QuarantineExpiresAt = nullableStatusTime(quarantineExpiresAt)
	record.Placement.ReplacementWindowExpiresAt = nullableStatusTime(replacementWindowExpiresAt)
	if semanticHoldState.Valid {
		record.Placement.SemanticHoldState = strings.TrimSpace(semanticHoldState.String)
	}
	if itemStatus != "" {
		item := PlacementItem{Status: itemStatus, Result: map[string]any{}}
		if err := json.Unmarshal(resultRaw, &item.Result); err != nil {
			return SubmissionDiagnosticRecord{}, err
		}
		record.Placement.Items = []PlacementItem{item}
		record.OperatorDiagnostic = submissionDiagnosticOperatorPayloadMap(item.Result)
	}
	return record, nil
}

func normalizeSubmissionDiagnosticFilter(filter SubmissionDiagnosticFilter) SubmissionDiagnosticFilter {
	filter.TeamID = strings.TrimSpace(filter.TeamID)
	filter.ProcessingState = strings.TrimSpace(filter.ProcessingState)
	if filter.Limit <= 0 {
		filter.Limit = 20
	}
	if filter.Limit > 100 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return filter
}

func validateSubmissionDiagnosticFilter(filter SubmissionDiagnosticFilter) error {
	if filter.TeamID != "" {
		if _, err := uuid.Parse(filter.TeamID); err != nil {
			return fmt.Errorf("team_id must be a UUID: %w", err)
		}
	}
	switch filter.ProcessingState {
	case "", "queued", "processing", "awaiting_review", "completed", "rejected", "quarantined", "failed":
	default:
		return fmt.Errorf("unsupported processing_state %q", filter.ProcessingState)
	}
	return nil
}

func nullableSubmissionDiagnosticTeamID(teamID string) any {
	if teamID == "" {
		return nil
	}
	return teamID
}
