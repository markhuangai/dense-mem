package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const AssessmentPolicyVersion = "v2.4.confidence-gate.1"

var (
	ErrPlacementAssessmentNotFound      = errors.New("placement assessment not found")
	ErrPlacementAssessmentClaimMismatch = errors.New("placement assessment claim key mismatch")
)

// PlacementAssessmentRepository is the append-once boundary between one
// assessor conversation and deterministic semantic policy. It deliberately
// stores a normalized response rather than provider transport data.
type PlacementAssessmentRepository interface {
	LoadPlacementAssessment(ctx context.Context, input LoadPlacementAssessmentInput) (*PlacementAssessment, error)
	ReservePlacementAssessmentProviderAttempt(ctx context.Context, input ReservePlacementAssessmentProviderAttemptInput) (bool, error)
	PersistPlacementAssessment(ctx context.Context, input PersistPlacementAssessmentInput) (*PlacementAssessment, bool, error)
	LoadAutoWriteConfidencePolicy(ctx context.Context, input LoadAutoWriteConfidencePolicyInput) (AutoWriteConfidencePolicy, error)
	LoadPlacementAssessmentReviewOverrides(ctx context.Context, input LoadPlacementAssessmentReviewOverridesInput) (PlacementAssessmentReviewOverrides, error)
	ExpirePlacementAssessmentReviews(ctx context.Context, input ExpirePlacementAssessmentReviewsInput) (int64, error)
}

type LoadPlacementAssessmentInput struct {
	TeamID          string
	OwnerProfileID  string
	PlacementItemID string
}

// ReservePlacementAssessmentProviderAttempt reserves the assessor conversation
// for the active placement claim while the caller owns its lease. A known
// provider failure is released only with its requeue transaction.
type ReservePlacementAssessmentProviderAttemptInput struct {
	TeamID           string
	OwnerProfileID   string
	PlacementRunID   string
	PlacementItemID  string
	WorkerID         string
	ExpectedAttempts int
}

type PersistPlacementAssessmentInput struct {
	TeamID                    string
	OwnerProfileID            string
	PlacementItemID           string
	ClaimKey                  string
	RequestID                 string
	AssessorContractVersion   string
	Model                     string
	Tokenizer                 string
	InputTokens               int
	OutputTokens              int
	CandidateContextTokens    int
	CandidateContextTruncated bool
	NormalizedResponse        json.RawMessage
	ResponseHash              string
	ValidatedAt               time.Time
}

type PlacementAssessment struct {
	TeamID                    string
	AssessmentID              string
	OwnerProfileID            string
	PlacementItemID           string
	ClaimKey                  string
	RequestID                 string
	AssessorContractVersion   string
	Model                     string
	Tokenizer                 string
	InputTokens               int
	OutputTokens              int
	CandidateContextTokens    int
	CandidateContextTruncated bool
	NormalizedResponse        json.RawMessage
	ResponseHash              string
	ValidatedAt               time.Time
	CreatedAt                 time.Time
}

type LoadAutoWriteConfidencePolicyInput struct {
	TeamID          string
	OwnerProfileID  string
	GlobalThreshold float64
}

type AutoWriteConfidencePolicy struct {
	Threshold     float64
	Source        string
	ConfigVersion int64
	Version       string
}

type LoadPlacementAssessmentReviewOverridesInput struct {
	TeamID          string
	OwnerProfileID  string
	PlacementItemID string
	AssessmentID    string
}

type PlacementAssessmentPredicateOverride struct {
	RelationshipRef  string
	PredicateKey     string
	PredicateVersion int
}

type PlacementAssessmentReviewOverrides struct {
	EntitySelections    map[string]string
	PredicateSelections map[string]PlacementAssessmentPredicateOverride
}

type ExpirePlacementAssessmentReviewsInput struct {
	TeamID string
	Now    time.Time
}

var _ PlacementAssessmentRepository = (*LedgerRepositoryImpl)(nil)

func (r *LedgerRepositoryImpl) LoadPlacementAssessment(
	ctx context.Context,
	input LoadPlacementAssessmentInput,
) (*PlacementAssessment, error) {
	input = normalizeLoadPlacementAssessmentInput(input)
	if err := validateLoadPlacementAssessmentInput(input); err != nil {
		return nil, err
	}
	var assessment *PlacementAssessment
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		loaded, err := loadPlacementAssessment(ctx, tx, input.TeamID, input.OwnerProfileID, input.PlacementItemID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPlacementAssessmentNotFound
		}
		if err != nil {
			return err
		}
		assessment = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("placement assessment load: %w", err)
	}
	return assessment, nil
}

func (r *LedgerRepositoryImpl) ReservePlacementAssessmentProviderAttempt(
	ctx context.Context,
	input ReservePlacementAssessmentProviderAttemptInput,
) (bool, error) {
	input = normalizeReservePlacementAssessmentProviderAttemptInput(input)
	if err := validateReservePlacementAssessmentProviderAttemptInput(input); err != nil {
		return false, err
	}
	reserved := false
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE placement_items AS item
			SET assessor_attempt_id = gen_random_uuid(),
			    assessor_attempted_at = now(),
			    updated_at = now()
			FROM placement_runs AS run
			WHERE item.team_id = ?::uuid
			  AND item.owner_profile_id = ?::uuid
			  AND item.placement_item_id = ?::uuid
			  AND item.placement_run_id = ?::uuid
			  AND item.assessor_attempt_id IS NULL
			  AND run.team_id = item.team_id
			  AND run.placement_run_id = item.placement_run_id
			  AND run.owner_profile_id = item.owner_profile_id
			  AND run.status = 'processing'
			  AND run.worker_id = ?
			  AND run.attempts = ?
			  AND run.lease_until > clock_timestamp()
		`, input.TeamID, input.OwnerProfileID, input.PlacementItemID, input.PlacementRunID,
			input.WorkerID, input.ExpectedAttempts)
		if result.Error != nil {
			return result.Error
		}
		reserved = result.RowsAffected == 1
		if reserved {
			return nil
		}

		var consumed bool
		if err := tx.WithContext(ctx).Raw(`
			SELECT assessor_attempt_id IS NOT NULL
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND placement_item_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, input.TeamID, input.OwnerProfileID, input.PlacementItemID, input.PlacementRunID).Row().Scan(&consumed); err != nil {
			return err
		}
		if consumed {
			return nil
		}
		return ErrPlacementLeaseLost
	})
	if err != nil {
		return false, fmt.Errorf("reserve placement assessment provider attempt: %w", err)
	}
	return reserved, nil
}

func releasePlacementAssessmentProviderAttempt(
	ctx context.Context,
	tx *gorm.DB,
	input CommitPlacementSemanticInput,
) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE placement_items
		SET assessor_attempt_id = NULL,
		    assessor_attempted_at = NULL,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND placement_item_id = ?::uuid
		  AND assessor_attempt_id IS NOT NULL
	`, input.TeamID, input.OwnerProfileID, input.PlacementRunID, input.PlacementItemID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrPlacementLeaseLost
	}
	return nil
}

// PersistPlacementAssessment inserts only the first valid normalized response.
// The bool is true when an existing row won the uniqueness race.
func (r *LedgerRepositoryImpl) PersistPlacementAssessment(
	ctx context.Context,
	input PersistPlacementAssessmentInput,
) (*PlacementAssessment, bool, error) {
	input = normalizePersistPlacementAssessmentInput(input)
	if err := validatePersistPlacementAssessmentInput(input); err != nil {
		return nil, false, err
	}
	var assessment *PlacementAssessment
	existing := false
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		inserted, err := insertPlacementAssessment(ctx, tx, input)
		if err != nil {
			if isPostgresForeignKeyConstraint(err, "placement_assessments_claim_ref") {
				return ErrPlacementAssessmentClaimMismatch
			}
			return err
		}
		if inserted != nil {
			assessment = inserted
			return nil
		}
		existing = true
		loaded, err := loadPlacementAssessment(ctx, tx, input.TeamID, input.OwnerProfileID, input.PlacementItemID)
		if errors.Is(err, sql.ErrNoRows) {
			claimExists, claimErr := placementAssessmentClaimKeyExists(ctx, tx, input.TeamID, input.ClaimKey)
			if claimErr != nil {
				return claimErr
			}
			if claimExists {
				return ErrPlacementAssessmentClaimMismatch
			}
			return ErrPlacementAssessmentNotFound
		}
		if err != nil {
			return err
		}
		if loaded.ClaimKey != input.ClaimKey {
			return ErrPlacementAssessmentClaimMismatch
		}
		assessment = loaded
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("placement assessment persist: %w", err)
	}
	return assessment, existing, nil
}

func (r *LedgerRepositoryImpl) LoadAutoWriteConfidencePolicy(
	ctx context.Context,
	input LoadAutoWriteConfidencePolicyInput,
) (AutoWriteConfidencePolicy, error) {
	input = normalizeLoadAutoWriteConfidencePolicyInput(input)
	if err := validateLoadAutoWriteConfidencePolicyInput(input); err != nil {
		return AutoWriteConfidencePolicy{}, err
	}
	policy := AutoWriteConfidencePolicy{
		Threshold: input.GlobalThreshold,
		Source:    "global",
		Version:   AssessmentPolicyVersion,
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		var configVersion int64
		var thresholdRaw []byte
		err := tx.WithContext(ctx).Raw(`
			SELECT config_version,
			       config #> '{memory_write,auto_write_confidence_threshold}'
			FROM teams
			WHERE id = ?::uuid
		`, input.TeamID).Row().Scan(&configVersion, &thresholdRaw)
		if err != nil {
			return err
		}
		policy.ConfigVersion = configVersion
		if len(thresholdRaw) == 0 || string(thresholdRaw) == "null" {
			return nil
		}
		threshold, err := confidenceThresholdFromJSON(thresholdRaw)
		if err != nil {
			return fmt.Errorf("memory_write.auto_write_confidence_threshold: %w", err)
		}
		policy.Threshold = threshold
		policy.Source = "team"
		return nil
	})
	if err != nil {
		return AutoWriteConfidencePolicy{}, fmt.Errorf("load auto-write confidence policy: %w", err)
	}
	return policy, nil
}

func (r *LedgerRepositoryImpl) LoadPlacementAssessmentReviewOverrides(
	ctx context.Context,
	input LoadPlacementAssessmentReviewOverridesInput,
) (PlacementAssessmentReviewOverrides, error) {
	input = normalizeLoadPlacementAssessmentReviewOverridesInput(input)
	if err := validateLoadPlacementAssessmentReviewOverridesInput(input); err != nil {
		return PlacementAssessmentReviewOverrides{}, err
	}
	overrides := PlacementAssessmentReviewOverrides{
		EntitySelections:    map[string]string{},
		PredicateSelections: map[string]PlacementAssessmentPredicateOverride{},
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT COALESCE(payload->>'semantic_kind', ''),
			       COALESCE(payload->>'relationship_ref', ''),
			       resolution
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND placement_item_id = ?::uuid
			  AND assessment_id = ?::uuid
			  AND status = 'resolved'
			  AND resolution IS NOT NULL
			ORDER BY updated_at ASC, review_task_id ASC
		`, input.TeamID, input.OwnerProfileID, input.PlacementItemID, input.AssessmentID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, relationshipRef string
			var resolutionRaw []byte
			if err := rows.Scan(&kind, &relationshipRef, &resolutionRaw); err != nil {
				return err
			}
			resolution := map[string]any{}
			if err := json.Unmarshal(resolutionRaw, &resolution); err != nil {
				return fmt.Errorf("invalid assessment review resolution: %w", err)
			}
			switch kind {
			case "identity":
				ref, _ := resolution["entity_ref"].(string)
				candidateID, _ := resolution["candidate_entity_id"].(string)
				if strings.TrimSpace(ref) != "" && strings.TrimSpace(candidateID) != "" {
					overrides.EntitySelections[ref] = candidateID
				}
			case "predicate":
				predicateKey, _ := resolution["predicate_key"].(string)
				version := reviewOverrideInt(resolution["predicate_version"])
				if strings.TrimSpace(relationshipRef) != "" && strings.TrimSpace(predicateKey) != "" && version > 0 {
					overrides.PredicateSelections[relationshipRef] = PlacementAssessmentPredicateOverride{
						RelationshipRef:  relationshipRef,
						PredicateKey:     predicateKey,
						PredicateVersion: version,
					}
				}
			}
		}
		return rows.Err()
	})
	if err != nil {
		return PlacementAssessmentReviewOverrides{}, fmt.Errorf("load placement assessment review overrides: %w", err)
	}
	return overrides, nil
}

func (r *LedgerRepositoryImpl) ExpirePlacementAssessmentReviews(
	ctx context.Context,
	input ExpirePlacementAssessmentReviewsInput,
) (int64, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return 0, fmt.Errorf("team_id is required: %w", err)
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	var affected int64
	// Expiry is a server-controlled transition across profile-owned tasks.
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.current_team_id', ?, true)", input.TeamID).Error; err != nil {
			return fmt.Errorf("set expiry team context: %w", err)
		}
		if err := ensureActiveTeamForMutation(ctx, tx, input.TeamID); err != nil {
			return err
		}
		type expiredTask struct {
			ownerProfileID  string
			placementRunID  string
			placementItemID string
			reviewTaskID    string
		}
		rows, err := tx.WithContext(ctx).Raw(`
			UPDATE review_tasks AS task
			SET status = 'expired',
			    resolved_at = COALESCE(task.resolved_at, ?),
			    updated_at = ?,
			    version = task.version + 1
			FROM placement_items AS item
			WHERE task.team_id = ?::uuid
			  AND task.placement_item_id = item.placement_item_id
			  AND item.team_id = task.team_id
			  AND task.status IN ('open', 'acknowledged')
			  AND task.expires_at IS NOT NULL
			  AND task.expires_at <= ?
			  AND (
			      jsonb_exists(task.payload, 'semantic_kind')
			      OR (task.task_type = 'identity_needs_review' AND task.reason = 'ambiguous_entity')
			      OR (task.task_type = 'predicate_needs_review' AND task.reason = 'unknown_predicate')
			  )
			RETURNING task.owner_profile_id::text, item.placement_run_id::text,
			          task.placement_item_id::text, task.review_task_id::text
		`, input.Now, input.Now, input.TeamID, input.Now).Rows()
		if err != nil {
			return err
		}
		expiredTasks := make([]expiredTask, 0)
		for rows.Next() {
			var task expiredTask
			if err := rows.Scan(&task.ownerProfileID, &task.placementRunID, &task.placementItemID, &task.reviewTaskID); err != nil {
				_ = rows.Close()
				return err
			}
			expiredTasks = append(expiredTasks, task)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		affected = int64(len(expiredTasks))
		for _, task := range expiredTasks {
			payload, err := marshalJSON(map[string]any{
				"actor":          "system",
				"review_task_id": task.reviewTaskID,
				"reason":         "semantic_review_expired",
			})
			if err != nil {
				return err
			}
			result := tx.WithContext(ctx).Exec(`
				INSERT INTO placement_outcomes (
				    team_id, placement_run_id, placement_item_id, owner_profile_id,
				    outcome_kind, status, idempotency_key, payload
				) VALUES (
				    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
				    'semantic_review_expired', 'expired', ?, ?::jsonb
				)
				ON CONFLICT (team_id, owner_profile_id, idempotency_key)
				WHERE idempotency_key <> ''
				DO NOTHING
			`, input.TeamID, task.placementRunID, task.placementItemID, task.ownerProfileID,
				"system:semantic_review_expiry:"+task.reviewTaskID, string(payload))
			if result.Error != nil {
				return result.Error
			}
		}
		if affected == 0 {
			return nil
		}
		if err := tx.WithContext(ctx).Exec(`
			WITH eligible_items AS (
			    SELECT DISTINCT task.placement_item_id
			    FROM review_tasks AS task
			    WHERE task.team_id = ?::uuid
			      AND task.placement_item_id IS NOT NULL
			      AND task.status = 'expired'
			      AND task.expires_at IS NOT NULL
			      AND task.expires_at <= ?
			      AND (
			          jsonb_exists(task.payload, 'semantic_kind')
			          OR (task.task_type = 'identity_needs_review' AND task.reason = 'ambiguous_entity')
			          OR (task.task_type = 'predicate_needs_review' AND task.reason = 'unknown_predicate')
			      )
			      AND NOT EXISTS (
			          SELECT 1
			          FROM review_tasks AS open_task
			          WHERE open_task.team_id = task.team_id
			            AND open_task.placement_item_id = task.placement_item_id
			            AND open_task.status IN ('open', 'acknowledged')
			      )
			), updated_items AS (
			    UPDATE placement_items AS item
			    SET status = 'completed',
			        category = CASE WHEN item.category = 'quarantined' THEN item.category ELSE 'candidate' END,
			        version = version + 1,
			        updated_at = now()
			WHERE item.team_id = ?::uuid
			  AND item.status = 'awaiting_review'
			  AND item.placement_item_id IN (SELECT placement_item_id FROM eligible_items)
		    RETURNING item.placement_run_id, item.placement_item_id
		), affected_runs AS (
		    SELECT DISTINCT placement_run_id FROM updated_items
			)
			UPDATE placement_runs AS run
			SET status = 'completed',
			    worker_id = '',
			    lease_until = NULL,
			    completed_at = now(),
			    updated_at = now()
			WHERE run.team_id = ?::uuid
			  AND run.status = 'awaiting_review'
			  AND run.placement_run_id IN (SELECT placement_run_id FROM affected_runs)
			  AND NOT EXISTS (
			      SELECT 1
			      FROM placement_items AS pending_item
		      WHERE pending_item.team_id = run.team_id
		        AND pending_item.placement_run_id = run.placement_run_id
		        AND pending_item.status IN ('queued', 'processing', 'awaiting_review')
		        AND NOT EXISTS (
		            SELECT 1
		            FROM updated_items AS updated_item
		            WHERE updated_item.placement_item_id = pending_item.placement_item_id
		        )
		  )
		`, input.TeamID, input.Now, input.TeamID, input.TeamID).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("expire placement assessment reviews: %w", err)
	}
	return affected, nil
}

func normalizeLoadPlacementAssessmentInput(input LoadPlacementAssessmentInput) LoadPlacementAssessmentInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	return input
}

func normalizeReservePlacementAssessmentProviderAttemptInput(input ReservePlacementAssessmentProviderAttemptInput) ReservePlacementAssessmentProviderAttemptInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.PlacementRunID = strings.TrimSpace(input.PlacementRunID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	return input
}

func validateReservePlacementAssessmentProviderAttemptInput(input ReservePlacementAssessmentProviderAttemptInput) error {
	for label, value := range map[string]string{
		"team_id":           input.TeamID,
		"owner_profile_id":  input.OwnerProfileID,
		"placement_run_id":  input.PlacementRunID,
		"placement_item_id": input.PlacementItemID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if input.ExpectedAttempts < 1 {
		return errors.New("expected_attempts must be greater than zero")
	}
	return nil
}

func validateLoadPlacementAssessmentInput(input LoadPlacementAssessmentInput) error {
	for label, value := range map[string]string{
		"team_id":           input.TeamID,
		"owner_profile_id":  input.OwnerProfileID,
		"placement_item_id": input.PlacementItemID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	return nil
}

func normalizePersistPlacementAssessmentInput(input PersistPlacementAssessmentInput) PersistPlacementAssessmentInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.ClaimKey = strings.TrimSpace(input.ClaimKey)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.AssessorContractVersion = strings.TrimSpace(input.AssessorContractVersion)
	input.Model = strings.TrimSpace(input.Model)
	input.Tokenizer = strings.TrimSpace(input.Tokenizer)
	input.ResponseHash = strings.TrimSpace(input.ResponseHash)
	if input.ValidatedAt.IsZero() {
		input.ValidatedAt = time.Now().UTC()
	} else {
		input.ValidatedAt = input.ValidatedAt.UTC()
	}
	return input
}

func validatePersistPlacementAssessmentInput(input PersistPlacementAssessmentInput) error {
	for label, value := range map[string]string{
		"team_id":           input.TeamID,
		"owner_profile_id":  input.OwnerProfileID,
		"placement_item_id": input.PlacementItemID,
		"claim_key":         input.ClaimKey,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	for label, value := range map[string]string{
		"request_id":                input.RequestID,
		"assessor_contract_version": input.AssessorContractVersion,
		"model":                     input.Model,
		"tokenizer":                 input.Tokenizer,
		"response_hash":             input.ResponseHash,
	} {
		if value == "" {
			return fmt.Errorf("%s is required", label)
		}
	}
	if input.InputTokens < 0 || input.OutputTokens < 0 || input.CandidateContextTokens < 0 {
		return errors.New("assessment token counts must be non-negative")
	}
	if !jsonObject(input.NormalizedResponse) {
		return errors.New("normalized_response must be a JSON object")
	}
	return nil
}

func normalizeLoadAutoWriteConfidencePolicyInput(input LoadAutoWriteConfidencePolicyInput) LoadAutoWriteConfidencePolicyInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	return input
}

func normalizeLoadPlacementAssessmentReviewOverridesInput(input LoadPlacementAssessmentReviewOverridesInput) LoadPlacementAssessmentReviewOverridesInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.AssessmentID = strings.TrimSpace(input.AssessmentID)
	return input
}

func validateLoadPlacementAssessmentReviewOverridesInput(input LoadPlacementAssessmentReviewOverridesInput) error {
	for label, value := range map[string]string{
		"team_id":           input.TeamID,
		"owner_profile_id":  input.OwnerProfileID,
		"placement_item_id": input.PlacementItemID,
		"assessment_id":     input.AssessmentID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	return nil
}

func reviewOverrideInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	case int:
		return typed
	default:
		return 0
	}
}

func validateLoadAutoWriteConfidencePolicyInput(input LoadAutoWriteConfidencePolicyInput) error {
	for label, value := range map[string]string{
		"team_id":          input.TeamID,
		"owner_profile_id": input.OwnerProfileID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if math.IsNaN(input.GlobalThreshold) || math.IsInf(input.GlobalThreshold, 0) || input.GlobalThreshold < 0 || input.GlobalThreshold > 1 {
		return errors.New("global threshold must be between 0 and 1")
	}
	return nil
}

func insertPlacementAssessment(
	ctx context.Context,
	tx *gorm.DB,
	input PersistPlacementAssessmentInput,
) (*PlacementAssessment, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO placement_assessments (
		    team_id, placement_item_id, claim_key, owner_profile_id, request_id,
		    assessor_contract_version, model, tokenizer,
		    input_tokens, output_tokens, candidate_context_tokens,
		    candidate_context_truncated, normalized_response, response_hash, validated_at
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?
		)
		ON CONFLICT DO NOTHING
		RETURNING team_id::text, assessment_id::text, owner_profile_id::text,
		          placement_item_id::text, claim_key::text, request_id,
		          assessor_contract_version, model, tokenizer,
		          input_tokens, output_tokens, candidate_context_tokens,
		          candidate_context_truncated, normalized_response, response_hash,
		          validated_at, created_at
	`, input.TeamID, input.PlacementItemID, input.ClaimKey, input.OwnerProfileID, input.RequestID,
		input.AssessorContractVersion, input.Model, input.Tokenizer,
		input.InputTokens, input.OutputTokens, input.CandidateContextTokens,
		input.CandidateContextTruncated, string(input.NormalizedResponse), input.ResponseHash,
		input.ValidatedAt).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	assessment, err := scanPlacementAssessment(rows)
	if err != nil {
		return nil, err
	}
	return assessment, rows.Err()
}

func placementAssessmentClaimKeyExists(
	ctx context.Context,
	tx *gorm.DB,
	teamID, claimKey string,
) (bool, error) {
	var exists bool
	err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM placement_assessments
			WHERE team_id = ?::uuid
			  AND claim_key = ?::uuid
		)
	`, teamID, claimKey).Row().Scan(&exists)
	return exists, err
}

func loadPlacementAssessment(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerProfileID, placementItemID string,
) (*PlacementAssessment, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, assessment_id::text, owner_profile_id::text,
		       placement_item_id::text, claim_key::text, request_id,
		       assessor_contract_version, model, tokenizer,
		       input_tokens, output_tokens, candidate_context_tokens,
		       candidate_context_truncated, normalized_response, response_hash,
		       validated_at, created_at
		FROM placement_assessments
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_item_id = ?::uuid
		LIMIT 1
	`, teamID, ownerProfileID, placementItemID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	assessment, err := scanPlacementAssessment(rows)
	if err != nil {
		return nil, err
	}
	return assessment, rows.Err()
}

func scanPlacementAssessment(rows *sql.Rows) (*PlacementAssessment, error) {
	var assessment PlacementAssessment
	var response []byte
	if err := rows.Scan(
		&assessment.TeamID,
		&assessment.AssessmentID,
		&assessment.OwnerProfileID,
		&assessment.PlacementItemID,
		&assessment.ClaimKey,
		&assessment.RequestID,
		&assessment.AssessorContractVersion,
		&assessment.Model,
		&assessment.Tokenizer,
		&assessment.InputTokens,
		&assessment.OutputTokens,
		&assessment.CandidateContextTokens,
		&assessment.CandidateContextTruncated,
		&response,
		&assessment.ResponseHash,
		&assessment.ValidatedAt,
		&assessment.CreatedAt,
	); err != nil {
		return nil, err
	}
	if !jsonObject(response) {
		return nil, errors.New("stored normalized_response is not a JSON object")
	}
	assessment.NormalizedResponse = append(json.RawMessage(nil), response...)
	return &assessment, nil
}

func confidenceThresholdFromJSON(raw []byte) (float64, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, errors.New("must be a JSON number")
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("must be a JSON number")
	}
	threshold, err := number.Float64()
	if err != nil || math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 1 {
		return 0, errors.New("must be between 0 and 1")
	}
	return threshold, nil
}

func jsonObject(raw []byte) bool {
	var value map[string]json.RawMessage
	return len(raw) > 0 && json.Unmarshal(raw, &value) == nil && value != nil
}
