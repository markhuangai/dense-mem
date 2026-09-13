package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

// ErrRememberReplay identifies a terminal attempt that owns an idempotency
// key. Callers reload its public result instead of reconstructing it.
var (
	ErrRememberAttemptDiagnosticNotFound = errors.New("remember attempt diagnostic not found")
)

type RememberAttemptRecordInput = knowledgecontract.RememberAttemptRecordInput
type RememberAttempt = knowledgecontract.RememberAttempt
type RememberAttemptLookupInput = knowledgecontract.RememberAttemptLookupInput
type RememberAttemptLookup = knowledgecontract.RememberAttemptLookup
type RememberFailureRecordInput = knowledgecontract.RememberFailureRecordInput
type RememberAttemptDiagnosticInput = knowledgecontract.RememberAttemptDiagnosticInput

type RememberAttemptDiagnosticFilter = knowledgecontract.RememberAttemptDiagnosticFilter
type RememberAttemptDiagnosticRecord = knowledgecontract.RememberAttemptDiagnosticRecord
type RememberAttemptDiagnosticEvent = knowledgecontract.RememberAttemptDiagnosticEvent
type RememberAttemptDiagnosticRecordItem = knowledgecontract.RememberAttemptDiagnosticRecordItem
type RememberAttemptDiagnosticRecordPage = knowledgecontract.RememberAttemptDiagnosticRecordPage

// RememberAttemptDiagnosticsRepository is the narrow application port for
// control-only attempt diagnostics and retention.
type RememberAttemptDiagnosticsRepository interface {
	ListRememberAttemptDiagnostics(context.Context, RememberAttemptDiagnosticFilter) (*RememberAttemptDiagnosticRecordPage, error)
	GetRememberAttemptDiagnostic(context.Context, string, string) (*RememberAttemptDiagnosticRecord, error)
}

var _ RememberAttemptDiagnosticsRepository = (*Store)(nil)

const (
	maxRememberDiagnosticBodyBytes    = 16 * 1024 * 1024
	maxRememberDiagnosticAttemptBytes = 64 * 1024 * 1024
	rememberDiagnosticPurgeBatchSize  = 100
	rememberDiagnosticRetention       = 7 * 24 * time.Hour
)

func validRememberDiagnosticCaptureState(value string) bool {
	switch strings.TrimSpace(value) {
	case "captured", "truncated", "not_captured", "hash_only", "provider_not_called", "no_response", "interrupted", "not_delivered":
		return true
	default:
		return false
	}
}

func rememberDiagnosticCaptureState(outcome string, requestBody, responseBody []byte) string {
	return knowledgecontract.DiagnosticCaptureState("", outcome, len(requestBody), len(responseBody))
}

func lockRememberIdempotencyKeyInTx(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, key string) error {
	digest := sha256.Sum256([]byte(teamID + "\x00" + ownerProfileID + "\x00" + key))
	return tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(?, ?)`,
		int32(binary.BigEndian.Uint32(digest[:4])), int32(binary.BigEndian.Uint32(digest[4:8]))).Error
}

func validateRememberFailureRetryInTx(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, key, requestHash string) error {
	var hasHashMismatch bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM remember_attempts
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
			  AND idempotency_key = ? AND outcome = 'failed'
			  AND request_hash <> ?
		)
	`, teamID, ownerProfileID, key, requestHash).Row().Scan(&hasHashMismatch); err != nil {
		return err
	}
	if hasHashMismatch {
		return fmt.Errorf("%w: idempotency key reused with a different request hash", ErrIdempotencyConflict)
	}
	return nil
}

func (r *Store) LoadRememberAttempt(ctx context.Context, input RememberAttemptLookupInput) (*RememberAttempt, error) {
	input.TeamID, input.OwnerProfileID, input.IdempotencyKey = strings.TrimSpace(input.TeamID), strings.TrimSpace(input.OwnerProfileID), strings.TrimSpace(input.IdempotencyKey)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("remember attempt: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return nil, fmt.Errorf("remember attempt: owner_profile_id is required: %w", err)
	}
	if input.IdempotencyKey == "" {
		return nil, errors.New("remember attempt: idempotency_key is required")
	}
	var result *RememberAttempt
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		var stored RememberAttempt
		var raw []byte
		err := tx.WithContext(ctx).Raw(`
			SELECT attempt_id::text, request_hash, contract_version, outcome,
			       COALESCE(retryable, outcome = 'failed'), public_result
			FROM remember_attempts
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?
			ORDER BY CASE WHEN outcome IN ('completed', 'rejected', 'quarantined', 'replayed') THEN 0 ELSE 1 END,
			         created_at DESC, attempt_id DESC LIMIT 1
		`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Row().Scan(&stored.AttemptID, &stored.RequestHash, &stored.ContractVersion, &stored.Outcome, &stored.Retryable, &raw)
		if err != nil {
			return err
		}
		stored.PublicResult = map[string]any{}
		if len(raw) != 0 {
			if err := json.Unmarshal(raw, &stored.PublicResult); err != nil {
				return fmt.Errorf("remember attempt: decode public result: %w", err)
			}
		}
		result = &stored
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRememberAttemptNotFound
	}
	return result, err
}

func loadTerminalRememberAttemptInTx(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, key, requestHash string) (*RememberAttempt, error) {
	var result RememberAttempt
	var raw []byte
	err := tx.WithContext(ctx).Raw(`
		SELECT attempt_id::text, request_hash, contract_version, outcome,
		       COALESCE(retryable, outcome = 'failed'), public_result
		FROM remember_attempts
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?
		  AND outcome IN ('completed', 'rejected', 'quarantined', 'replayed')
		ORDER BY created_at DESC, attempt_id DESC LIMIT 1
	`, teamID, ownerProfileID, key).Row().Scan(&result.AttemptID, &result.RequestHash, &result.ContractVersion, &result.Outcome, &result.Retryable, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(result.RequestHash) != strings.TrimSpace(requestHash) {
		return nil, fmt.Errorf("%w: idempotency key reused with a different request hash", ErrIdempotencyConflict)
	}
	if version := strings.TrimSpace(result.ContractVersion); version != "" && !domain.ContractVersionCompatible(version) {
		return nil, fmt.Errorf("%w: historical Remember contract is not replayable", ErrIdempotencyConflict)
	}
	if result.Outcome == "rejected" || result.Outcome == "quarantined" {
		return nil, fmt.Errorf("%w: historical Remember outcome is not replayable", ErrIdempotencyConflict)
	}
	result.PublicResult = map[string]any{}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &result.PublicResult); err != nil {
			return nil, err
		}
	}
	return &result, nil
}

func (r *Store) RecordRememberAttempt(ctx context.Context, input RememberAttemptRecordInput) error {
	input = normalizeRememberAttemptRecord(input)
	if err := validateRememberAttemptRecord(input); err != nil {
		return err
	}
	return r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error { return insertRememberAttemptInTx(ctx, tx, input) })
}

// RecordRememberFailure keeps a bounded operational record without creating
// canonical ingest, evidence, placement, semantic, or search state.
func (r *Store) RecordRememberFailure(ctx context.Context, input RememberFailureRecordInput) error {
	input.Attempt = normalizeRememberAttemptRecord(input.Attempt)
	input.Attempt.Outcome = "failed"
	if !input.Attempt.RetryabilitySet && !input.Attempt.Retryable {
		input.Attempt.Retryable = true
	}
	if err := validateRememberAttemptRecord(input.Attempt); err != nil {
		return err
	}
	if input.Attempt.FailedPhase == "" {
		input.Attempt.FailedPhase = "execution"
	}
	if input.Attempt.ErrorCode == "" {
		input.Attempt.ErrorCode = "internal_failure"
	}
	for index := range input.Diagnostics {
		diagnostic := &input.Diagnostics[index]
		diagnostic.Kind = strings.TrimSpace(diagnostic.Kind)
		diagnostic.Component = strings.TrimSpace(diagnostic.Component)
		diagnostic.Model = strings.TrimSpace(diagnostic.Model)
		diagnostic.RequestContentType = strings.TrimSpace(diagnostic.RequestContentType)
		diagnostic.ResponseContentType = strings.TrimSpace(diagnostic.ResponseContentType)
		diagnostic.Outcome = strings.TrimSpace(diagnostic.Outcome)
		if diagnostic.SequenceNo < 1 {
			return fmt.Errorf("remember failure: diagnostic[%d] sequence is required", index)
		}
		switch diagnostic.Kind {
		case "original_request", "provider_exchange", "caller_response":
		default:
			return fmt.Errorf("remember failure: diagnostic[%d] kind is unsupported", index)
		}
		if len(diagnostic.RequestBody) > maxRememberDiagnosticBodyBytes || len(diagnostic.ResponseBody) > maxRememberDiagnosticBodyBytes {
			return fmt.Errorf("remember failure: diagnostic[%d] body exceeds %d bytes", index, maxRememberDiagnosticBodyBytes)
		}
		if diagnostic.Outcome == "" {
			diagnostic.Outcome = "captured"
		}
		if diagnostic.CaptureState == "" {
			diagnostic.CaptureState = rememberDiagnosticCaptureState(diagnostic.Outcome, diagnostic.RequestBody, diagnostic.ResponseBody)
		}
		if !validRememberDiagnosticCaptureState(diagnostic.CaptureState) {
			return fmt.Errorf("remember failure: diagnostic[%d] capture state %q is unsupported", index, diagnostic.CaptureState)
		}
		if len(diagnostic.RequestBody) > maxRememberDiagnosticBodyBytes {
			diagnostic.RequestBody = diagnostic.RequestBody[:maxRememberDiagnosticBodyBytes]
			diagnostic.CaptureState = "truncated"
		}
		if len(diagnostic.ResponseBody) > maxRememberDiagnosticBodyBytes {
			diagnostic.ResponseBody = diagnostic.ResponseBody[:maxRememberDiagnosticBodyBytes]
			diagnostic.CaptureState = "truncated"
		}
	}
	remainingDiagnosticBytes := maxRememberDiagnosticAttemptBytes
	for index := range input.Diagnostics {
		diagnostic := &input.Diagnostics[index]
		if remainingDiagnosticBytes <= 0 {
			diagnostic.RequestBody = nil
			diagnostic.ResponseBody = nil
			diagnostic.CaptureState = "truncated"
			continue
		}
		if len(diagnostic.RequestBody) > remainingDiagnosticBytes {
			diagnostic.RequestBody = diagnostic.RequestBody[:remainingDiagnosticBytes]
			diagnostic.ResponseBody = nil
			diagnostic.CaptureState = "truncated"
			remainingDiagnosticBytes = 0
			continue
		}
		remainingDiagnosticBytes -= len(diagnostic.RequestBody)
		if len(diagnostic.ResponseBody) > remainingDiagnosticBytes {
			diagnostic.ResponseBody = diagnostic.ResponseBody[:remainingDiagnosticBytes]
			diagnostic.CaptureState = "truncated"
			remainingDiagnosticBytes = 0
			continue
		}
		remainingDiagnosticBytes -= len(diagnostic.ResponseBody)
	}
	if err := r.withAtomicRememberTx(ctx, input.Attempt.TeamID, input.Attempt.OwnerProfileID, func(txCtx context.Context) error {
		tx := transactionFromContext(txCtx)
		var databaseNow time.Time
		for index := range input.Diagnostics {
			diagnostic := &input.Diagnostics[index]
			if diagnostic.CapturedAt.IsZero() {
				if databaseNow.IsZero() {
					if err := tx.WithContext(txCtx).Raw(`SELECT clock_timestamp()`).Row().Scan(&databaseNow); err != nil {
						return fmt.Errorf("remember failure: database clock: %w", err)
					}
					databaseNow = databaseNow.UTC()
				}
				diagnostic.CapturedAt = databaseNow
			}
			if diagnostic.ExpiresAt.IsZero() {
				diagnostic.ExpiresAt = diagnostic.CapturedAt.Add(rememberDiagnosticRetention)
			}
			if diagnostic.ExpiresAt.Before(diagnostic.CapturedAt) || diagnostic.ExpiresAt.After(diagnostic.CapturedAt.Add(rememberDiagnosticRetention)) {
				return fmt.Errorf("remember failure: diagnostic[%d] expiry is outside retention", index)
			}
		}
		if err := insertRememberAttemptInTx(txCtx, tx, input.Attempt); err != nil {
			return err
		}
		for _, diagnostic := range input.Diagnostics {
			if diagnostic.DiagnosticID == "" {
				diagnostic.DiagnosticID = uuid.NewString()
			}
			if _, err := uuid.Parse(diagnostic.DiagnosticID); err != nil {
				return fmt.Errorf("remember failure: diagnostic ID is invalid: %w", err)
			}
			requestBody := diagnostic.RequestBody
			if requestBody == nil {
				requestBody = []byte{}
			}
			responseBody := diagnostic.ResponseBody
			if responseBody == nil {
				responseBody = []byte{}
			}
			if err := tx.WithContext(txCtx).Exec(`
				INSERT INTO remember_attempt_diagnostics (
				 team_id, diagnostic_id, attempt_id, owner_profile_id, sequence_no, kind, component, model,
				 request_bytes, response_bytes, request_content_type, response_content_type,
				 status_code, outcome, capture_state, captured_at, expires_at)
				VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				`, input.Attempt.TeamID, diagnostic.DiagnosticID, input.Attempt.AttemptID, input.Attempt.OwnerProfileID,
				diagnostic.SequenceNo, diagnostic.Kind, diagnostic.Component, diagnostic.Model,
				requestBody, responseBody, diagnostic.RequestContentType, diagnostic.ResponseContentType,
				diagnostic.StatusCode, diagnostic.Outcome, diagnostic.CaptureState, diagnostic.CapturedAt.UTC(), diagnostic.ExpiresAt.UTC()).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if strings.TrimSpace(input.Attempt.SpaceID) != "" {
		if err := r.synchronizeRememberAttemptDiagnosticHold(ctx, input.Attempt.SpaceID); err != nil {
			return fmt.Errorf("%w: %v", ErrRememberFailureRetentionDegraded, err)
		}
	}
	return nil
}

func normalizeRememberAttemptRecord(input RememberAttemptRecordInput) RememberAttemptRecordInput {
	input.TeamID, input.OwnerProfileID, input.AttemptID = strings.TrimSpace(input.TeamID), strings.TrimSpace(input.OwnerProfileID), strings.TrimSpace(input.AttemptID)
	input.SpaceID, input.IdempotencyKey, input.RequestHash = strings.TrimSpace(input.SpaceID), strings.TrimSpace(input.IdempotencyKey), strings.TrimSpace(input.RequestHash)
	input.ContractVersion, input.SubmissionKind = strings.TrimSpace(input.ContractVersion), strings.TrimSpace(input.SubmissionKind)
	input.Outcome, input.FailedPhase, input.ErrorCode, input.CorrelationID = strings.TrimSpace(input.Outcome), strings.TrimSpace(input.FailedPhase), strings.TrimSpace(input.ErrorCode), strings.TrimSpace(input.CorrelationID)
	if input.Outcome == "failed" && !input.RetryabilitySet && !input.Retryable {
		input.Retryable = true
	}
	if input.PublicResult == nil {
		input.PublicResult = map[string]any{}
	}
	return input
}

func validateRememberAttemptRecord(input RememberAttemptRecordInput) error {
	for label, value := range map[string]string{"team_id": input.TeamID, "owner_profile_id": input.OwnerProfileID, "attempt_id": input.AttemptID} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("remember attempt: %s is required: %w", label, err)
		}
	}
	if input.IdempotencyKey == "" || input.RequestHash == "" || input.ContractVersion == "" || input.SubmissionKind == "" {
		return errors.New("remember attempt: identity fields are required")
	}
	if input.SpaceID != "" {
		if _, err := uuid.Parse(input.SpaceID); err != nil {
			return fmt.Errorf("remember attempt: space_id is invalid: %w", err)
		}
		if input.SpaceGeneration < 1 {
			return errors.New("remember attempt: space_generation is required")
		}
	}
	if input.SpaceID == "" && input.SpaceGeneration != 0 {
		return errors.New("remember attempt: space_id is required")
	}
	if input.Outcome != "completed" && input.Outcome != "rejected" && input.Outcome != "quarantined" && input.Outcome != "failed" && input.Outcome != "replayed" {
		return fmt.Errorf("remember attempt: unsupported outcome %q", input.Outcome)
	}
	if input.Retryable && input.Outcome != "failed" {
		return errors.New("remember attempt: retryable is only valid for failed outcomes")
	}
	if input.EvidenceCount < 0 || input.RelationshipCount < 0 || input.DocumentCount < 0 || input.AssessorTurns < 0 || input.Duration < 0 {
		return errors.New("remember attempt: counters and duration cannot be negative")
	}
	return nil
}

func insertRememberAttemptInTx(ctx context.Context, tx *gorm.DB, input RememberAttemptRecordInput) error {
	if err := lockRememberIdempotencyKeyInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey); err != nil {
		return err
	}
	if err := validateRememberFailureRetryInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, input.RequestHash); err != nil {
		return err
	}
	var previousFailedRetryable bool
	err := tx.WithContext(ctx).Raw(`
		SELECT COALESCE(retryable, true)
		FROM remember_attempts
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
		  AND idempotency_key = ? AND outcome = 'failed'
		ORDER BY created_at DESC, attempt_id DESC LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Row().Scan(&previousFailedRetryable)
	if err == nil && !previousFailedRetryable {
		return ErrRememberReplay
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existing, err := loadTerminalRememberAttemptInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, input.RequestHash); err != nil {
		return err
	} else if existing != nil {
		return ErrRememberReplay
	}
	encoded, err := json.Marshal(input.PublicResult)
	if err != nil {
		return fmt.Errorf("remember attempt: public result: %w", err)
	}
	result := tx.WithContext(ctx).Exec(`
		INSERT INTO remember_attempts (team_id, attempt_id, owner_profile_id, space_id, space_generation,
		 idempotency_key, request_hash, contract_version, submission_kind, outcome, failed_phase, error_code, retryable,
		 correlation_id, public_result, evidence_count, relationship_count, document_count, assessor_turns, duration_ms, completed_at)
		VALUES (?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?, 0), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, now())
	`, input.TeamID, input.AttemptID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration, input.IdempotencyKey, input.RequestHash, input.ContractVersion, input.SubmissionKind, input.Outcome, input.FailedPhase, input.ErrorCode, input.Retryable, input.CorrelationID, string(encoded), input.EvidenceCount, input.RelationshipCount, input.DocumentCount, input.AssessorTurns, input.Duration.Milliseconds())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRememberReplay
	}
	metadata, err := json.Marshal(map[string]any{"contract_version": input.ContractVersion, "assessor_turns": input.AssessorTurns, "document_count": input.DocumentCount, "error_code": input.ErrorCode, "retryable": input.Retryable})
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO remember_attempt_events (team_id, event_id, attempt_id, owner_profile_id, sequence_no, phase, event_kind, outcome, metadata)
		VALUES (?::uuid, gen_random_uuid(), ?::uuid, ?::uuid, 1, ?, ?, ?, ?::jsonb)
	`, input.TeamID, input.AttemptID, input.OwnerProfileID, rememberAttemptPhase(input), rememberAttemptEventKind(input), input.Outcome, string(metadata)).Error
}

func rememberAttemptPhase(input RememberAttemptRecordInput) string {
	if input.FailedPhase != "" {
		return input.FailedPhase
	}
	return "commit"
}
func rememberAttemptEventKind(input RememberAttemptRecordInput) string {
	if input.Outcome == "failed" {
		return rememberAttemptPhase(input) + "_failed"
	}
	if input.FailedPhase != "" {
		return rememberAttemptPhase(input) + "_" + input.Outcome
	}
	return "commit_completed"
}

func (r *Store) ListRememberAttemptDiagnostics(
	ctx context.Context,
	filter RememberAttemptDiagnosticFilter,
) (*RememberAttemptDiagnosticRecordPage, error) {
	page := &RememberAttemptDiagnosticRecordPage{Records: []RememberAttemptDiagnosticRecord{}}
	err := r.withSystemReadOnlyRepeatableTx(ctx, func(tx *gorm.DB) error {
		teamID := filter.TeamID
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(*)
			FROM remember_attempts AS attempt
			WHERE (NULLIF(?, '')::uuid IS NULL OR attempt.team_id = NULLIF(?, '')::uuid)
			  AND (? = '' OR attempt.outcome = ?)
		`, teamID, teamID, filter.Outcome, filter.Outcome).Scan(&page.Total).Error; err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT attempt.team_id::text, team.name, attempt.owner_profile_id::text,
			       attempt.attempt_id::text, COALESCE(attempt.space_id::text, ''),
			       COALESCE(attempt.space_generation, 0), attempt.contract_version,
			       attempt.submission_kind, attempt.outcome, attempt.failed_phase,
			       attempt.error_code, COALESCE(attempt.retryable, attempt.outcome = 'failed'), attempt.correlation_id,
			       COALESCE(attempt.canonical_attempt_id::text, ''),
			       attempt.evidence_count, attempt.relationship_count,
			       attempt.document_count, attempt.assessor_turns, attempt.duration_ms,
			       attempt.created_at, attempt.completed_at
			FROM remember_attempts AS attempt
			JOIN teams AS team ON team.id = attempt.team_id
			WHERE (NULLIF(?, '')::uuid IS NULL OR attempt.team_id = NULLIF(?, '')::uuid)
			  AND (? = '' OR attempt.outcome = ?)
			ORDER BY attempt.created_at DESC, attempt.attempt_id DESC
			LIMIT ? OFFSET ?
		`, teamID, teamID, filter.Outcome, filter.Outcome, filter.Limit, filter.Offset).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			record, err := scanRememberAttemptDiagnosticSummary(rows)
			if err != nil {
				return err
			}
			page.Records = append(page.Records, record)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list remember attempt diagnostics: %w", err)
	}
	return page, nil
}

func (r *Store) GetRememberAttemptDiagnostic(ctx context.Context, teamID, attemptID string) (*RememberAttemptDiagnosticRecord, error) {
	teamID, attemptID = strings.TrimSpace(teamID), strings.TrimSpace(attemptID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(attemptID); err != nil {
		return nil, fmt.Errorf("attempt_id is required: %w", err)
	}
	var record *RememberAttemptDiagnosticRecord
	err := r.withSystemReadOnlyRepeatableTx(ctx, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT attempt.team_id::text, team.name, attempt.owner_profile_id::text,
			       attempt.attempt_id::text, COALESCE(attempt.space_id::text, ''),
			       COALESCE(attempt.space_generation, 0), attempt.contract_version,
			       attempt.submission_kind, attempt.outcome, attempt.failed_phase,
			       attempt.error_code, COALESCE(attempt.retryable, attempt.outcome = 'failed'), attempt.correlation_id,
			       COALESCE(attempt.canonical_attempt_id::text, ''),
			       attempt.evidence_count, attempt.relationship_count,
			       attempt.document_count, attempt.assessor_turns, attempt.duration_ms,
			       attempt.created_at, attempt.completed_at, attempt.public_result
			FROM remember_attempts AS attempt
			JOIN teams AS team ON team.id = attempt.team_id
			WHERE attempt.team_id = ?::uuid AND attempt.attempt_id = ?::uuid
		`, teamID, attemptID).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			rowErr := rows.Err()
			_ = rows.Close()
			if rowErr != nil {
				return rowErr
			}
			return ErrRememberAttemptDiagnosticNotFound
		}
		value, err := scanRememberAttemptDiagnosticDetail(rows)
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		value.Events, err = loadRememberAttemptDiagnosticEvents(ctx, tx, teamID, attemptID)
		if err != nil {
			return err
		}
		value.Diagnostics, err = loadRememberAttemptDiagnostics(ctx, tx, teamID, attemptID)
		if err != nil {
			return err
		}
		record = &value
		return nil
	})
	if errors.Is(err, ErrRememberAttemptDiagnosticNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("get remember attempt diagnostic: %w", err)
	}
	return record, nil
}

func loadRememberAttemptDiagnostics(ctx context.Context, tx *gorm.DB, teamID, attemptID string) ([]RememberAttemptDiagnosticRecordItem, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT diagnostic_id::text, sequence_no, kind, component, model,
		       CASE WHEN expires_at > clock_timestamp() OR (retained_by_legal_hold AND EXISTS (
				SELECT 1 FROM private_memory_legal_holds AS active_hold
				JOIN remember_attempts AS held_attempt ON held_attempt.space_id = active_hold.space_id
				WHERE held_attempt.team_id = remember_attempt_diagnostics.team_id
				  AND held_attempt.attempt_id = remember_attempt_diagnostics.attempt_id
				  AND held_attempt.owner_profile_id = remember_attempt_diagnostics.owner_profile_id
				  AND active_hold.released_at IS NULL
			)) THEN request_bytes ELSE ''::bytea END,
		       CASE WHEN expires_at > clock_timestamp() OR (retained_by_legal_hold AND EXISTS (
				SELECT 1 FROM private_memory_legal_holds AS active_hold
				JOIN remember_attempts AS held_attempt ON held_attempt.space_id = active_hold.space_id
				WHERE held_attempt.team_id = remember_attempt_diagnostics.team_id
				  AND held_attempt.attempt_id = remember_attempt_diagnostics.attempt_id
				  AND held_attempt.owner_profile_id = remember_attempt_diagnostics.owner_profile_id
				  AND active_hold.released_at IS NULL
			)) THEN response_bytes ELSE ''::bytea END,
		       CASE WHEN expires_at > clock_timestamp() OR (retained_by_legal_hold AND EXISTS (
				SELECT 1 FROM private_memory_legal_holds AS active_hold
				JOIN remember_attempts AS held_attempt ON held_attempt.space_id = active_hold.space_id
				WHERE held_attempt.team_id = remember_attempt_diagnostics.team_id
				  AND held_attempt.attempt_id = remember_attempt_diagnostics.attempt_id
				  AND held_attempt.owner_profile_id = remember_attempt_diagnostics.owner_profile_id
				  AND active_hold.released_at IS NULL
			)) THEN request_content_type ELSE '' END,
		       CASE WHEN expires_at > clock_timestamp() OR (retained_by_legal_hold AND EXISTS (
				SELECT 1 FROM private_memory_legal_holds AS active_hold
				JOIN remember_attempts AS held_attempt ON held_attempt.space_id = active_hold.space_id
				WHERE held_attempt.team_id = remember_attempt_diagnostics.team_id
				  AND held_attempt.attempt_id = remember_attempt_diagnostics.attempt_id
				  AND held_attempt.owner_profile_id = remember_attempt_diagnostics.owner_profile_id
				  AND active_hold.released_at IS NULL
			)) THEN response_content_type ELSE '' END,
		       status_code, outcome,
		       CASE WHEN expires_at > clock_timestamp() OR (retained_by_legal_hold AND EXISTS (
				SELECT 1 FROM private_memory_legal_holds AS active_hold
				JOIN remember_attempts AS held_attempt ON held_attempt.space_id = active_hold.space_id
				WHERE held_attempt.team_id = remember_attempt_diagnostics.team_id
				  AND held_attempt.attempt_id = remember_attempt_diagnostics.attempt_id
				  AND held_attempt.owner_profile_id = remember_attempt_diagnostics.owner_profile_id
				  AND active_hold.released_at IS NULL
				)) THEN capture_state ELSE 'expired' END,
		       captured_at, expires_at,
		       (retained_by_legal_hold AND EXISTS (
				SELECT 1 FROM private_memory_legal_holds AS hold
				JOIN remember_attempts AS attempt ON attempt.space_id = hold.space_id
				WHERE attempt.team_id = remember_attempt_diagnostics.team_id
				  AND attempt.attempt_id = remember_attempt_diagnostics.attempt_id
				  AND attempt.owner_profile_id = remember_attempt_diagnostics.owner_profile_id
				  AND hold.released_at IS NULL
			))
		FROM remember_attempt_diagnostics
		WHERE team_id = ?::uuid AND attempt_id = ?::uuid
		ORDER BY sequence_no ASC
	`, teamID, attemptID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RememberAttemptDiagnosticRecordItem, 0)
	for rows.Next() {
		var item RememberAttemptDiagnosticRecordItem
		if err := rows.Scan(
			&item.DiagnosticID, &item.SequenceNo, &item.Kind, &item.Component, &item.Model,
			&item.RequestBody, &item.ResponseBody, &item.RequestContentType, &item.ResponseContentType,
			&item.StatusCode, &item.Outcome, &item.CaptureState, &item.CapturedAt, &item.ExpiresAt, &item.RetainedByLegalHold,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type rememberAttemptDiagnosticScanner interface {
	Scan(...any) error
}

func scanRememberAttemptDiagnosticSummary(scanner rememberAttemptDiagnosticScanner) (RememberAttemptDiagnosticRecord, error) {
	var record RememberAttemptDiagnosticRecord
	var spaceGeneration, durationMS int64
	var completedAt sql.NullTime
	if err := scanner.Scan(
		&record.TeamID, &record.TeamName, &record.OwnerProfileID, &record.AttemptID,
		&record.SpaceID, &spaceGeneration, &record.ContractVersion, &record.SubmissionKind,
		&record.Outcome, &record.FailedPhase, &record.ErrorCode, &record.Retryable, &record.CorrelationID,
		&record.CanonicalAttemptID, &record.EvidenceCount, &record.RelationshipCount,
		&record.DocumentCount, &record.AssessorTurns, &durationMS, &record.CreatedAt,
		&completedAt,
	); err != nil {
		return RememberAttemptDiagnosticRecord{}, err
	}
	record.SpaceGeneration = spaceGeneration
	record.Duration = time.Duration(durationMS) * time.Millisecond
	if completedAt.Valid {
		value := completedAt.Time.UTC()
		record.CompletedAt = &value
	}
	return record, nil
}

func scanRememberAttemptDiagnosticDetail(scanner rememberAttemptDiagnosticScanner) (RememberAttemptDiagnosticRecord, error) {
	record, err := scanRememberAttemptDiagnosticSummaryWithResult(scanner)
	return record, err
}

func scanRememberAttemptDiagnosticSummaryWithResult(scanner rememberAttemptDiagnosticScanner) (RememberAttemptDiagnosticRecord, error) {
	var record RememberAttemptDiagnosticRecord
	var spaceGeneration, durationMS int64
	var completedAt sql.NullTime
	var publicJSON []byte
	if err := scanner.Scan(
		&record.TeamID, &record.TeamName, &record.OwnerProfileID, &record.AttemptID,
		&record.SpaceID, &spaceGeneration, &record.ContractVersion, &record.SubmissionKind,
		&record.Outcome, &record.FailedPhase, &record.ErrorCode, &record.Retryable, &record.CorrelationID,
		&record.CanonicalAttemptID, &record.EvidenceCount, &record.RelationshipCount,
		&record.DocumentCount, &record.AssessorTurns, &durationMS, &record.CreatedAt,
		&completedAt, &publicJSON,
	); err != nil {
		return RememberAttemptDiagnosticRecord{}, err
	}
	record.SpaceGeneration = spaceGeneration
	record.Duration = time.Duration(durationMS) * time.Millisecond
	if completedAt.Valid {
		value := completedAt.Time.UTC()
		record.CompletedAt = &value
	}
	record.PublicResult = map[string]any{}
	if len(publicJSON) > 0 {
		if err := json.Unmarshal(publicJSON, &record.PublicResult); err != nil {
			return RememberAttemptDiagnosticRecord{}, fmt.Errorf("decode remember attempt public result: %w", err)
		}
	}
	return record, nil
}

func loadRememberAttemptDiagnosticEvents(ctx context.Context, tx *gorm.DB, teamID, attemptID string) ([]RememberAttemptDiagnosticEvent, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT sequence_no, phase, event_kind, outcome, metadata, created_at
		FROM remember_attempt_events
		WHERE team_id = ?::uuid AND attempt_id = ?::uuid
		ORDER BY sequence_no ASC
	`, teamID, attemptID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]RememberAttemptDiagnosticEvent, 0)
	for rows.Next() {
		var event RememberAttemptDiagnosticEvent
		var metadataJSON []byte
		if err := rows.Scan(&event.SequenceNo, &event.Phase, &event.EventKind, &event.Outcome, &metadataJSON, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.Metadata = map[string]any{}
		if len(metadataJSON) > 0 {
			if err := json.Unmarshal(metadataJSON, &event.Metadata); err != nil {
				return nil, fmt.Errorf("decode remember attempt event metadata: %w", err)
			}
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (r *Store) PurgeExpiredRememberAttemptDiagnostics(ctx context.Context, batchSize int) (int, error) {
	deleted, err := r.purgeExpiredRememberAttemptDiagnostics(ctx, batchSize)
	return deleted, err
}

func (r *Store) purgeExpiredRememberAttemptDiagnostics(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 || batchSize > rememberDiagnosticPurgeBatchSize {
		batchSize = rememberDiagnosticPurgeBatchSize
	}
	var deleted int64
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		type purgeCandidate struct {
			teamID, diagnosticID, spaceID string
		}
		candidates := make([]purgeCandidate, 0, batchSize)
		if err := tx.Exec("SELECT set_config('app.remember_attempt_diagnostic_purge', 'true', true)").Error; err != nil {
			return err
		}
		if err := tx.Exec("SELECT set_config('app.remember_failure_artifact_purge', 'true', true)").Error; err != nil {
			return err
		}
		privateRows, err := tx.WithContext(ctx).Raw(`
			SELECT diagnostic.team_id::text, diagnostic.diagnostic_id::text, attempt.space_id::text
			FROM remember_attempt_diagnostics AS diagnostic
			JOIN remember_attempts AS attempt
			  ON attempt.team_id = diagnostic.team_id
			 AND attempt.attempt_id = diagnostic.attempt_id
			 AND attempt.owner_profile_id = diagnostic.owner_profile_id
			JOIN memory_spaces AS space
			  ON space.team_id = attempt.team_id AND space.id = attempt.space_id
			WHERE diagnostic.expires_at <= clock_timestamp()
			  AND NOT EXISTS (
				SELECT 1 FROM private_memory_legal_holds AS hold
				WHERE hold.space_id = attempt.space_id AND hold.released_at IS NULL
			  )
			ORDER BY diagnostic.expires_at ASC, diagnostic.team_id ASC, diagnostic.diagnostic_id ASC
			LIMIT ?
			FOR UPDATE OF diagnostic SKIP LOCKED
			FOR KEY SHARE OF space SKIP LOCKED
		`, batchSize).Rows()
		if err != nil {
			return err
		}
		for privateRows.Next() {
			var candidate purgeCandidate
			if err := privateRows.Scan(&candidate.teamID, &candidate.diagnosticID, &candidate.spaceID); err != nil {
				_ = privateRows.Close()
				return err
			}
			candidates = append(candidates, candidate)
		}
		if err := privateRows.Err(); err != nil {
			_ = privateRows.Close()
			return err
		}
		if err := privateRows.Close(); err != nil {
			return err
		}
		if remaining := batchSize - len(candidates); remaining > 0 {
			globalRows, err := tx.WithContext(ctx).Raw(`
				SELECT diagnostic.team_id::text, diagnostic.diagnostic_id::text, ''
				FROM remember_attempt_diagnostics AS diagnostic
				JOIN remember_attempts AS attempt
				  ON attempt.team_id = diagnostic.team_id
				 AND attempt.attempt_id = diagnostic.attempt_id
				 AND attempt.owner_profile_id = diagnostic.owner_profile_id
				WHERE attempt.space_id IS NULL
				  AND diagnostic.expires_at <= clock_timestamp()
				ORDER BY diagnostic.expires_at ASC, diagnostic.team_id ASC, diagnostic.diagnostic_id ASC
				LIMIT ?
				FOR UPDATE OF diagnostic SKIP LOCKED
			`, remaining).Rows()
			if err != nil {
				return err
			}
			for globalRows.Next() {
				var candidate purgeCandidate
				if err := globalRows.Scan(&candidate.teamID, &candidate.diagnosticID, &candidate.spaceID); err != nil {
					_ = globalRows.Close()
					return err
				}
				candidates = append(candidates, candidate)
			}
			if err := globalRows.Err(); err != nil {
				_ = globalRows.Close()
				return err
			}
			if err := globalRows.Close(); err != nil {
				return err
			}
		}
		for _, candidate := range candidates {
			result := tx.WithContext(ctx).Exec(`
				DELETE FROM remember_attempt_diagnostics AS diagnostic
				WHERE diagnostic.team_id = ?::uuid AND diagnostic.diagnostic_id = ?::uuid
				  AND diagnostic.expires_at <= clock_timestamp()
				  AND NOT EXISTS (
					SELECT 1
					FROM remember_attempts AS attempt
					JOIN private_memory_legal_holds AS hold
					  ON hold.space_id = attempt.space_id AND hold.released_at IS NULL
					WHERE attempt.team_id = diagnostic.team_id
					  AND attempt.attempt_id = diagnostic.attempt_id
					  AND attempt.owner_profile_id = diagnostic.owner_profile_id
				  )
			`, candidate.teamID, candidate.diagnosticID)
			if result.Error != nil {
				return result.Error
			}
			deleted += result.RowsAffected
		}
		legacyPrivateResult := tx.WithContext(ctx).Exec(`
			WITH candidates AS (
				SELECT artifact.team_id, artifact.artifact_id
				FROM remember_failure_artifacts AS artifact
				JOIN remember_attempts AS attempt
				  ON attempt.team_id = artifact.team_id
				 AND attempt.attempt_id = artifact.attempt_id
				 AND attempt.owner_profile_id = artifact.owner_profile_id
				JOIN memory_spaces AS space
				  ON space.team_id = attempt.team_id AND space.id = attempt.space_id
				WHERE artifact.expires_at <= clock_timestamp()
				  AND NOT EXISTS (
					SELECT 1
					FROM private_memory_legal_holds AS hold
					WHERE hold.space_id = attempt.space_id AND hold.released_at IS NULL
				  )
				ORDER BY artifact.expires_at ASC, artifact.team_id ASC, artifact.artifact_id ASC
				LIMIT ?
				FOR UPDATE OF artifact SKIP LOCKED
				FOR KEY SHARE OF space SKIP LOCKED
			)
			DELETE FROM remember_failure_artifacts AS artifact
			USING candidates
			WHERE artifact.team_id = candidates.team_id
			  AND artifact.artifact_id = candidates.artifact_id
		`, batchSize)
		if legacyPrivateResult.Error != nil {
			return legacyPrivateResult.Error
		}
		deleted += legacyPrivateResult.RowsAffected
		remainingLegacy := batchSize - int(legacyPrivateResult.RowsAffected)
		if remainingLegacy > 0 {
			legacyGlobalResult := tx.WithContext(ctx).Exec(`
				WITH candidates AS (
					SELECT artifact.team_id, artifact.artifact_id
					FROM remember_failure_artifacts AS artifact
					JOIN remember_attempts AS attempt
					  ON attempt.team_id = artifact.team_id
					 AND attempt.attempt_id = artifact.attempt_id
					 AND attempt.owner_profile_id = artifact.owner_profile_id
					WHERE attempt.space_id IS NULL
					  AND artifact.expires_at <= clock_timestamp()
					ORDER BY artifact.expires_at ASC, artifact.team_id ASC, artifact.artifact_id ASC
					LIMIT ?
					FOR UPDATE OF artifact SKIP LOCKED
				)
				DELETE FROM remember_failure_artifacts AS artifact
				USING candidates
				WHERE artifact.team_id = candidates.team_id
				  AND artifact.artifact_id = candidates.artifact_id
			`, remainingLegacy)
			if legacyGlobalResult.Error != nil {
				return legacyGlobalResult.Error
			}
			deleted += legacyGlobalResult.RowsAffected
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("remember attempt diagnostic purge: %w", err)
	}
	return int(deleted), nil
}

func drainExpiredRememberAttemptDiagnostics(ctx context.Context, repo *Store) (int, error) {
	deletedTotal := 0
	for {
		deleted, err := repo.purgeExpiredRememberAttemptDiagnostics(ctx, rememberDiagnosticPurgeBatchSize)
		deletedTotal += deleted
		if err != nil {
			return deletedTotal, err
		}
		if deleted < rememberDiagnosticPurgeBatchSize {
			return deletedTotal, nil
		}
		if err := ctx.Err(); err != nil {
			return deletedTotal, err
		}
	}
}

// StartRememberAttemptDiagnosticPurger exposes the capability-owned diagnostic
// retention worker.
func (r *Store) StartRememberAttemptDiagnosticPurger(ctx context.Context, interval time.Duration, logger *slog.Logger) <-chan struct{} {
	if r == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	if interval <= 0 {
		interval = time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.rememberDiagnosticLifecycleMu.Lock()
	if r.rememberDiagnosticDone != nil {
		done := r.rememberDiagnosticDone
		r.rememberDiagnosticLifecycleMu.Unlock()
		return done
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.rememberDiagnosticCancel = cancel
	r.rememberDiagnosticDone = done
	r.rememberDiagnosticLifecycleMu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				deleted, err := r.purgeRememberAttemptDiagnostics(workerCtx)
				if err != nil && workerCtx.Err() == nil {
					logger.Warn("remember attempt diagnostic purge failed", "error_code", "diagnostic_purge_failed")
				} else if deleted > 0 {
					logger.Info("remember attempt diagnostics purged", "count", deleted)
				}
			}
		}
	}()
	return done
}

func (r *Store) purgeRememberAttemptDiagnostics(ctx context.Context) (int, error) {
	if r != nil && r.rememberDiagnosticPurgeFn != nil {
		return r.rememberDiagnosticPurgeFn(ctx)
	}
	return drainExpiredRememberAttemptDiagnostics(ctx, r)
}

// ShutdownRememberAttemptDiagnosticPurger cancels the diagnostic retention
// worker and waits for it to stop before returning.
func (r *Store) ShutdownRememberAttemptDiagnosticPurger(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.rememberDiagnosticLifecycleMu.Lock()
	cancel := r.rememberDiagnosticCancel
	done := r.rememberDiagnosticDone
	r.rememberDiagnosticLifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		r.rememberDiagnosticLifecycleMu.Lock()
		if r.rememberDiagnosticDone == done {
			r.rememberDiagnosticCancel = nil
			r.rememberDiagnosticDone = nil
		}
		r.rememberDiagnosticLifecycleMu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
