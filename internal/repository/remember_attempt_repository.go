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
	AttemptID    string
	RequestHash  string
	Outcome      string
	PublicResult map[string]any
}

type RememberAttemptLookup interface {
	LoadRememberAttempt(context.Context, RememberAttemptLookupInput) (*RememberAttempt, error)
}

type RememberAttemptLookupInput struct {
	TeamID         string
	OwnerProfileID string
	IdempotencyKey string
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
			SELECT attempt_id::text, request_hash, outcome, public_result
			FROM remember_attempts
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
			  AND idempotency_key = ?
			ORDER BY created_at DESC, attempt_id DESC
			LIMIT 1
		`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Row().Scan(
			&attempt.AttemptID, &attempt.RequestHash, &attempt.Outcome, &publicJSON,
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
	publicJSON, err := json.Marshal(input.PublicResult)
	if err != nil {
		return fmt.Errorf("remember attempt: public result: %w", err)
	}
	return r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
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
			ON CONFLICT (team_id, attempt_id) DO NOTHING
		`, input.TeamID, input.AttemptID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
			input.IdempotencyKey, input.RequestHash, input.ContractVersion, input.SubmissionKind,
			input.Outcome, input.FailedPhase, input.ErrorCode, input.CorrelationID, string(publicJSON), input.AttemptID,
			input.EvidenceCount, input.RelationshipCount, input.DocumentCount, input.AssessorTurns,
			input.Duration.Milliseconds(), completedAt)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO remember_attempt_events (
			    team_id, event_id, attempt_id, owner_profile_id, sequence_no,
			    phase, event_kind, outcome, metadata, created_at
			)
			SELECT ?::uuid, gen_random_uuid(), ?::uuid, ?::uuid,
			       COALESCE(max(sequence_no), 0) + 1, 'remember', ?, ?,
			       jsonb_build_object('contract_version', ?), now()
			FROM remember_attempt_events
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid
		`, input.TeamID, input.AttemptID, input.OwnerProfileID, input.Outcome, input.Outcome,
			input.ContractVersion, input.TeamID, input.AttemptID).Error; err != nil {
			return err
		}
		return nil
	})
}
