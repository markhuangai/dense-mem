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

type RememberAttemptRecordInput struct {
	TeamID            string
	OwnerProfileID    string
	AttemptID         string
	SpaceID           string
	SpaceGeneration   int64
	IdempotencyKey    string
	RequestHash       string
	ContractVersion   string
	SubmissionKind    string
	Outcome           string
	FailedPhase       string
	ErrorCode         string
	CorrelationID     string
	PublicResult      map[string]any
	EvidenceCount     int
	RelationshipCount int
	DocumentCount     int
	AssessorTurns     int
	Duration          time.Duration
}

type RememberAttemptRecorder interface {
	RecordRememberAttempt(context.Context, RememberAttemptRecordInput) error
}

var ErrRememberAttemptNotFound = errors.New("remember attempt not found")

type RememberAttempt struct {
	AttemptID       string
	RequestHash     string
	ContractVersion string
	Outcome         string
	PublicResult    map[string]any
}

type RememberAttemptLookup interface {
	LoadRememberAttempt(context.Context, RememberAttemptLookupInput) (*RememberAttempt, error)
}

type RememberAttemptLookupInput struct {
	TeamID         string
	OwnerProfileID string
	IdempotencyKey string
}

const maxRememberFailureArtifactBytes = 256 * 1024
const maxRememberFailureArtifactRetention = 7 * 24 * time.Hour
const rememberFailureArtifactPurgeBatchSize = 1000

func lockRememberIdempotencyKeyInTx(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, idempotencyKey string) error {
	digest := sha256.Sum256([]byte(teamID + "\x00" + ownerProfileID + "\x00" + idempotencyKey))
	first := int32(binary.BigEndian.Uint32(digest[:4]))
	second := int32(binary.BigEndian.Uint32(digest[4:8]))
	return tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(?, ?)`, first, second).Error
}

func rememberAttemptHashMatches(storedHash, storedContractVersion, requestHash, migratedRequestHash string) bool {
	if strings.TrimSpace(storedHash) == strings.TrimSpace(requestHash) {
		return true
	}
	return strings.TrimSpace(storedContractVersion) == domain.MigratedRememberRequestHashVersion &&
		strings.TrimSpace(migratedRequestHash) != "" &&
		strings.TrimSpace(storedHash) == strings.TrimSpace(migratedRequestHash)
}

func checkRememberFailureIdempotencyInTx(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, idempotencyKey, requestHash, migratedRequestHash string) error {
	var hasFailure, hasHashMismatch bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT
			EXISTS (
				SELECT 1
				FROM remember_attempts
				WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
				  AND idempotency_key = ? AND outcome = 'failed'
			),
			EXISTS (
				SELECT 1
				FROM remember_attempts
				WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
				  AND idempotency_key = ? AND outcome = 'failed'
				  AND NOT (
					  request_hash = ?
					  OR (contract_version = ? AND ? <> '' AND request_hash = ?)
				  )
		)
	`, teamID, ownerProfileID, idempotencyKey, teamID, ownerProfileID, idempotencyKey,
		requestHash, domain.MigratedRememberRequestHashVersion, migratedRequestHash, migratedRequestHash).Row().Scan(&hasFailure, &hasHashMismatch); err != nil {
		return err
	}
	if hasHashMismatch {
		return fmt.Errorf("%w: idempotency key reused with a different request hash", ErrIdempotencyConflict)
	}
	if hasFailure {
		return ErrRememberReplay
	}
	return nil
}

// RememberFailureArtifactInput is a bounded, failure-only diagnostic payload.
// Callers must never place credentials, provider prompts, or database errors in
// Content.
type RememberFailureArtifactInput struct {
	ArtifactID   string
	ArtifactKind string
	ContentType  string
	Content      []byte
	CapturedAt   time.Time
	ExpiresAt    time.Time
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

type RememberFailureArtifactDescriptor struct {
	ArtifactID    string    `json:"artifact_id"`
	ArtifactKind  string    `json:"artifact_kind"`
	ContentType   string    `json:"content_type"`
	ByteCount     int64     `json:"byte_count"`
	ContentSHA256 string    `json:"content_sha256"`
	CapturedAt    time.Time `json:"captured_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type RememberFailureRecordInput struct {
	TeamID              string
	OwnerProfileID      string
	AttemptID           string
	SpaceID             string
	SpaceGeneration     int64
	IdempotencyKey      string
	RequestHash         string
	MigratedRequestHash string
	ContractVersion     string
	SubmissionKind      string
	FailedPhase         string
	ErrorCode           string
	CorrelationID       string
	PublicResult        map[string]any
	EvidenceCount       int
	RelationshipCount   int
	DocumentCount       int
	AssessorTurns       int
	Duration            time.Duration
	Artifacts           []RememberFailureArtifactInput
}

type RememberFailureArtifactReader interface {
	GetRememberFailureArtifact(context.Context, string, string, string) (*RememberFailureArtifact, error)
	PurgeExpiredRememberFailureArtifacts(context.Context, time.Time, int) (int, error)
}

var ErrRememberFailureArtifactNotFound = errors.New("remember failure artifact not found")

// RecordRememberFailure persists a replayable failed attempt and its bounded
// artifacts without creating canonical ingest, evidence, or search state.
func (r *LedgerRepositoryImpl) RecordRememberFailure(ctx context.Context, input RememberFailureRecordInput) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	input.MigratedRequestHash = strings.TrimSpace(input.MigratedRequestHash)
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.SubmissionKind = strings.TrimSpace(input.SubmissionKind)
	input.FailedPhase = strings.TrimSpace(input.FailedPhase)
	input.ErrorCode = strings.TrimSpace(input.ErrorCode)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("remember failure: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("remember failure: owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.AttemptID); err != nil {
		return fmt.Errorf("remember failure: attempt_id is required: %w", err)
	}
	if input.IdempotencyKey == "" || input.RequestHash == "" || input.ContractVersion == "" || input.SubmissionKind == "" {
		return errors.New("remember failure: identity fields are required")
	}
	if input.FailedPhase == "" {
		input.FailedPhase = "execution"
	}
	if input.ErrorCode == "" {
		input.ErrorCode = "internal_failure"
	}
	for index := range input.Artifacts {
		artifact := &input.Artifacts[index]
		artifact.ArtifactKind = strings.TrimSpace(artifact.ArtifactKind)
		artifact.ContentType = strings.TrimSpace(artifact.ContentType)
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
		if artifact.CapturedAt.IsZero() {
			artifact.CapturedAt = time.Now().UTC()
		}
		if artifact.ExpiresAt.IsZero() {
			artifact.ExpiresAt = artifact.CapturedAt.Add(maxRememberFailureArtifactRetention)
		}
		if artifact.ExpiresAt.Before(artifact.CapturedAt) {
			return fmt.Errorf("remember failure: artifact[%d] expiry precedes capture", index)
		}
		if artifact.ExpiresAt.After(artifact.CapturedAt.Add(maxRememberFailureArtifactRetention)) {
			return fmt.Errorf("remember failure: artifact[%d] retention exceeds seven days", index)
		}
	}
	return r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockRememberIdempotencyKeyInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey); err != nil {
			return err
		}
		if err := checkRememberFailureIdempotencyInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, input.RequestHash, input.MigratedRequestHash); err != nil {
			return err
		}
		if existing, err := loadCanonicalRememberAttemptByKeyInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey); err != nil {
			return err
		} else if existing != nil {
			if !rememberAttemptHashMatches(existing.RequestHash, existing.ContractVersion, input.RequestHash, input.MigratedRequestHash) {
				return fmt.Errorf("%w: idempotency key reused with a different request hash", ErrIdempotencyConflict)
			}
			return ErrRememberReplay
		}
		publicResult := input.PublicResult
		if publicResult == nil {
			publicResult = map[string]any{}
		}
		if err := insertRememberAttemptInTx(ctx, tx, RememberAttemptRecordInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, AttemptID: input.AttemptID,
			SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
			RequestHash: input.RequestHash, ContractVersion: input.ContractVersion, SubmissionKind: input.SubmissionKind,
			Outcome: "failed", FailedPhase: input.FailedPhase, ErrorCode: input.ErrorCode,
			CorrelationID: input.CorrelationID, PublicResult: publicResult,
			EvidenceCount: input.EvidenceCount, RelationshipCount: input.RelationshipCount,
			DocumentCount: input.DocumentCount, AssessorTurns: input.AssessorTurns, Duration: input.Duration,
		}); err != nil {
			if errors.Is(err, ErrRememberReplay) {
				existing, lookupErr := loadCanonicalRememberAttemptByKeyInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey)
				if lookupErr != nil {
					return lookupErr
				}
				if existing != nil {
					if !rememberAttemptHashMatches(existing.RequestHash, existing.ContractVersion, input.RequestHash, input.MigratedRequestHash) {
						return fmt.Errorf("%w: idempotency key reused with a different request hash", ErrIdempotencyConflict)
					}
					return ErrRememberReplay
				}
			}
			return err
		}
		for _, artifact := range input.Artifacts {
			digest := sha256.Sum256(artifact.Content)
			if err := tx.WithContext(ctx).Exec(`
				INSERT INTO remember_failure_artifacts (
				    team_id, artifact_id, attempt_id, owner_profile_id, artifact_kind,
				    content_type, content_bytes, byte_count, content_sha256, captured_at, expires_at
				) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?, ?)
			`, input.TeamID, artifact.ArtifactID, input.AttemptID, input.OwnerProfileID,
				artifact.ArtifactKind, artifact.ContentType, artifact.Content, len(artifact.Content),
				"sha256:"+fmt.Sprintf("%x", digest[:]), artifact.CapturedAt.UTC(), artifact.ExpiresAt.UTC()).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *LedgerRepositoryImpl) GetRememberFailureArtifact(ctx context.Context, teamID, attemptID, artifactID string) (*RememberFailureArtifact, error) {
	teamID, attemptID, artifactID = strings.TrimSpace(teamID), strings.TrimSpace(attemptID), strings.TrimSpace(artifactID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("remember artifact: team_id is invalid: %w", err)
	}
	if _, err := uuid.Parse(attemptID); err != nil {
		return nil, fmt.Errorf("remember artifact: attempt_id is invalid: %w", err)
	}
	if _, err := uuid.Parse(artifactID); err != nil {
		return nil, fmt.Errorf("remember artifact: artifact_id is invalid: %w", err)
	}
	var artifact RememberFailureArtifact
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT team_id::text, artifact_id::text, attempt_id::text, artifact_kind,
			       content_type, content_bytes, byte_count, content_sha256, captured_at, expires_at
			FROM remember_failure_artifacts
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid AND artifact_id = ?::uuid
			  AND expires_at > clock_timestamp()
		`, teamID, attemptID, artifactID).Row().Scan(
			&artifact.TeamID, &artifact.ArtifactID, &artifact.AttemptID, &artifact.ArtifactKind,
			&artifact.ContentType, &artifact.Content, &artifact.ByteCount, &artifact.ContentSHA256,
			&artifact.CapturedAt, &artifact.ExpiresAt)
	})
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRememberFailureArtifactNotFound
	}
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

func (r *LedgerRepositoryImpl) PurgeExpiredRememberFailureArtifacts(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 || limit > rememberFailureArtifactPurgeBatchSize {
		limit = rememberFailureArtifactPurgeBatchSize
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var deleted int64
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Exec(`SELECT set_config('app.remember_failure_artifact_purge', 'true', true)`).Error; err != nil {
			return err
		}
		result := tx.WithContext(ctx).Exec(`
			WITH expired AS (
				SELECT team_id, artifact_id
				FROM remember_failure_artifacts
				WHERE expires_at <= ?
				ORDER BY expires_at, team_id, artifact_id
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			)
			DELETE FROM remember_failure_artifacts AS artifact
			USING expired
			WHERE artifact.team_id = expired.team_id AND artifact.artifact_id = expired.artifact_id
		`, now.UTC(), limit)
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		return nil
	})
	return int(deleted), err
}

func (r *LedgerRepositoryImpl) purgeExpiredRememberFailureArtifactsUntilDrained(ctx context.Context, now time.Time) (int, error) {
	deletedTotal := 0
	for {
		deleted, err := r.PurgeExpiredRememberFailureArtifacts(ctx, now, rememberFailureArtifactPurgeBatchSize)
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

// StartRememberFailureArtifactPurger removes expired failure bytes while
// preserving attempt/event metadata and hashes.
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
				deleted, err := r.purgeExpiredRememberFailureArtifactsUntilDrained(ctx, time.Now().UTC())
				if err != nil {
					logger.Warn("remember failure artifact purge failed", "error_code", "artifact_purge_failed")
				} else if deleted > 0 {
					logger.Info("remember failure artifacts purged", "count", deleted)
				}
			}
		}
	}()
}

func (r *LedgerRepositoryImpl) LoadRememberAttempt(ctx context.Context, input RememberAttemptLookupInput) (*RememberAttempt, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("remember attempt: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return nil, fmt.Errorf("remember attempt: owner_profile_id is required: %w", err)
	}
	if input.IdempotencyKey == "" {
		return nil, errors.New("remember attempt: idempotency_key is required")
	}
	var attempt RememberAttempt
	var publicJSON []byte
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT attempt_id::text, request_hash, contract_version, outcome, public_result
			FROM remember_attempts
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
			  AND idempotency_key = ?
			ORDER BY CASE WHEN outcome IN ('completed', 'rejected', 'quarantined', 'replayed') THEN 0 ELSE 1 END,
			         created_at DESC, attempt_id DESC
			LIMIT 1
		`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Row().Scan(
			&attempt.AttemptID, &attempt.RequestHash, &attempt.ContractVersion, &attempt.Outcome, &publicJSON,
		)
	})
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRememberAttemptNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(publicJSON) == 0 {
		attempt.PublicResult = map[string]any{}
		return &attempt, nil
	}
	if err := json.Unmarshal(publicJSON, &attempt.PublicResult); err != nil {
		return nil, fmt.Errorf("remember attempt: decode public result: %w", err)
	}
	return &attempt, nil
}

func loadCanonicalRememberAttemptByKeyInTx(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, idempotencyKey string) (*RememberAttempt, error) {
	var attempt RememberAttempt
	var publicJSON []byte
	err := tx.WithContext(ctx).Raw(`
		SELECT attempt_id::text, request_hash, contract_version, outcome, public_result
		FROM remember_attempts
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?
		  AND outcome IN ('completed', 'rejected', 'quarantined', 'replayed')
		ORDER BY created_at DESC, attempt_id DESC
		LIMIT 1
	`, teamID, ownerProfileID, idempotencyKey).Row().Scan(
		&attempt.AttemptID, &attempt.RequestHash, &attempt.ContractVersion, &attempt.Outcome, &publicJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(publicJSON) == 0 {
		attempt.PublicResult = map[string]any{}
	} else if err := json.Unmarshal(publicJSON, &attempt.PublicResult); err != nil {
		return nil, fmt.Errorf("remember attempt: decode public result: %w", err)
	}
	return &attempt, nil
}

func (r *LedgerRepositoryImpl) RecordRememberAttempt(ctx context.Context, input RememberAttemptRecordInput) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.SubmissionKind = strings.TrimSpace(input.SubmissionKind)
	input.Outcome = strings.TrimSpace(input.Outcome)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("remember attempt: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("remember attempt: owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.AttemptID); err != nil {
		return fmt.Errorf("remember attempt: attempt_id is required: %w", err)
	}
	if input.IdempotencyKey == "" || input.RequestHash == "" || input.ContractVersion == "" || input.SubmissionKind == "" {
		return errors.New("remember attempt: identity fields are required")
	}
	if input.Outcome != "completed" && input.Outcome != "rejected" && input.Outcome != "quarantined" && input.Outcome != "failed" && input.Outcome != "replayed" {
		return fmt.Errorf("remember attempt: unsupported outcome %q", input.Outcome)
	}
	if input.PublicResult == nil {
		input.PublicResult = map[string]any{}
	}
	return r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		return insertRememberAttemptInTx(ctx, tx, input)
	})
}

// insertRememberAttemptInTx is shared by terminal synchronous Remember
// transactions and failure recording. The caller owns the enclosing
// transaction, so attempt and event rows commit with the relevant state.
func insertRememberAttemptInTx(ctx context.Context, tx *gorm.DB, input RememberAttemptRecordInput) error {
	if err := lockRememberIdempotencyKeyInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey); err != nil {
		return err
	}
	publicJSON, err := json.Marshal(input.PublicResult)
	if err != nil {
		return fmt.Errorf("remember attempt: public result: %w", err)
	}
	completedAt := time.Now().UTC()
	result := tx.WithContext(ctx).Exec(`
			INSERT INTO remember_attempts (
			    team_id, attempt_id, owner_profile_id, space_id, space_generation,
			    idempotency_key, request_hash, contract_version, submission_kind,
			    outcome, failed_phase, error_code, correlation_id, public_result,
			    canonical_attempt_id, evidence_count, relationship_count,
			    document_count, assessor_turns, duration_ms, completed_at
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?, 0),
			    ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?::uuid, ?, ?, ?, ?, ?, ?
			)
			ON CONFLICT DO NOTHING
		`, input.TeamID, input.AttemptID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
		input.IdempotencyKey, input.RequestHash, input.ContractVersion, input.SubmissionKind,
		input.Outcome, input.FailedPhase, input.ErrorCode, input.CorrelationID, string(publicJSON), input.AttemptID,
		input.EvidenceCount, input.RelationshipCount, input.DocumentCount, input.AssessorTurns,
		input.Duration.Milliseconds(), completedAt)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrRememberReplay
	}
	for _, event := range rememberAttemptEventDrafts(input) {
		metadataJSON, err := json.Marshal(event.Metadata)
		if err != nil {
			return fmt.Errorf("remember attempt: event metadata: %w", err)
		}
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO remember_attempt_events (
			    team_id, event_id, attempt_id, owner_profile_id, sequence_no,
			    phase, event_kind, outcome, metadata, created_at
			)
			SELECT ?::uuid, gen_random_uuid(), ?::uuid, ?::uuid,
			       COALESCE(max(sequence_no), 0) + 1, ?, ?, ?, ?::jsonb, now()
			FROM remember_attempt_events
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid
		`, input.TeamID, input.AttemptID, input.OwnerProfileID,
			event.Phase, event.EventKind, event.Outcome, string(metadataJSON),
			input.TeamID, input.AttemptID).Error; err != nil {
			return err
		}
	}
	return nil
}

type rememberAttemptEventDraft struct {
	Phase     string
	EventKind string
	Outcome   string
	Metadata  map[string]any
}

func rememberAttemptEventDrafts(input RememberAttemptRecordInput) []rememberAttemptEventDraft {
	metadata := map[string]any{
		"contract_version": input.ContractVersion,
		"assessor_turns":   input.AssessorTurns,
		"document_count":   input.DocumentCount,
	}
	if input.ErrorCode != "" {
		metadata["error_code"] = input.ErrorCode
	}
	switch input.Outcome {
	case "completed":
		return []rememberAttemptEventDraft{
			{Phase: "request", EventKind: "request_validated", Outcome: input.Outcome, Metadata: metadata},
			{Phase: "assessment", EventKind: "assessment_validated", Outcome: input.Outcome, Metadata: metadata},
			{Phase: "embedding", EventKind: "embedding_batch_completed", Outcome: input.Outcome, Metadata: metadata},
			{Phase: "commit", EventKind: "commit_completed", Outcome: input.Outcome, Metadata: metadata},
		}
	case "rejected", "quarantined":
		return []rememberAttemptEventDraft{
			{Phase: "request", EventKind: "request_validated", Outcome: input.Outcome, Metadata: metadata},
			{Phase: "assessment", EventKind: "assessment_" + input.Outcome, Outcome: input.Outcome, Metadata: metadata},
			{Phase: "commit", EventKind: "commit_completed", Outcome: input.Outcome, Metadata: metadata},
		}
	case "failed":
		phase := strings.TrimSpace(input.FailedPhase)
		if phase == "" {
			phase = "execution"
		}
		return []rememberAttemptEventDraft{{
			Phase: phase, EventKind: phase + "_failed", Outcome: input.Outcome, Metadata: metadata,
		}}
	case "replayed":
		return []rememberAttemptEventDraft{{
			Phase: "request", EventKind: "idempotency_replayed", Outcome: input.Outcome, Metadata: metadata,
		}}
	default:
		return []rememberAttemptEventDraft{{
			Phase: "execution", EventKind: "terminal_outcome", Outcome: input.Outcome, Metadata: metadata,
		}}
	}
}
