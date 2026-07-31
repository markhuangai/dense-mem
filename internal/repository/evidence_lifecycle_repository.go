package repository

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

	"github.com/markhuangai/dense-mem/internal/domain"
)

const evidenceLifecycleCompleted = "completed"

type RetractEvidenceInput struct {
	TeamID         string
	OwnerProfileID string
	EvidenceIDs    []string
	Reason         string
	IdempotencyKey string
	RequestHash    string
}

type EvidenceLifecycleResult struct {
	DecisionID                      string   `json:"-"`
	ProcessingState                 string   `json:"processing_state"`
	RetractedEvidenceIDs            []string `json:"retracted_evidence_ids"`
	AffectedRelationshipCount       int      `json:"affected_relationship_count"`
	PendingRelationshipCount        int      `json:"pending_relationship_count"`
	RetainedActiveRelationshipCount int      `json:"retained_active_relationship_count"`
	Existing                        bool     `json:"-"`
}

type evidenceLifecycleOperationInput struct {
	TeamID              string
	OwnerProfileID      string
	ActorProfileID      string
	Action              string
	EvidenceIDs         []string
	Reason              string
	IdempotencyKey      string
	RequestHash         string
	ReplacementID       string
	ReplacementIngestID string
}

type evidenceLifecyclePlan struct {
	EvidenceIDs                     []string
	Supports                        []evidenceLifecycleSupport
	AffectedRelationshipCount       int
	PendingRelationshipCount        int
	RetainedActiveRelationshipCount int
}

type evidenceLifecycleSupport struct {
	SupportID      string
	RelationshipID string
	OwnerProfileID string
}

func (r *LedgerRepositoryImpl) RetractEvidence(ctx context.Context, input RetractEvidenceInput) (*EvidenceLifecycleResult, error) {
	input = normalizeRetractEvidenceInput(input)
	if err := validateRetractEvidenceInput(input); err != nil {
		return nil, err
	}
	var result *EvidenceLifecycleResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
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
		if err := insertEvidenceLifecycleEvents(ctx, tx, operation, decisionID); err != nil {
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
			return fmt.Errorf("evidence_ids contains invalid UUID %q: %w", evidenceID, err)
		}
		if _, exists := seen[evidenceID]; exists {
			return fmt.Errorf("evidence_ids contains duplicate UUID %q", evidenceID)
		}
		seen[evidenceID] = struct{}{}
	}
	return nil
}

func lockDirectSupersessionIdempotencyKeys(ctx context.Context, tx *gorm.DB, input CreateIngestInput) error {
	keys := make([]string, 0)
	for _, item := range input.Evidence {
		if len(item.SupersedesEvidenceIDs) > 0 {
			keys = append(keys, item.IdempotencyKey)
		}
	}
	return lockEvidenceLifecycleIdempotencyKeys(ctx, tx, input.TeamID, input.OwnerProfileID, keys)
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

func loadDirectSupersessionReplay(ctx context.Context, tx *gorm.DB, input CreateIngestInput) (*CreateIngestResult, error) {
	if err := lockDirectSupersessionIdempotencyKeys(ctx, tx, input); err != nil {
		return nil, err
	}
	keys := make([]string, 0)
	for _, item := range input.Evidence {
		if len(item.SupersedesEvidenceIDs) > 0 {
			keys = append(keys, item.IdempotencyKey)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	var replacementIngestID string
	existingCount := 0
	for _, key := range keys {
		op, err := loadEvidenceLifecycleOperation(ctx, tx, input.TeamID, input.OwnerProfileID, key)
		if err != nil {
			return nil, err
		}
		if op == nil {
			continue
		}
		existingCount++
		if op.Action != "supersede" || op.RequestHash != input.RequestHash || op.ReplacementIngestID == "" {
			return nil, fmt.Errorf("%w: evidence lifecycle idempotency key reused with a different request", ErrIdempotencyConflict)
		}
		if replacementIngestID == "" {
			replacementIngestID = op.ReplacementIngestID
		} else if replacementIngestID != op.ReplacementIngestID {
			return nil, fmt.Errorf("%w: direct supersession replay does not resolve to one ingest", ErrIdempotencyConflict)
		}
	}
	if existingCount == 0 {
		return nil, nil
	}
	if existingCount != len(keys) {
		return nil, fmt.Errorf("%w: direct supersession retry must include every original replacement", ErrIdempotencyConflict)
	}
	return loadCreateIngestResult(ctx, tx, input.TeamID, replacementIngestID, true)
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

func applyDirectEvidenceSupersessions(
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
			TeamID:              input.TeamID,
			OwnerProfileID:      input.OwnerProfileID,
			Action:              "supersede",
			EvidenceIDs:         item.SupersedesEvidenceIDs,
			IdempotencyKey:      item.IdempotencyKey,
			RequestHash:         input.RequestHash,
			ReplacementID:       evidence[index].FragmentID,
			ReplacementIngestID: ingestID,
		}
		planned, err := planEvidenceLifecycle(ctx, tx, operation)
		if err != nil {
			return err
		}
		decisionID, err := insertEvidenceLifecycleOperation(ctx, tx, operation, planned)
		if err != nil {
			return err
		}
		if err := insertEvidenceLifecycleEvents(ctx, tx, operation, decisionID); err != nil {
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
		if err := tx.WithContext(ctx).Exec(
			"SELECT pg_advisory_xact_lock(hashtextextended(?::text, 0))",
			strings.Join([]string{teamID, "evidence-lifecycle-target", evidenceID}, ":"),
		).Error; err != nil {
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
	if err := validateEvidenceLifecycleTargets(ctx, tx, input.TeamID, input.OwnerProfileID, evidenceIDs); err != nil {
		return nil, err
	}
	supports, err := loadEffectiveEvidenceLifecycleSupports(ctx, tx, input.TeamID, evidenceIDs)
	if err != nil {
		return nil, err
	}
	plan := &evidenceLifecyclePlan{EvidenceIDs: evidenceIDs, Supports: supports}
	supportsByRelationship := make(map[string][]evidenceLifecycleSupport)
	for _, support := range supports {
		supportsByRelationship[support.RelationshipID] = append(supportsByRelationship[support.RelationshipID], support)
	}
	relationshipIDs := make([]string, 0, len(supportsByRelationship))
	for relationshipID := range supportsByRelationship {
		relationshipIDs = append(relationshipIDs, relationshipID)
	}
	sort.Strings(relationshipIDs)
	for _, relationshipID := range relationshipIDs {
		relationship, err := loadRelationshipRecordForUpdate(ctx, tx, input.TeamID, relationshipID)
		if err != nil {
			return nil, err
		}
		counts, err := effectiveRelationshipSupportCounts(ctx, tx, input.TeamID, relationshipID)
		if err != nil {
			return nil, err
		}
		nextStatus := statusForEffectiveSupport(relationship.Status, counts.SupportCount-len(supportsByRelationship[relationshipID]))
		plan.AffectedRelationshipCount++
		switch nextStatus {
		case string(domain.RelationshipStatusPendingEvidence):
			plan.PendingRelationshipCount++
		case string(domain.RelationshipStatusActive):
			plan.RetainedActiveRelationshipCount++
		}
	}
	return plan, nil
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
) error {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT fragment.fragment_id::text,
		       fragment.owner_profile_id::text,
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
		return err
	}
	defer rows.Close()
	type targetState struct {
		ownerProfileID    string
		sourceRevisionID  string
		currentRevisionID string
		terminal          bool
	}
	states := make(map[string]targetState, len(evidenceIDs))
	for rows.Next() {
		var evidenceID string
		var state targetState
		if err := rows.Scan(&evidenceID, &state.ownerProfileID, &state.sourceRevisionID, &state.currentRevisionID, &state.terminal); err != nil {
			return err
		}
		states[evidenceID] = state
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, evidenceID := range evidenceIDs {
		state, exists := states[evidenceID]
		if !exists || state.ownerProfileID != ownerProfileID {
			return ErrEvidenceLifecycleNotFound
		}
		if state.terminal {
			return ErrEvidenceLifecycleConflict
		}
		if state.sourceRevisionID != "" && state.sourceRevisionID != state.currentRevisionID {
			return ErrEvidenceLifecycleConflict
		}
	}
	return nil
}

func loadEffectiveEvidenceLifecycleSupports(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	evidenceIDs []string,
) ([]evidenceLifecycleSupport, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH latest AS (
			SELECT DISTINCT ON (support_id)
			       support_id,
			       decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			ORDER BY support_id, created_at DESC, support_decision_id DESC
		)
		SELECT support.support_id::text,
		       support.relationship_id::text,
		       support.owner_profile_id::text
		FROM relationship_evidence_supports AS support
		JOIN latest
		  ON latest.support_id = support.support_id
		LEFT JOIN evidence_quarantines AS quarantine
		  ON quarantine.team_id = support.team_id
		 AND quarantine.fragment_id = support.fragment_id
		 AND quarantine.status = 'active'
		LEFT JOIN evidence_sources AS source
		  ON source.team_id = support.team_id
		 AND source.source_id = support.source_id
		WHERE support.team_id = ?::uuid
		  AND support.fragment_id = ANY(?::uuid[])
		  AND latest.decision IN ('grant', 'reinstate')
		  AND quarantine.quarantine_id IS NULL
		  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
		ORDER BY support.relationship_id ASC, support.support_id ASC
	`, teamID, teamID, pq.Array(evidenceIDs)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	supports := []evidenceLifecycleSupport{}
	for rows.Next() {
		var support evidenceLifecycleSupport
		if err := rows.Scan(&support.SupportID, &support.RelationshipID, &support.OwnerProfileID); err != nil {
			return nil, err
		}
		supports = append(supports, support)
	}
	return supports, rows.Err()
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
		    team_id, owner_profile_id, actor_profile_id, action, idempotency_key, request_hash,
		    reason, replacement_ingest_id, result
		) VALUES (
		    ?::uuid, ?::uuid, NULLIF(?, '')::uuid, ?, ?, ?, ?, NULLIF(?, '')::uuid, ?::jsonb
		)
		RETURNING lifecycle_operation_id::text
	`, input.TeamID, input.OwnerProfileID, input.ActorProfileID, input.Action, input.IdempotencyKey, input.RequestHash,
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
		"processing_state":                   result.ProcessingState,
		"retracted_evidence_ids":             result.RetractedEvidenceIDs,
		"affected_relationship_count":        result.AffectedRelationshipCount,
		"pending_relationship_count":         result.PendingRelationshipCount,
		"retained_active_relationship_count": result.RetainedActiveRelationshipCount,
	})
}

func evidenceLifecycleResultFromPlan(decisionID string, plan *evidenceLifecyclePlan) *EvidenceLifecycleResult {
	if plan == nil {
		plan = &evidenceLifecyclePlan{}
	}
	return &EvidenceLifecycleResult{
		DecisionID:                      decisionID,
		ProcessingState:                 evidenceLifecycleCompleted,
		RetractedEvidenceIDs:            append([]string(nil), plan.EvidenceIDs...),
		AffectedRelationshipCount:       plan.AffectedRelationshipCount,
		PendingRelationshipCount:        plan.PendingRelationshipCount,
		RetainedActiveRelationshipCount: plan.RetainedActiveRelationshipCount,
	}
}

func insertEvidenceLifecycleEvents(
	ctx context.Context,
	tx *gorm.DB,
	input evidenceLifecycleOperationInput,
	operationID string,
) error {
	for _, evidenceID := range sortedEvidenceIDs(input.EvidenceIDs) {
		result := tx.WithContext(ctx).Exec(`
			INSERT INTO evidence_lifecycle_events (
			    team_id, lifecycle_operation_id, target_fragment_id,
			    replacement_fragment_id, owner_profile_id, action
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid, ?::uuid, ?
			)
		`, input.TeamID, operationID, evidenceID, input.ReplacementID, input.OwnerProfileID, input.Action)
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

func applyEvidenceLifecycleEffectsInSystem(
	ctx context.Context,
	tx *gorm.DB,
	input evidenceLifecycleOperationInput,
	operationID string,
	plan *evidenceLifecyclePlan,
) error {
	relationshipsToRetire, err := revokeEvidenceLifecycleSupports(ctx, tx, input, operationID, plan.Supports)
	if err != nil {
		return err
	}
	if err := retireEvidenceLifecycleSearchDocuments(ctx, tx, input.TeamID, "evidence", plan.EvidenceIDs); err != nil {
		return err
	}
	return retireEvidenceLifecycleSearchDocuments(ctx, tx, input.TeamID, "relationship", relationshipsToRetire)
}

func revokeEvidenceLifecycleSupports(
	ctx context.Context,
	tx *gorm.DB,
	input evidenceLifecycleOperationInput,
	operationID string,
	supports []evidenceLifecycleSupport,
) ([]string, error) {
	decisionByRelationship := make(map[string]string)
	for _, support := range supports {
		decisionID, err := insertSupportDecisionEvent(ctx, tx, supportDecisionInput{
			TeamID:         input.TeamID,
			OwnerProfileID: support.OwnerProfileID,
			ActorProfileID: lifecycleActorProfileID(input),
			SupportID:      support.SupportID,
			RelationshipID: support.RelationshipID,
			Decision:       string(domain.SupportRevoke),
			Reason:         "evidence_" + input.Action + "d",
			IdempotencyKey: "evidence-lifecycle:" + operationID + ":support:" + support.SupportID,
			Metadata: map[string]any{
				"lifecycle_operation_id": operationID,
				"action":                 input.Action,
			},
		})
		if err != nil {
			return nil, err
		}
		decisionByRelationship[support.RelationshipID] = decisionID
	}
	relationshipIDs := make([]string, 0, len(decisionByRelationship))
	for relationshipID := range decisionByRelationship {
		relationshipIDs = append(relationshipIDs, relationshipID)
	}
	sort.Strings(relationshipIDs)
	retire := make([]string, 0)
	for _, relationshipID := range relationshipIDs {
		recomputed, err := recomputeRelationshipFromEffectiveSupport(
			ctx,
			tx,
			input.TeamID,
			relationshipID,
			decisionByRelationship[relationshipID],
			"evidence_"+input.Action+"d",
		)
		if err != nil {
			return nil, err
		}
		if !relationshipSearchEligible(recomputed.After) {
			retire = append(retire, relationshipID)
		}
	}
	return retire, nil
}

func lifecycleActorProfileID(input evidenceLifecycleOperationInput) string {
	if input.ActorProfileID != "" {
		return input.ActorProfileID
	}
	return input.OwnerProfileID
}

func retireEvidenceLifecycleSearchDocuments(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
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
		  AND source_kind = ?
		  AND source_id = ANY(?::uuid[])
	`, teamID, sourceKind, pq.Array(sourceIDs)).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		UPDATE embedding_jobs
		SET status = 'stale',
		    error = 'evidence lifecycle changed before embedding completion',
		    completed_at = now(),
		    lease_until = NULL,
		    worker_id = '',
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND source_kind = ?
		  AND source_id = ANY(?::uuid[])
		  AND status IN ('queued', 'processing')
	`, teamID, sourceKind, pq.Array(sourceIDs)).Error
}
