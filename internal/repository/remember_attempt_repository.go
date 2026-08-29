package repository

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
)

// ErrRememberReplay identifies a terminal attempt that owns an idempotency
// key. Callers reload its public result instead of reconstructing it.
var ErrRememberReplay = errors.New("remember result already committed")

var ErrRememberAttemptNotFound = errors.New("remember attempt not found")

var (
	ErrRememberAttemptDiagnosticNotFound = errors.New("remember attempt diagnostic not found")
	ErrRememberFailureArtifactNotFound   = errors.New("remember failure artifact not found")
)

type RememberAttemptRecordInput struct {
	TeamID, OwnerProfileID, AttemptID string
	SpaceID                           string
	SpaceGeneration                   int64
	IdempotencyKey, RequestHash       string
	MigratedRequestHash               string
	ContractVersion, SubmissionKind   string
	Outcome, FailedPhase, ErrorCode   string
	CorrelationID                     string
	PublicResult                      map[string]any
	EvidenceCount, RelationshipCount  int
	DocumentCount, AssessorTurns      int
	Duration                          time.Duration
}

type RememberAttempt struct {
	AttemptID, RequestHash, ContractVersion, Outcome string
	PublicResult                                     map[string]any
}

type RememberAttemptLookupInput struct {
	TeamID, OwnerProfileID, IdempotencyKey string
}

type RememberAttemptLookup interface {
	LoadRememberAttempt(context.Context, RememberAttemptLookupInput) (*RememberAttempt, error)
}

// RememberFailureArtifactInput is intentionally failure-only. Its content must
// already be scrubbed of credentials, prompts, provider responses, and database
// errors by the application service.
type RememberFailureArtifactInput struct {
	ArtifactID, ArtifactKind, ContentType string
	Content                               []byte
	CapturedAt, ExpiresAt                 time.Time
}

type RememberFailureRecordInput struct {
	Attempt   RememberAttemptRecordInput
	Artifacts []RememberFailureArtifactInput
}

// RememberAttemptDiagnosticFilter is the normalized filter supplied by the
// diagnostics service for the control-portal attempt read model. It
// deliberately uses the durable attempt outcome rather than a placement
// processing state.
type RememberAttemptDiagnosticFilter struct {
	TeamID  string
	Outcome string
	Limit   int
	Offset  int
}

// RememberAttemptDiagnosticRecord is a control-only projection. PublicResult,
// Events, and Artifacts are populated only by the single-attempt detail read;
// list records contain scalar metadata only.
type RememberAttemptDiagnosticRecord struct {
	TeamID, TeamName, OwnerProfileID, AttemptID string
	SpaceID, CanonicalAttemptID                 string
	SpaceGeneration                             int64
	ContractVersion, SubmissionKind             string
	Outcome, FailedPhase, ErrorCode             string
	CorrelationID                               string
	EvidenceCount, RelationshipCount            int
	DocumentCount, AssessorTurns                int
	Duration                                    time.Duration
	CreatedAt                                   time.Time
	CompletedAt                                 *time.Time
	PublicResult                                map[string]any
	Events                                      []RememberAttemptDiagnosticEvent
	Artifacts                                   []RememberFailureArtifactDescriptor
}

type RememberAttemptDiagnosticEvent struct {
	SequenceNo int
	Phase      string
	EventKind  string
	Outcome    string
	Metadata   map[string]any
	CreatedAt  time.Time
}

type RememberFailureArtifactDescriptor struct {
	ArtifactID    string
	ArtifactKind  string
	ContentType   string
	ByteCount     int64
	ContentSHA256 string
	CapturedAt    time.Time
	ExpiresAt     time.Time
}

type RememberFailureArtifact struct {
	TeamID        string
	ArtifactID    string
	AttemptID     string
	ArtifactKind  string
	ContentType   string
	Content       []byte
	ByteCount     int64
	ContentSHA256 string
	CapturedAt    time.Time
	ExpiresAt     time.Time
}

type RememberAttemptDiagnosticRecordPage struct {
	Records []RememberAttemptDiagnosticRecord
	Total   int64
}

// RememberAttemptDiagnosticsRepository is the narrow application port for
// control-only attempt diagnostics and retention.
type RememberAttemptDiagnosticsRepository interface {
	ListRememberAttemptDiagnostics(context.Context, RememberAttemptDiagnosticFilter) (*RememberAttemptDiagnosticRecordPage, error)
	GetRememberAttemptDiagnostic(context.Context, string, string) (*RememberAttemptDiagnosticRecord, error)
	GetRememberFailureArtifact(context.Context, string, string, string) (*RememberFailureArtifact, error)
	PurgeExpiredRememberFailureArtifacts(context.Context, int) (int, error)
}

var _ RememberAttemptDiagnosticsRepository = (*LedgerRepositoryImpl)(nil)

const (
	maxRememberFailureArtifactBytes       = 256 * 1024
	maxRememberFailureArtifactRetention   = 7 * 24 * time.Hour
	rememberFailureArtifactPurgeBatchSize = 100
)

func lockRememberIdempotencyKeyInTx(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, key string) error {
	digest := sha256.Sum256([]byte(teamID + "\x00" + ownerProfileID + "\x00" + key))
	return tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(?, ?)`,
		int32(binary.BigEndian.Uint32(digest[:4])), int32(binary.BigEndian.Uint32(digest[4:8]))).Error
}

func validateRememberFailureRetryInTx(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, key, requestHash, migratedHash string) error {
	var hasHashMismatch bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM remember_attempts
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
			  AND idempotency_key = ? AND outcome = 'failed'
			  AND NOT (
				  request_hash = ?
				  OR (contract_version = ? AND ? <> '' AND request_hash = ?)
			  )
		)
	`, teamID, ownerProfileID, key, requestHash, domain.MigratedRememberRequestHashVersion, migratedHash, migratedHash).Row().Scan(&hasHashMismatch); err != nil {
		return err
	}
	if hasHashMismatch {
		return fmt.Errorf("%w: idempotency key reused with a different request hash", ErrIdempotencyConflict)
	}
	return nil
}

func (r *LedgerRepositoryImpl) LoadRememberAttempt(ctx context.Context, input RememberAttemptLookupInput) (*RememberAttempt, error) {
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
			SELECT attempt_id::text, request_hash, contract_version, outcome, public_result
			FROM remember_attempts
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?
			ORDER BY CASE WHEN outcome IN ('completed', 'rejected', 'quarantined', 'replayed') THEN 0 ELSE 1 END,
			         created_at DESC, attempt_id DESC LIMIT 1
		`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Row().Scan(&stored.AttemptID, &stored.RequestHash, &stored.ContractVersion, &stored.Outcome, &raw)
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

func loadTerminalRememberAttemptInTx(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, key, requestHash, migratedHash string) (*RememberAttempt, error) {
	var result RememberAttempt
	var raw []byte
	err := tx.WithContext(ctx).Raw(`
		SELECT attempt_id::text, request_hash, contract_version, outcome, public_result
		FROM remember_attempts
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?
		  AND outcome IN ('completed', 'rejected', 'quarantined')
		ORDER BY created_at DESC, attempt_id DESC LIMIT 1
	`, teamID, ownerProfileID, key).Row().Scan(&result.AttemptID, &result.RequestHash, &result.ContractVersion, &result.Outcome, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !domain.RememberRequestHashMatches(result.RequestHash, result.ContractVersion, requestHash, migratedHash) {
		return nil, fmt.Errorf("%w: idempotency key reused with a different request hash", ErrIdempotencyConflict)
	}
	result.PublicResult = map[string]any{}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &result.PublicResult); err != nil {
			return nil, err
		}
	}
	return &result, nil
}

func (r *LedgerRepositoryImpl) RecordRememberAttempt(ctx context.Context, input RememberAttemptRecordInput) error {
	input = normalizeRememberAttemptRecord(input)
	if err := validateRememberAttemptRecord(input); err != nil {
		return err
	}
	return r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error { return insertRememberAttemptInTx(ctx, tx, input) })
}

// RecordRememberFailure keeps a bounded operational record without creating
// canonical ingest, evidence, placement, semantic, or search state.
func (r *LedgerRepositoryImpl) RecordRememberFailure(ctx context.Context, input RememberFailureRecordInput) error {
	input.Attempt = normalizeRememberAttemptRecord(input.Attempt)
	input.Attempt.Outcome = "failed"
	if err := validateRememberAttemptRecord(input.Attempt); err != nil {
		return err
	}
	if input.Attempt.FailedPhase == "" {
		input.Attempt.FailedPhase = "execution"
	}
	if input.Attempt.ErrorCode == "" {
		input.Attempt.ErrorCode = "internal_failure"
	}
	for index := range input.Artifacts {
		artifact := &input.Artifacts[index]
		artifact.ArtifactID, artifact.ArtifactKind, artifact.ContentType = strings.TrimSpace(artifact.ArtifactID), strings.TrimSpace(artifact.ArtifactKind), strings.TrimSpace(artifact.ContentType)
		if artifact.ArtifactID == "" {
			artifact.ArtifactID = uuid.NewString()
		}
		if _, err := uuid.Parse(artifact.ArtifactID); err != nil {
			return fmt.Errorf("remember failure: artifact[%d] id is invalid: %w", index, err)
		}
		if artifact.ArtifactKind == "" || artifact.ContentType == "" {
			return fmt.Errorf("remember failure: artifact[%d] kind and content type are required", index)
		}
		if len(artifact.Content) > maxRememberFailureArtifactBytes {
			return fmt.Errorf("remember failure: artifact[%d] exceeds %d bytes", index, maxRememberFailureArtifactBytes)
		}
		if !artifact.CapturedAt.IsZero() && !artifact.ExpiresAt.IsZero() &&
			(artifact.ExpiresAt.Before(artifact.CapturedAt) || artifact.ExpiresAt.After(artifact.CapturedAt.Add(maxRememberFailureArtifactRetention))) {
			return fmt.Errorf("remember failure: artifact[%d] expiry is outside retention", index)
		}
	}
	return r.withAtomicRememberTx(ctx, input.Attempt.TeamID, input.Attempt.OwnerProfileID, func(txCtx context.Context) error {
		tx := transactionFromContext(txCtx)
		var databaseNow time.Time
		for index := range input.Artifacts {
			artifact := &input.Artifacts[index]
			if artifact.CapturedAt.IsZero() || artifact.ExpiresAt.IsZero() {
				if databaseNow.IsZero() {
					if err := tx.WithContext(txCtx).Raw(`SELECT clock_timestamp()`).Row().Scan(&databaseNow); err != nil {
						return fmt.Errorf("remember failure: database clock: %w", err)
					}
					databaseNow = databaseNow.UTC()
				}
				if artifact.CapturedAt.IsZero() {
					artifact.CapturedAt = databaseNow
				}
				if artifact.ExpiresAt.IsZero() {
					artifact.ExpiresAt = artifact.CapturedAt.Add(maxRememberFailureArtifactRetention)
				}
			}
			if artifact.ExpiresAt.Before(artifact.CapturedAt) || artifact.ExpiresAt.After(artifact.CapturedAt.Add(maxRememberFailureArtifactRetention)) {
				return fmt.Errorf("remember failure: artifact[%d] expiry is outside retention", index)
			}
		}
		if err := insertRememberAttemptInTx(txCtx, tx, input.Attempt); err != nil {
			return err
		}
		for _, artifact := range input.Artifacts {
			digest := sha256.Sum256(artifact.Content)
			if err := tx.WithContext(txCtx).Exec(`
				INSERT INTO remember_failure_artifacts (team_id, artifact_id, attempt_id, owner_profile_id, artifact_kind,
				 content_type, content_bytes, byte_count, content_sha256, captured_at, expires_at)
				VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?, ?)
			`, input.Attempt.TeamID, artifact.ArtifactID, input.Attempt.AttemptID, input.Attempt.OwnerProfileID,
				artifact.ArtifactKind, artifact.ContentType, artifact.Content, len(artifact.Content), "sha256:"+fmt.Sprintf("%x", digest[:]), artifact.CapturedAt.UTC(), artifact.ExpiresAt.UTC()).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func normalizeRememberAttemptRecord(input RememberAttemptRecordInput) RememberAttemptRecordInput {
	input.TeamID, input.OwnerProfileID, input.AttemptID = strings.TrimSpace(input.TeamID), strings.TrimSpace(input.OwnerProfileID), strings.TrimSpace(input.AttemptID)
	input.SpaceID, input.IdempotencyKey, input.RequestHash = strings.TrimSpace(input.SpaceID), strings.TrimSpace(input.IdempotencyKey), strings.TrimSpace(input.RequestHash)
	input.MigratedRequestHash, input.ContractVersion, input.SubmissionKind = strings.TrimSpace(input.MigratedRequestHash), strings.TrimSpace(input.ContractVersion), strings.TrimSpace(input.SubmissionKind)
	input.Outcome, input.FailedPhase, input.ErrorCode, input.CorrelationID = strings.TrimSpace(input.Outcome), strings.TrimSpace(input.FailedPhase), strings.TrimSpace(input.ErrorCode), strings.TrimSpace(input.CorrelationID)
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
	if input.EvidenceCount < 0 || input.RelationshipCount < 0 || input.DocumentCount < 0 || input.AssessorTurns < 0 || input.Duration < 0 {
		return errors.New("remember attempt: counters and duration cannot be negative")
	}
	return nil
}

func insertRememberAttemptInTx(ctx context.Context, tx *gorm.DB, input RememberAttemptRecordInput) error {
	if err := lockRememberIdempotencyKeyInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey); err != nil {
		return err
	}
	if err := validateRememberFailureRetryInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, input.RequestHash, input.MigratedRequestHash); err != nil {
		return err
	}
	if existing, err := loadTerminalRememberAttemptInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, input.RequestHash, input.MigratedRequestHash); err != nil {
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
		 idempotency_key, request_hash, contract_version, submission_kind, outcome, failed_phase, error_code,
		 correlation_id, public_result, evidence_count, relationship_count, document_count, assessor_turns, duration_ms, completed_at)
		VALUES (?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?, 0), ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, now())
	`, input.TeamID, input.AttemptID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration, input.IdempotencyKey, input.RequestHash, input.ContractVersion, input.SubmissionKind, input.Outcome, input.FailedPhase, input.ErrorCode, input.CorrelationID, string(encoded), input.EvidenceCount, input.RelationshipCount, input.DocumentCount, input.AssessorTurns, input.Duration.Milliseconds())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRememberReplay
	}
	metadata, err := json.Marshal(map[string]any{"contract_version": input.ContractVersion, "assessor_turns": input.AssessorTurns, "document_count": input.DocumentCount, "error_code": input.ErrorCode})
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

func (r *LedgerRepositoryImpl) ListRememberAttemptDiagnostics(
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
			       attempt.error_code, attempt.correlation_id,
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

func (r *LedgerRepositoryImpl) GetRememberAttemptDiagnostic(ctx context.Context, teamID, attemptID string) (*RememberAttemptDiagnosticRecord, error) {
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
			       attempt.error_code, attempt.correlation_id,
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
		value.Artifacts, err = loadRememberFailureArtifactDescriptors(ctx, tx, teamID, attemptID)
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
		&record.Outcome, &record.FailedPhase, &record.ErrorCode, &record.CorrelationID,
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
		&record.Outcome, &record.FailedPhase, &record.ErrorCode, &record.CorrelationID,
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

func loadRememberFailureArtifactDescriptors(ctx context.Context, tx *gorm.DB, teamID, attemptID string) ([]RememberFailureArtifactDescriptor, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT artifact_id::text, artifact_kind, content_type, byte_count,
		       content_sha256, captured_at, expires_at
		FROM remember_failure_artifacts
		WHERE team_id = ?::uuid AND attempt_id = ?::uuid
		  AND expires_at > clock_timestamp()
		ORDER BY captured_at ASC, artifact_id ASC
	`, teamID, attemptID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RememberFailureArtifactDescriptor, 0)
	for rows.Next() {
		var item RememberFailureArtifactDescriptor
		if err := rows.Scan(&item.ArtifactID, &item.ArtifactKind, &item.ContentType, &item.ByteCount, &item.ContentSHA256, &item.CapturedAt, &item.ExpiresAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *LedgerRepositoryImpl) GetRememberFailureArtifact(ctx context.Context, teamID, attemptID, artifactID string) (*RememberFailureArtifact, error) {
	teamID, attemptID, artifactID = strings.TrimSpace(teamID), strings.TrimSpace(attemptID), strings.TrimSpace(artifactID)
	for label, value := range map[string]string{"team_id": teamID, "attempt_id": attemptID, "artifact_id": artifactID} {
		if _, err := uuid.Parse(value); err != nil {
			return nil, fmt.Errorf("remember artifact: %s is invalid: %w", label, err)
		}
	}
	var artifact RememberFailureArtifact
	err := r.withSystemReadOnlyRepeatableTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT team_id::text, artifact_id::text, attempt_id::text, artifact_kind,
			       content_type, content_bytes, byte_count, content_sha256, captured_at, expires_at
			FROM remember_failure_artifacts
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid AND artifact_id = ?::uuid
			  AND expires_at > clock_timestamp()
		`, teamID, attemptID, artifactID).Row().Scan(
			&artifact.TeamID, &artifact.ArtifactID, &artifact.AttemptID, &artifact.ArtifactKind,
			&artifact.ContentType, &artifact.Content, &artifact.ByteCount, &artifact.ContentSHA256,
			&artifact.CapturedAt, &artifact.ExpiresAt,
		)
	})
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRememberFailureArtifactNotFound
	}
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

func (r *LedgerRepositoryImpl) PurgeExpiredRememberFailureArtifacts(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 || batchSize > rememberFailureArtifactPurgeBatchSize {
		batchSize = rememberFailureArtifactPurgeBatchSize
	}
	var deleted int64
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			WITH expired AS (
				SELECT team_id, artifact_id
				FROM remember_failure_artifacts
				WHERE expires_at <= clock_timestamp()
				ORDER BY expires_at ASC, team_id ASC, artifact_id ASC
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			)
			DELETE FROM remember_failure_artifacts AS artifact
			USING expired
			WHERE artifact.team_id = expired.team_id
			  AND artifact.artifact_id = expired.artifact_id
		`, batchSize)
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("remember failure artifact purge: %w", err)
	}
	return int(deleted), nil
}

func drainExpiredRememberFailureArtifacts(ctx context.Context, repo *LedgerRepositoryImpl) (int, error) {
	deletedTotal := 0
	for {
		deleted, err := repo.PurgeExpiredRememberFailureArtifacts(ctx, rememberFailureArtifactPurgeBatchSize)
		deletedTotal += deleted
		if err != nil {
			return deletedTotal, err
		}
		if deleted < rememberFailureArtifactPurgeBatchSize {
			return deletedTotal, nil
		}
		if err := ctx.Err(); err != nil {
			return deletedTotal, err
		}
	}
}

// StartRememberFailureArtifactPurger exposes the capability-owned retention
// worker for the future T09 production lifecycle. T07 deliberately does not
// call it from normal server startup.
func (r *LedgerRepositoryImpl) StartRememberFailureArtifactPurger(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				deleted, err := drainExpiredRememberFailureArtifacts(ctx, r)
				if err != nil && ctx.Err() == nil {
					logger.Warn("remember failure artifact purge failed", "error_code", "artifact_purge_failed")
				} else if deleted > 0 {
					logger.Info("remember failure artifacts purged", "count", deleted)
				}
			}
		}
	}()
}
