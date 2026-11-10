package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const evidenceLifecycleCompleted = "completed"

type RetractEvidenceInput = knowledgecontract.RetractEvidenceInput
type EvidenceLifecycleResult = knowledgecontract.EvidenceLifecycleResult

type evidenceLifecycleOperationInput struct {
	TeamID                  string
	OwnerProfileID          string
	ActorProfileID          string
	Action                  string
	EvidenceIDs             []string
	Reason                  string
	IdempotencyKey          string
	RequestHash             string
	ReplacementID           string
	ReplacementOccurrenceID string
	ReplacementIngestID     string
}

type evidenceLifecyclePlan struct {
	SpaceID     string
	EvidenceIDs []string
}

func (r *Store) RetractEvidence(ctx context.Context, input RetractEvidenceInput) (*EvidenceLifecycleResult, error) {
	input = normalizeRetractEvidenceInput(input)
	if err := validateRetractEvidenceInput(input); err != nil {
		return nil, err
	}
	var result *EvidenceLifecycleResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		resolved, err := storagepostgres.ResolveMemoryHandlesTx(ctx, tx, input.TeamID, "evidence_fragments", input.EvidenceIDs, "")
		if err != nil {
			return err
		}
		input.EvidenceIDs = normalizeUUIDStrings(resolved)
		if err := authorizeEvidenceRetraction(ctx, tx, input); err != nil {
			return err
		}
		if err := lockEvidenceLifecycleIdempotencyKeys(ctx, tx, input.TeamID, input.OwnerProfileID, []string{input.IdempotencyKey}); err != nil {
			return err
		}
		existing, err := loadEvidenceLifecycleOperation(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.Action != "retract" || existing.RequestHash != input.RequestHash {
				return fmt.Errorf("%w: evidence lifecycle idempotency key reused with a different request", ErrIdempotencyConflict)
			}
			result = existing.Result
			result.Existing = true
			return nil
		}
		planned, err := planEvidenceLifecycle(ctx, tx, evidenceLifecycleOperationInput{
			TeamID:         input.TeamID,
			OwnerProfileID: input.OwnerProfileID,
			Action:         "retract",
			EvidenceIDs:    input.EvidenceIDs,
		})
		if err != nil {
			return err
		}
		operation := evidenceLifecycleOperationInput{
			TeamID:         input.TeamID,
			OwnerProfileID: input.OwnerProfileID,
			Action:         "retract",
			EvidenceIDs:    input.EvidenceIDs,
			Reason:         input.Reason,
			IdempotencyKey: input.IdempotencyKey,
			RequestHash:    input.RequestHash,
		}
		decisionID, err := insertEvidenceLifecycleOperation(ctx, tx, operation, planned)
		if err != nil {
			return err
		}
		if err := insertEvidenceLifecycleEvents(ctx, tx, operation, decisionID, planned.SpaceID); err != nil {
			return err
		}
		if err := applyEvidenceLifecycleEffects(ctx, tx, operation, decisionID, planned); err != nil {
			return err
		}
		result = evidenceLifecycleResultFromPlan(decisionID, planned)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: retract evidence: %w", err)
	}
	return result, nil
}

func normalizeRetractEvidenceInput(input RetractEvidenceInput) RetractEvidenceInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Reason = strings.TrimSpace(input.Reason)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	input.EvidenceIDs = normalizeUUIDStrings(input.EvidenceIDs)
	return input
}

func validateRetractEvidenceInput(input RetractEvidenceInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if len(input.EvidenceIDs) == 0 {
		return errors.New("evidence_ids is required")
	}
	if len(input.EvidenceIDs) > 50 {
		return errors.New("evidence_ids exceeds maximum 50")
	}
	if input.Reason == "" {
		return errors.New("reason is required")
	}
	if len(input.Reason) > 1000 {
		return errors.New("reason exceeds maximum 1000")
	}
	if input.IdempotencyKey == "" {
		return errors.New("idempotency_key is required")
	}
	if len(input.IdempotencyKey) > 128 {
		return errors.New("idempotency_key exceeds maximum 128")
	}
	if input.RequestHash == "" {
		return errors.New("request_hash is required")
	}
	seen := make(map[string]struct{}, len(input.EvidenceIDs))
	for _, evidenceID := range input.EvidenceIDs {
		if _, err := uuid.Parse(evidenceID); err != nil {
			return fmt.Errorf("%w: evidence_ids contains an invalid UUID: %w", ErrEvidenceLifecycleIDInvalid, err)
		}
		if _, exists := seen[evidenceID]; exists {
			return fmt.Errorf("evidence_ids contains duplicate UUID %q", evidenceID)
		}
		seen[evidenceID] = struct{}{}
	}
	return nil
}

func lockEvidenceLifecycleIdempotencyKeys(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	ownerProfileID string,
	keys []string,
) error {
	if len(keys) == 0 {
		return nil
	}
	keys = append([]string(nil), keys...)
	sort.Strings(keys)
	for _, key := range keys {
		lockKey := strings.Join([]string{teamID, ownerProfileID, key}, ":")
		if err := tx.WithContext(ctx).Exec(
			"SELECT pg_advisory_xact_lock(hashtextextended(?::text, 0))",
			lockKey,
		).Error; err != nil {
			return err
		}
	}
	return nil
}

type storedEvidenceLifecycleOperation struct {
	DecisionID          string
	Action              string
	RequestHash         string
	ReplacementIngestID string
	Result              *EvidenceLifecycleResult
}

func loadEvidenceLifecycleOperation(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	ownerProfileID string,
	idempotencyKey string,
) (*storedEvidenceLifecycleOperation, error) {
	row := tx.WithContext(ctx).Raw(`
		SELECT lifecycle_operation_id::text, action, request_hash, COALESCE(replacement_ingest_id::text, ''), result
		FROM evidence_lifecycle_operations
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND idempotency_key = ?
		LIMIT 1
	`, teamID, ownerProfileID, idempotencyKey).Row()
	var stored storedEvidenceLifecycleOperation
	var raw []byte
	if err := row.Scan(&stored.DecisionID, &stored.Action, &stored.RequestHash, &stored.ReplacementIngestID, &raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	result := &EvidenceLifecycleResult{}
	if err := json.Unmarshal(raw, result); err != nil {
		return nil, err
	}
	result.DecisionID = stored.DecisionID
	stored.Result = result
	return &stored, nil
}

func applyEvidenceSupersessions(
	ctx context.Context,
	tx *gorm.DB,
	input CreateIngestInput,
	ingestID string,
	evidence []EvidenceFragment,
) error {
	for index, item := range input.Evidence {
		if len(item.SupersedesEvidenceIDs) == 0 {
			continue
		}
		operation := evidenceLifecycleOperationInput{
			TeamID:                  input.TeamID,
			OwnerProfileID:          input.OwnerProfileID,
			Action:                  "supersede",
			EvidenceIDs:             item.SupersedesEvidenceIDs,
			IdempotencyKey:          item.IdempotencyKey,
			RequestHash:             input.RequestHash,
			ReplacementID:           evidence[index].FragmentID,
			ReplacementOccurrenceID: evidence[index].OccurrenceID,
			ReplacementIngestID:     ingestID,
		}
		planned, err := planEvidenceLifecycle(ctx, tx, operation)
		if err != nil {
			return err
		}
		decisionID, err := insertEvidenceLifecycleOperation(ctx, tx, operation, planned)
		if err != nil {
			return err
		}
		if err := insertEvidenceLifecycleEvents(ctx, tx, operation, decisionID, planned.SpaceID); err != nil {
			return err
		}
		if err := applyEvidenceLifecycleEffects(ctx, tx, operation, decisionID, planned); err != nil {
			return err
		}
		evidence[index].SupersededEvidenceIDs = append([]string(nil), planned.EvidenceIDs...)
	}
	return nil
}

func planEvidenceLifecycle(
	ctx context.Context,
	tx *gorm.DB,
	input evidenceLifecycleOperationInput,
) (*evidenceLifecyclePlan, error) {
	if err := lockEvidenceLifecycleTargetIDs(ctx, tx, input.TeamID, input.EvidenceIDs); err != nil {
		return nil, err
	}
	var plan *evidenceLifecyclePlan
	err := withSystemModeInTx(ctx, tx, input.TeamID, input.OwnerProfileID, func(systemTx *gorm.DB) error {
		var err error
		plan, err = planEvidenceLifecycleInSystem(ctx, systemTx, input)
		return err
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func lockEvidenceLifecycleTargetIDs(ctx context.Context, tx *gorm.DB, teamID string, evidenceIDs []string) error {
	evidenceIDs = sortedEvidenceIDs(evidenceIDs)
	for _, evidenceID := range evidenceIDs {
		if err := lockEvidenceLifecycleTarget(ctx, tx, teamID, evidenceID); err != nil {
			return err
		}
	}
	return nil
}

func planEvidenceLifecycleInSystem(
	ctx context.Context,
	tx *gorm.DB,
	input evidenceLifecycleOperationInput,
) (*evidenceLifecyclePlan, error) {
	evidenceIDs := sortedEvidenceIDs(input.EvidenceIDs)
	targetSpaceID, err := validateEvidenceLifecycleTargets(ctx, tx, input.TeamID, input.OwnerProfileID, evidenceIDs)
	if err != nil {
		return nil, err
	}
	if input.ReplacementID != "" {
		if err := validateEvidenceLifecycleReplacement(ctx, tx, input.TeamID, input.OwnerProfileID, input.ReplacementID, targetSpaceID); err != nil {
			return nil, err
		}
	}
	return &evidenceLifecyclePlan{SpaceID: targetSpaceID, EvidenceIDs: evidenceIDs}, nil
}

func sortedEvidenceIDs(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

func validateEvidenceLifecycleTargets(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	ownerProfileID string,
	evidenceIDs []string,
) (string, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT fragment.fragment_id::text,
		       fragment.owner_profile_id::text,
		       fragment.space_id::text,
		       COALESCE(fragment.source_revision_id::text, ''),
		       COALESCE(source.current_revision_id::text, ''),
		       EXISTS (
		           SELECT 1
		           FROM evidence_lifecycle_events AS lifecycle
		           WHERE lifecycle.team_id = fragment.team_id
		             AND lifecycle.target_fragment_id = fragment.fragment_id
		       ) AS terminal
		FROM evidence_fragments AS fragment
		LEFT JOIN evidence_sources AS source
		  ON source.team_id = fragment.team_id
		 AND source.source_id = fragment.source_id
		WHERE fragment.team_id = ?::uuid
		  AND fragment.fragment_id = ANY(?::uuid[])
		ORDER BY fragment.fragment_id ASC
	`, teamID, pq.Array(evidenceIDs)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type targetState struct {
		ownerProfileID    string
		spaceID           string
		sourceRevisionID  string
		currentRevisionID string
		terminal          bool
	}
	states := make(map[string]targetState, len(evidenceIDs))
	for rows.Next() {
		var evidenceID string
		var state targetState
		if err := rows.Scan(&evidenceID, &state.ownerProfileID, &state.spaceID, &state.sourceRevisionID, &state.currentRevisionID, &state.terminal); err != nil {
			return "", err
		}
		states[evidenceID] = state
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	targetSpaceID := ""
	for _, evidenceID := range evidenceIDs {
		state, exists := states[evidenceID]
		if !exists || state.ownerProfileID != ownerProfileID {
			return "", ErrEvidenceLifecycleNotFound
		}
		if state.spaceID == "" {
			return "", ErrEvidenceLifecycleNotFound
		}
		if targetSpaceID == "" {
			targetSpaceID = state.spaceID
		} else if targetSpaceID != state.spaceID {
			return "", ErrEvidenceLifecycleConflict
		}
		if state.terminal {
			return "", ErrEvidenceLifecycleConflict
		}
		if state.sourceRevisionID != "" && state.sourceRevisionID != state.currentRevisionID {
			return "", ErrEvidenceLifecycleConflict
		}
	}
	return targetSpaceID, nil
}

func validateEvidenceLifecycleReplacement(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	ownerProfileID string,
	replacementID string,
	targetSpaceID string,
) error {
	if _, err := uuid.Parse(replacementID); err != nil {
		return ErrEvidenceLifecycleNotFound
	}
	var replacementOwnerID, replacementSpaceID string
	err := tx.WithContext(ctx).Raw(`
		SELECT owner_profile_id::text, COALESCE(space_id::text, '')
		FROM evidence_fragments
		WHERE team_id = ?::uuid
		  AND fragment_id = ?::uuid
		LIMIT 1
	`, teamID, replacementID).Row().Scan(&replacementOwnerID, &replacementSpaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEvidenceLifecycleNotFound
		}
		return err
	}
	if replacementOwnerID != ownerProfileID || replacementSpaceID == "" {
		return ErrEvidenceLifecycleNotFound
	}
	if replacementSpaceID != targetSpaceID {
		return ErrEvidenceLifecycleConflict
	}
	return nil
}

func insertEvidenceLifecycleOperation(
	ctx context.Context,
	tx *gorm.DB,
	input evidenceLifecycleOperationInput,
	plan *evidenceLifecyclePlan,
) (string, error) {
	result := evidenceLifecycleResultFromPlan("", plan)
	encoded, err := marshalEvidenceLifecycleResult(result)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_lifecycle_operations (
		    team_id, space_id, owner_profile_id, actor_profile_id, action, idempotency_key, request_hash,
		    reason, replacement_ingest_id, result
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid, ?, ?, ?, ?, NULLIF(?, '')::uuid, ?::jsonb
		)
		RETURNING lifecycle_operation_id::text
	`, input.TeamID, plan.SpaceID, input.OwnerProfileID, input.ActorProfileID, input.Action, input.IdempotencyKey, input.RequestHash,
		input.Reason, input.ReplacementIngestID, string(encoded)).Rows()
	if err != nil {
		if isPostgresUniqueConstraint(err, "evidence_lifecycle_operations_idempotency_unique") {
			return "", fmt.Errorf("%w: evidence lifecycle idempotency key already exists", ErrIdempotencyConflict)
		}
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", sql.ErrNoRows
	}
	var operationID string
	if err := rows.Scan(&operationID); err != nil {
		return "", err
	}
	return operationID, rows.Err()
}

func marshalEvidenceLifecycleResult(result *EvidenceLifecycleResult) ([]byte, error) {
	if result == nil {
		result = &EvidenceLifecycleResult{}
	}
	return json.Marshal(map[string]any{
		"processing_state":       result.ProcessingState,
		"retracted_evidence_ids": result.RetractedEvidenceIDs,
	})
}

func evidenceLifecycleResultFromPlan(decisionID string, plan *evidenceLifecyclePlan) *EvidenceLifecycleResult {
	if plan == nil {
		plan = &evidenceLifecyclePlan{}
	}
	return &EvidenceLifecycleResult{
		DecisionID:           decisionID,
		ProcessingState:      evidenceLifecycleCompleted,
		RetractedEvidenceIDs: append([]string(nil), plan.EvidenceIDs...),
	}
}

func insertEvidenceLifecycleEvents(
	ctx context.Context,
	tx *gorm.DB,
	input evidenceLifecycleOperationInput,
	operationID string,
	spaceID string,
) error {
	replacementOccurrenceID := input.ReplacementOccurrenceID
	if replacementOccurrenceID == "" {
		replacementOccurrenceID = input.ReplacementID
	}
	for _, evidenceID := range sortedEvidenceIDs(input.EvidenceIDs) {
		result := tx.WithContext(ctx).Exec(`
			INSERT INTO evidence_lifecycle_events (
			    team_id, lifecycle_operation_id, target_fragment_id,
			    target_occurrence_id, replacement_fragment_id, replacement_occurrence_id,
			    owner_profile_id, action, space_id
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid,
			    NULLIF(?, '')::uuid, ?::uuid, ?, ?::uuid
			)
		`, input.TeamID, operationID, evidenceID, evidenceID, input.ReplacementID,
			replacementOccurrenceID, input.OwnerProfileID, input.Action, spaceID)
		if result.Error != nil {
			if isPostgresUniqueConstraint(result.Error, "evidence_lifecycle_events_terminal_target_unique") {
				return fmt.Errorf("%w: evidence target is already terminal", ErrEvidenceLifecycleConflict)
			}
			return result.Error
		}
	}
	return nil
}

func applyEvidenceLifecycleEffects(
	ctx context.Context,
	tx *gorm.DB,
	input evidenceLifecycleOperationInput,
	operationID string,
	plan *evidenceLifecyclePlan,
) error {
	return withSystemModeInTx(ctx, tx, input.TeamID, input.OwnerProfileID, func(systemTx *gorm.DB) error {
		return applyEvidenceLifecycleEffectsInSystem(ctx, systemTx, input, operationID, plan)
	})
}

func applyEvidenceLifecycleEffectsInSystem(ctx context.Context, tx *gorm.DB, input evidenceLifecycleOperationInput, operationID string, plan *evidenceLifecyclePlan) error {
	return retireEvidenceLifecycleSearchDocuments(ctx, tx, input.TeamID, plan.SpaceID, "evidence", plan.EvidenceIDs)
}

func retireEvidenceLifecycleSearchDocuments(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	spaceID string,
	sourceKind string,
	sourceIDs []string,
) error {
	if len(sourceIDs) == 0 {
		return nil
	}
	if err := tx.WithContext(ctx).Exec(`
		UPDATE search_documents
		SET document_version = document_version + 1,
		    search_state = 'not_required',
		    embedding = NULL,
		    embedding_updated_at = NULL,
		    embedding_error = '',
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND space_id = ?::uuid
		  AND source_kind = ?
		  AND source_id = ANY(?::uuid[])
	`, teamID, spaceID, sourceKind, pq.Array(sourceIDs)).Error; err != nil {
		return err
	}
	return nil
}
