package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrRememberReplay identifies a terminal attempt that owns an idempotency
// key. Callers reload its public result instead of reconstructing it.
var ErrRememberReplay = errors.New("remember result already committed")

var ErrRememberAttemptNotFound = errors.New("remember attempt not found")

// MigratedRememberRequestHashVersion identifies the legacy request-hash
// contract whose hash may be accepted as an equivalent retry identity.
const MigratedRememberRequestHashVersion = "remember_request_hash_v1"

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

func lockRememberIdempotencyKeyInTx(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, key string) error {
	digest := sha256.Sum256([]byte(teamID + "\x00" + ownerProfileID + "\x00" + key))
	return tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(?, ?)`,
		int32(binary.BigEndian.Uint32(digest[:4])), int32(binary.BigEndian.Uint32(digest[4:8]))).Error
}

func rememberAttemptHashMatches(storedHash, storedContract, requestHash, migratedHash string) bool {
	if strings.TrimSpace(storedHash) == strings.TrimSpace(requestHash) {
		return true
	}
	return strings.TrimSpace(storedContract) == MigratedRememberRequestHashVersion &&
		strings.TrimSpace(migratedHash) != "" && strings.TrimSpace(storedHash) == strings.TrimSpace(migratedHash)
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
	`, teamID, ownerProfileID, key, requestHash, MigratedRememberRequestHashVersion, migratedHash, migratedHash).Row().Scan(&hasHashMismatch); err != nil {
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
	if !rememberAttemptHashMatches(result.RequestHash, result.ContractVersion, requestHash, migratedHash) {
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
		if len(artifact.Content) > 256*1024 {
			return fmt.Errorf("remember failure: artifact[%d] exceeds 262144 bytes", index)
		}
		if artifact.CapturedAt.IsZero() {
			artifact.CapturedAt = time.Now().UTC()
		}
		if artifact.ExpiresAt.IsZero() {
			artifact.ExpiresAt = artifact.CapturedAt.Add(7 * 24 * time.Hour)
		}
		if artifact.ExpiresAt.Before(artifact.CapturedAt) || artifact.ExpiresAt.After(artifact.CapturedAt.Add(7*24*time.Hour)) {
			return fmt.Errorf("remember failure: artifact[%d] expiry is outside retention", index)
		}
	}
	return r.withAtomicRememberTx(ctx, input.Attempt.TeamID, input.Attempt.OwnerProfileID, func(txCtx context.Context) error {
		tx := transactionFromContext(txCtx)
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
	if input.Outcome == "failed" && input.FailedPhase != "" {
		return input.FailedPhase
	}
	return "commit"
}
func rememberAttemptEventKind(input RememberAttemptRecordInput) string {
	if input.Outcome == "failed" {
		return rememberAttemptPhase(input) + "_failed"
	}
	return "commit_completed"
}
