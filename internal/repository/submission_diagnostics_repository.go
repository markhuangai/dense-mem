package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	TeamName      string
	EvidenceCount int
	Placement     CreateIngestResult
}

type SubmissionDiagnosticRecordPage struct {
	Records []SubmissionDiagnosticRecord
	Total   int64
}

var _ SubmissionDiagnosticsRepository = (*LedgerRepositoryImpl)(nil)

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
			SELECT run.team_id::text, team.name, run.owner_profile_id::text, run.ingest_id::text,
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
				SELECT item.status,
				       jsonb_strip_nulls(jsonb_build_object(
				           'failure_stage', item.result -> 'failure_stage',
				           'failure_class', item.result -> 'failure_class'
				       )) AS result
				FROM placement_items AS item
				WHERE item.team_id = run.team_id
				  AND item.placement_run_id = run.placement_run_id
				ORDER BY
				  CASE WHEN COALESCE(item.result ->> 'failure_stage', '') <> ''
				         OR COALESCE(item.result ->> 'failure_class', '') <> '' THEN 0 ELSE 1 END,
				  item.evidence_index ASC
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
	err := tx.WithContext(ctx).Raw(`
		SELECT run.team_id::text, team.name, run.owner_profile_id::text, run.ingest_id::text,
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
		       jsonb_strip_nulls(jsonb_build_object(
		           'failure_stage', result -> 'failure_stage',
		           'failure_class', result -> 'failure_class',
		           'hold_issues', result -> 'hold_issues',
		           'hold_issues_truncated', result -> 'hold_issues_truncated',
		           'search_document_ids', result -> 'search_document_ids',
		           'embedding_job_ids', result -> 'embedding_job_ids'
		       ))
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
	return &record, nil
}

func scanSubmissionDiagnosticRecord(rows *sql.Rows) (SubmissionDiagnosticRecord, error) {
	var record SubmissionDiagnosticRecord
	var submittedAt, nextAttemptAt, startedAt, updatedAt, completedAt sql.NullTime
	var semanticHoldState sql.NullString
	var quarantineExpiresAt, replacementWindowExpiresAt sql.NullTime
	var itemStatus string
	var resultRaw []byte
	if err := rows.Scan(
		&record.Placement.TeamID,
		&record.TeamName,
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
