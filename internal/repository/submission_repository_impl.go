package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const submissionMaxEvidenceItems = 20

var _ SubmissionRepository = (*LedgerRepositoryImpl)(nil)

func (r *LedgerRepositoryImpl) CreateSubmission(ctx context.Context, input CreateSubmissionInput) (*Submission, error) {
	input = normalizeCreateSubmissionInput(input)
	if err := validateCreateSubmissionInput(input); err != nil {
		return nil, err
	}
	var result *Submission
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		if input.ReplacesQuarantinedSubmissionID != "" {
			if err := lockReplaceableQuarantinedSubmission(ctx, tx, input); err != nil {
				return err
			}
		}
		submissionID, created, err := insertSubmissionRun(ctx, tx, input)
		if err != nil {
			return err
		}
		if !created {
			loaded, err := loadSubmission(ctx, tx, input.TeamID, input.OwnerProfileID, submissionID, true)
			if err != nil {
				return err
			}
			result = loaded
			return nil
		}
		if err := insertSubmissionProposal(ctx, tx, input.TeamID, input.OwnerProfileID, submissionID, input.Proposal); err != nil {
			return err
		}
		for index, evidence := range input.Evidence {
			if err := insertStagedSubmissionEvidence(ctx, tx, input.TeamID, input.OwnerProfileID, submissionID, index, evidence); err != nil {
				return err
			}
		}
		loaded, err := loadSubmission(ctx, tx, input.TeamID, input.OwnerProfileID, submissionID, true)
		if err != nil {
			return err
		}
		result = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("submission create: %w", err)
	}
	return result, nil
}

func (r *LedgerRepositoryImpl) GetSubmissionStatus(ctx context.Context, input GetSubmissionStatusInput) (*SubmissionStatus, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	if err := validateSubmissionIdentity(input.TeamID, input.OwnerProfileID, input.SubmissionID); err != nil {
		return nil, err
	}
	var result *SubmissionStatus
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		loaded, err := loadSubmissionStatus(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSubmissionNotFound
		}
		if err != nil {
			return err
		}
		result = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("submission status: %w", err)
	}
	return result, nil
}

func (r *LedgerRepositoryImpl) ClaimNextSubmission(ctx context.Context, teamID, workerID string, lease time.Duration) (*SubmissionClaim, error) {
	teamID = strings.TrimSpace(teamID)
	workerID = strings.TrimSpace(workerID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if workerID == "" || lease < time.Second {
		return nil, errors.New("submission worker and lease are required")
	}
	var claim *SubmissionClaim
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH ready AS MATERIALIZED (
				SELECT submission_id
				FROM submission_runs
				WHERE team_id = ?::uuid
				  AND status = 'queued'
				  AND attempts < max_attempts
				  AND available_at <= clock_timestamp()
				ORDER BY available_at, created_at, submission_id
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			), expired AS MATERIALIZED (
				SELECT submission_id
				FROM submission_runs
				WHERE team_id = ?::uuid
				  AND status = 'processing'
				  AND attempts < max_attempts
				  AND lease_until < clock_timestamp()
				  AND NOT EXISTS (SELECT 1 FROM ready)
				ORDER BY lease_until, created_at, submission_id
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			), next AS (
				SELECT submission_id FROM ready
				UNION ALL
				SELECT submission_id FROM expired
				LIMIT 1
			)
			UPDATE submission_runs AS submission
			SET status = 'processing',
				attempts = attempts + 1,
				worker_id = ?,
				lease_until = clock_timestamp() + (? * interval '1 second'),
				updated_at = clock_timestamp()
			FROM next
			WHERE submission.team_id = ?::uuid
			  AND submission.submission_id = next.submission_id
			RETURNING submission.team_id::text, submission.submission_id::text,
			          submission.owner_profile_id::text, submission.attempts,
			          submission.max_attempts, submission.created_at, submission.lease_until
		`, teamID, teamID, workerID, int(lease.Seconds()), teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		loaded := SubmissionClaim{}
		var leaseUntil sql.NullTime
		if err := rows.Scan(&loaded.TeamID, &loaded.SubmissionID, &loaded.OwnerProfileID, &loaded.Attempts, &loaded.MaxAttempts, &loaded.CreatedAt, &leaseUntil); err != nil {
			return err
		}
		if leaseUntil.Valid {
			loaded.LeaseUntil = &leaseUntil.Time
		}
		claim = &loaded
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("submission claim: %w", err)
	}
	return claim, nil
}

func (r *LedgerRepositoryImpl) LoadClaimedSubmission(ctx context.Context, input LoadClaimedSubmissionInput) (*Submission, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if err := validateSubmissionIdentity(input.TeamID, input.OwnerProfileID, input.SubmissionID); err != nil {
		return nil, err
	}
	if input.WorkerID == "" || input.Attempts < 1 {
		return nil, errors.New("submission claim is required")
	}
	var result *Submission
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		var found int
		if err := tx.WithContext(ctx).Raw(`
			SELECT 1 FROM submission_runs
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
			  AND status = 'processing' AND worker_id = ? AND attempts = ? AND lease_until > clock_timestamp()
		`, input.TeamID, input.OwnerProfileID, input.SubmissionID, input.WorkerID, input.Attempts).Scan(&found).Error; err != nil {
			return err
		}
		if found != 1 {
			return ErrSubmissionLeaseConflict
		}
		loaded, err := loadSubmission(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, true)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSubmissionNotFound
		}
		if err != nil {
			return err
		}
		result = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("submission load claimed: %w", err)
	}
	return result, nil
}

func (r *LedgerRepositoryImpl) LoadSubmissionAssessment(ctx context.Context, input LoadSubmissionAssessmentInput) (*SubmissionAssessment, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	if err := validateSubmissionIdentity(input.TeamID, input.OwnerProfileID, input.SubmissionID); err != nil {
		return nil, err
	}
	var result *SubmissionAssessment
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		loaded, err := loadSubmissionAssessment(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSubmissionAssessmentNotFound
		}
		if err != nil {
			return err
		}
		result = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("submission assessment load: %w", err)
	}
	return result, nil
}

func (r *LedgerRepositoryImpl) PersistSubmissionAssessment(ctx context.Context, input PersistSubmissionAssessmentInput) (*SubmissionAssessment, bool, error) {
	input = normalizePersistSubmissionAssessmentInput(input)
	if err := validatePersistSubmissionAssessmentInput(input); err != nil {
		return nil, false, err
	}
	var result *SubmissionAssessment
	existing := false
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := requireSubmissionLease(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, input.WorkerID, input.ExpectedAttempts); err != nil {
			return err
		}
		payload := string(input.NormalizedResponse)
		rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO submission_assessments (
				team_id, submission_id, owner_profile_id, request_id, model, tokenizer,
				input_tokens, output_tokens, candidate_context_tokens, candidate_context_truncated,
				normalized_response, response_hash, validated_at
			) VALUES (
				?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?
			)
			ON CONFLICT (team_id, submission_id) DO NOTHING
			RETURNING assessment_id::text, submission_id::text, request_id, model, tokenizer,
			          input_tokens, output_tokens, candidate_context_tokens, candidate_context_truncated,
			          normalized_response, response_hash, validated_at
		`, input.TeamID, input.SubmissionID, input.OwnerProfileID, input.RequestID, input.Model, input.Tokenizer,
			input.InputTokens, input.OutputTokens, input.CandidateContextTokens, input.CandidateContextTruncated,
			payload, input.ResponseHash, input.ValidatedAt).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		loaded, scanErr := scanSubmissionAssessment(rows)
		if scanErr == nil {
			result = loaded
			return nil
		}
		if !errors.Is(scanErr, sql.ErrNoRows) {
			return scanErr
		}
		loaded, err = loadSubmissionAssessment(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID)
		if err != nil {
			return err
		}
		existing = true
		result = loaded
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("submission assessment persist: %w", err)
	}
	return result, existing, nil
}

func (r *LedgerRepositoryImpl) RequeueSubmission(ctx context.Context, input RequeueSubmissionInput) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.ReasonCode = strings.TrimSpace(input.ReasonCode)
	if err := validateSubmissionIdentity(input.TeamID, input.OwnerProfileID, input.SubmissionID); err != nil {
		return err
	}
	if input.WorkerID == "" || input.ExpectedAttempts < 1 {
		return errors.New("submission lease is required")
	}
	if input.RetryAfter < 0 {
		input.RetryAfter = 0
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE submission_runs
			SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
				available_at = CASE WHEN attempts >= max_attempts THEN available_at ELSE clock_timestamp() + (? * interval '1 second') END,
				lease_until = NULL, worker_id = '', error_code = ?,
				completed_at = CASE WHEN attempts >= max_attempts THEN clock_timestamp() ELSE NULL END,
				updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
			  AND status = 'processing' AND worker_id = ? AND attempts = ? AND lease_until > clock_timestamp()
		`, int(input.RetryAfter.Seconds()), input.ReasonCode, input.TeamID, input.OwnerProfileID,
			input.SubmissionID, input.WorkerID, input.ExpectedAttempts)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSubmissionLeaseConflict
		}
		if err := insertSubmissionOutcome(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, nil, "", "retry", input.ReasonCode, nil); err != nil {
			return err
		}
		var status string
		if err := tx.WithContext(ctx).Raw(`
			SELECT status
			FROM submission_runs
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
		`, input.TeamID, input.OwnerProfileID, input.SubmissionID).Row().Scan(&status); err != nil {
			return err
		}
		if status == string(domain.SubmissionFailed) {
			return deleteSubmissionStage(ctx, tx, input.TeamID, input.SubmissionID)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("submission requeue: %w", err)
	}
	return nil
}

func (r *LedgerRepositoryImpl) CompleteSubmission(ctx context.Context, input CompleteSubmissionInput) error {
	input = normalizeCompleteSubmissionInput(input)
	if err := validateCompleteSubmissionInput(input); err != nil {
		return err
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := requireSubmissionLease(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, input.WorkerID, input.ExpectedAttempts); err != nil {
			return err
		}
		if err := insertSubmissionCompletionOutcomes(ctx, tx, input); err != nil {
			return err
		}
		result := tx.WithContext(ctx).Exec(`
			UPDATE submission_runs
			SET status = ?, error_code = ?, canonical_ingest_id = NULLIF(?, '')::uuid,
				lease_until = NULL, worker_id = '', completed_at = clock_timestamp(), updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
			  AND status = 'processing' AND attempts = ?
		`, input.Status, input.ReasonCode, input.CanonicalIngestID, input.TeamID, input.OwnerProfileID, input.SubmissionID, input.ExpectedAttempts)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSubmissionLeaseConflict
		}
		if err := deleteSubmissionStage(ctx, tx, input.TeamID, input.SubmissionID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("submission complete: %w", err)
	}
	return nil
}

func (r *LedgerRepositoryImpl) QuarantineSubmission(ctx context.Context, input QuarantineSubmissionInput) error {
	input = normalizeQuarantineSubmissionInput(input)
	if err := validateQuarantineSubmissionInput(input); err != nil {
		return err
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := requireSubmissionLease(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, input.WorkerID, input.ExpectedAttempts); err != nil {
			return err
		}
		for _, outcome := range input.EvidenceOutcomes {
			index := outcome.EvidenceIndex
			if err := insertSubmissionOutcome(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, &index, "", outcome.Status, outcome.ReasonCode, map[string]any{"search_state": outcome.SearchState}); err != nil {
				return err
			}
		}
		result := tx.WithContext(ctx).Exec(`
			UPDATE submission_runs AS submission
			SET status = 'quarantined', error_code = ?,
				quarantine_expires_at = quarantine_time.at + interval '24 hours',
				lease_until = NULL, worker_id = '', completed_at = quarantine_time.at,
				updated_at = quarantine_time.at
			FROM (SELECT clock_timestamp() AS at) AS quarantine_time
			WHERE submission.team_id = ?::uuid AND submission.owner_profile_id = ?::uuid
			  AND submission.submission_id = ?::uuid AND submission.status = 'processing'
			  AND submission.attempts = ?
		`, input.ReasonCode, input.TeamID, input.OwnerProfileID, input.SubmissionID, input.ExpectedAttempts)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSubmissionLeaseConflict
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("submission quarantine: %w", err)
	}
	return nil
}

func (r *LedgerRepositoryImpl) CleanupExpiredSubmissions(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var processed int64
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		type expiredSubmission struct {
			teamID       string
			ownerID      string
			submissionID string
		}
		rows, err := tx.WithContext(ctx).Raw(`
			WITH expired AS MATERIALIZED (
				SELECT team_id, owner_profile_id, submission_id
				FROM submission_runs
				WHERE status = 'processing'
				  AND attempts >= max_attempts
				  AND lease_until <= ?
				ORDER BY lease_until, submission_id
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			)
			UPDATE submission_runs AS submission
			SET status = 'failed', lease_until = NULL, worker_id = '',
				error_code = 'lease_expired_final_attempt', completed_at = ?, updated_at = ?
			FROM expired
			WHERE submission.team_id = expired.team_id
			  AND submission.submission_id = expired.submission_id
			RETURNING submission.team_id::text, submission.owner_profile_id::text, submission.submission_id::text
		`, now, limit, now, now).Rows()
		if err != nil {
			return err
		}
		finalized := make([]expiredSubmission, 0, limit)
		for rows.Next() {
			item := expiredSubmission{}
			if err := rows.Scan(&item.teamID, &item.ownerID, &item.submissionID); err != nil {
				_ = rows.Close()
				return err
			}
			finalized = append(finalized, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, item := range finalized {
			if err := insertSubmissionOutcome(ctx, tx, item.teamID, item.ownerID, item.submissionID, nil, "", "failed", "lease_expired_final_attempt", map[string]any{"search_state": string(domain.SearchProjectionNotRequired)}); err != nil {
				return err
			}
			if err := deleteSubmissionStage(ctx, tx, item.teamID, item.submissionID); err != nil {
				return err
			}
		}
		processed += int64(len(finalized))
		remaining := limit - len(finalized)
		if remaining == 0 {
			return nil
		}
		rows, err = tx.WithContext(ctx).Raw(`
			WITH expired AS MATERIALIZED (
				SELECT team_id, submission_id
				FROM submission_runs
				WHERE status = 'quarantined' AND quarantine_expires_at <= ?
				ORDER BY quarantine_expires_at, submission_id
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			)
			DELETE FROM submission_runs AS submission
			USING expired
			WHERE submission.team_id = expired.team_id AND submission.submission_id = expired.submission_id
			RETURNING 1
		`, now, remaining).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			processed++
		}
		return rows.Err()
	})
	if err != nil {
		return 0, fmt.Errorf("submission cleanup: %w", err)
	}
	return processed, nil
}

func normalizeCreateSubmissionInput(input CreateSubmissionInput) CreateSubmissionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.ActorCredentialID = strings.TrimSpace(input.ActorCredentialID)
	input.ActorAuthMethod = strings.TrimSpace(input.ActorAuthMethod)
	input.ActorRole = strings.TrimSpace(input.ActorRole)
	input.ActorScopes = normalizeSubmissionActorScopes(input.ActorScopes)
	input.CorrelationID = strings.TrimSpace(input.CorrelationID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	input.SourceSummary = strings.TrimSpace(input.SourceSummary)
	input.ReplacesQuarantinedSubmissionID = strings.TrimSpace(input.ReplacesQuarantinedSubmissionID)
	if input.Proposal == nil {
		input.Proposal = map[string]any{}
	}
	for index := range input.Evidence {
		item := &input.Evidence[index]
		item.SourceType = strings.TrimSpace(item.SourceType)
		item.Source = strings.TrimSpace(item.Source)
		item.SourceGroup = strings.TrimSpace(item.SourceGroup)
		item.Authority = strings.TrimSpace(item.Authority)
		item.SourceKey = strings.TrimSpace(item.SourceKey)
		item.SourceRevision = strings.TrimSpace(item.SourceRevision)
		item.PreviousSourceRevision = strings.TrimSpace(item.PreviousSourceRevision)
		item.IdempotencyKey = strings.TrimSpace(item.IdempotencyKey)
		if item.SourceType == "" {
			item.SourceType = "conversation"
		}
		if item.Authority == "" {
			item.Authority = string(domain.AuthorityPrimary)
		}
		if item.Metadata == nil {
			item.Metadata = map[string]any{}
		}
	}
	return input
}

func validateCreateSubmissionInput(input CreateSubmissionInput) error {
	if err := validateSubmissionIdentity(input.TeamID, input.OwnerProfileID, uuid.Nil.String()); err != nil {
		return err
	}
	if input.ActorCredentialID != "" {
		credentialID, err := uuid.Parse(input.ActorCredentialID)
		if err != nil || credentialID == uuid.Nil {
			return errors.New("actor_credential_id is invalid")
		}
		if input.ActorAuthMethod == "" || input.ActorRole == "" {
			return errors.New("actor credential provenance is incomplete")
		}
	} else if input.ActorAuthMethod != "" || input.ActorRole != "" || len(input.ActorScopes) > 0 {
		return errors.New("actor credential provenance requires actor_credential_id")
	}
	if len(input.ActorAuthMethod) > 64 || len(input.ActorRole) > 64 || len(input.CorrelationID) > 512 {
		return errors.New("submission actor provenance exceeds bounds")
	}
	if len(input.ActorScopes) > 64 {
		return errors.New("submission actor scopes exceed bounds")
	}
	for _, scope := range input.ActorScopes {
		if len(scope) > 64 {
			return errors.New("submission actor scope exceeds bounds")
		}
	}
	if input.IdempotencyKey != "" && input.RequestHash == "" {
		return errors.New("request_hash is required when idempotency_key is set")
	}
	if len(input.Evidence) == 0 || len(input.Evidence) > submissionMaxEvidenceItems {
		return fmt.Errorf("submission evidence must contain between 1 and %d items", submissionMaxEvidenceItems)
	}
	if input.ReplacesQuarantinedSubmissionID != "" {
		if _, err := uuid.Parse(input.ReplacesQuarantinedSubmissionID); err != nil {
			return fmt.Errorf("replaces_quarantined_submission_id is invalid: %w", err)
		}
	}
	for index, item := range input.Evidence {
		if strings.TrimSpace(item.Content) == "" {
			return fmt.Errorf("evidence[%d].content is required", index)
		}
		if !contains([]string{"conversation", "document", "observation", "manual"}, item.SourceType) {
			return fmt.Errorf("evidence[%d].source_type is unsupported", index)
		}
		if !domain.Authority(item.Authority).IsValid() {
			return fmt.Errorf("evidence[%d].authority is unsupported", index)
		}
		if (item.SourceKey == "") != (item.SourceRevision == "") {
			return fmt.Errorf("evidence[%d].source_key and source_revision must appear together", index)
		}
	}
	return nil
}

func validateSubmissionIdentity(teamID, ownerID, submissionID string) error {
	if _, err := uuid.Parse(teamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(ownerID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if submissionID != uuid.Nil.String() {
		if _, err := uuid.Parse(submissionID); err != nil {
			return fmt.Errorf("submission_id is required: %w", err)
		}
	}
	return nil
}
