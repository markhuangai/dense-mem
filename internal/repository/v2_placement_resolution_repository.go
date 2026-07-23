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

	"github.com/markhuangai/dense-mem/internal/domain"
)

var (
	ErrV2PlacementResolutionNotFound     = errors.New("v2 placement resolution target not found")
	ErrV2PlacementResolutionInvalidState = errors.New("v2 placement resolution invalid state")
	ErrV2PlacementResolutionStale        = errors.New("v2 placement resolution stale")
)

type V2ResolvePlacementReviewInput struct {
	TeamID               string
	OwnerProfileID       string
	ActorRole            string
	Action               string
	IngestID             string
	PlacementItemID      string
	PlacementItemVersion int
	ObservationID        string
	EntityRef            string
	CandidateEntityID    string
	PredicateKey         string
	PredicateVersion     int
	Message              string
	Evidence             []V2EvidenceInput
	IdempotencyKey       string
}

type V2ResolvePlacementReviewResult struct {
	DecisionID        string
	IngestID          string
	PlacementRunID    string
	PlacementItemID   string
	Status            string
	ImpactSummary     string
	CheckAfterSeconds int
	Existing          bool
}

type v2PlacementResolutionScope struct {
	TeamID          string
	OwnerProfileID  string
	IngestID        string
	PlacementRunID  string
	PlacementItemID string
	FragmentID      string
	ItemVersion     int
	RunStatus       string
	ItemStatus      string
	ItemCategory    string
}

func (r *V2LedgerRepositoryImpl) ResolvePlacementReview(
	ctx context.Context,
	input V2ResolvePlacementReviewInput,
) (*V2ResolvePlacementReviewResult, error) {
	input = normalizeV2ResolvePlacementReviewInput(input)
	if err := validateV2ResolvePlacementReviewInput(input); err != nil {
		return nil, err
	}
	requestHash, err := v2PlacementResolutionRequestHash(input)
	if err != nil {
		return nil, err
	}
	var result *V2ResolvePlacementReviewResult
	err = r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if existing, err := loadV2PlacementResolutionByIdempotency(ctx, tx, input, requestHash); err != nil || existing != nil {
			result = existing
			return err
		}
		scope, err := lockV2PlacementResolutionScope(ctx, tx, input)
		if err != nil {
			return err
		}
		if err := validateV2PlacementResolutionState(input, scope); err != nil {
			return err
		}
		evidenceIDs, err := appendV2PlacementResolutionEvidence(ctx, tx, input)
		if err != nil {
			return err
		}
		payload := v2PlacementResolutionPayload(input, scope, requestHash, evidenceIDs)
		outcomeID, err := insertV2PlacementOutcome(ctx, tx, V2PlacementOutcomeInput{
			TeamID:          input.TeamID,
			OwnerProfileID:  input.OwnerProfileID,
			PlacementRunID:  scope.PlacementRunID,
			PlacementItemID: scope.PlacementItemID,
			OutcomeKind:     "placement_resolution",
			Status:          v2PlacementResolutionOutcomeStatus(input.Action),
			IdempotencyKey:  input.IdempotencyKey,
			Payload:         payload,
		})
		if err != nil {
			return err
		}
		if err := applyV2PlacementResolutionState(ctx, tx, input, scope, outcomeID, payload); err != nil {
			return err
		}
		result = v2PlacementResolutionResult(input, scope, outcomeID, false)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 placement resolution: %w", err)
	}
	return result, nil
}

func normalizeV2ResolvePlacementReviewInput(input V2ResolvePlacementReviewInput) V2ResolvePlacementReviewInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.ActorRole = strings.TrimSpace(input.ActorRole)
	input.Action = strings.TrimSpace(input.Action)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.ObservationID = strings.TrimSpace(input.ObservationID)
	input.EntityRef = strings.TrimSpace(input.EntityRef)
	input.CandidateEntityID = strings.TrimSpace(input.CandidateEntityID)
	input.PredicateKey = strings.TrimSpace(input.PredicateKey)
	input.Message = strings.TrimSpace(input.Message)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	for i := range input.Evidence {
		input.Evidence[i].Content = strings.TrimSpace(input.Evidence[i].Content)
	}
	return input
}

func validateV2ResolvePlacementReviewInput(input V2ResolvePlacementReviewInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.IdempotencyKey == "" {
		return errors.New("idempotency_key is required")
	}
	switch domain.V2ResolveAction(input.Action) {
	case domain.V2ResolveAcknowledge:
		return validateV2ResolutionUUIDs(input.IngestID)
	case domain.V2ResolveSelectEntity:
		if err := validateV2ResolutionUUIDs(input.IngestID, input.PlacementItemID, input.CandidateEntityID); err != nil {
			return err
		}
		if err := validateV2PlacementItemVersion(input); err != nil {
			return err
		}
		if input.EntityRef == "" {
			return errors.New("entity_ref is required")
		}
	case domain.V2ResolveConfirmNewEntity:
		if err := validateV2ResolutionUUIDs(input.IngestID, input.PlacementItemID); err != nil {
			return err
		}
		if err := validateV2PlacementItemVersion(input); err != nil {
			return err
		}
		if input.EntityRef == "" {
			return errors.New("entity_ref is required")
		}
		if len(input.Evidence) == 0 {
			return errors.New("evidence is required")
		}
	case domain.V2ResolveSelectPredicate:
		if err := validateV2ResolutionUUIDs(input.IngestID, input.PlacementItemID, input.ObservationID); err != nil {
			return err
		}
		if err := validateV2PlacementItemVersion(input); err != nil {
			return err
		}
		if input.PredicateKey == "" {
			return errors.New("predicate_key is required")
		}
		if input.PredicateVersion < 1 {
			return errors.New("predicate_version must be greater than zero")
		}
	case domain.V2ResolveAccept, domain.V2ResolveCorrect:
		if err := validateV2ResolutionUUIDs(input.IngestID, input.PlacementItemID); err != nil {
			return err
		}
		if err := validateV2PlacementItemVersion(input); err != nil {
			return err
		}
		if len(input.Evidence) == 0 {
			return errors.New("evidence is required")
		}
	case domain.V2ResolveReject, domain.V2ResolveReleaseQuarantine:
		if err := validateV2ResolutionUUIDs(input.IngestID, input.PlacementItemID); err != nil {
			return err
		}
		if err := validateV2PlacementItemVersion(input); err != nil {
			return err
		}
		if input.Message == "" {
			return errors.New("message is required")
		}
	case domain.V2ResolveForget:
		return errors.New("forget is handled by semantic lifecycle")
	default:
		return fmt.Errorf("unsupported placement resolution action %q", input.Action)
	}
	return validateV2ResolutionEvidence(input)
}

func validateV2PlacementItemVersion(input V2ResolvePlacementReviewInput) error {
	if input.PlacementItemVersion < 1 {
		return errors.New("placement_item_version is required")
	}
	return nil
}

func validateV2ResolutionUUIDs(values ...string) error {
	for _, value := range values {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("uuid is required: %w", err)
		}
	}
	return nil
}

func validateV2ResolutionEvidence(input V2ResolvePlacementReviewInput) error {
	if len(input.Evidence) == 0 {
		return nil
	}
	normalized := normalizeV2CreateIngestInput(V2CreateIngestInput{
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		Evidence:       input.Evidence,
	})
	return validateV2CreateIngestInput(normalized)
}

func loadV2PlacementResolutionByIdempotency(
	ctx context.Context,
	tx *gorm.DB,
	input V2ResolvePlacementReviewInput,
	requestHash string,
) (*V2ResolvePlacementReviewResult, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT outcome_id::text, placement_run_id::text,
		       COALESCE(placement_item_id::text, ''), status, payload
		FROM placement_outcomes
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND idempotency_key = ?
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var outcomeID, placementRunID, placementItemID, status string
	var rawPayload []byte
	if err := rows.Scan(&outcomeID, &placementRunID, &placementItemID, &status, &rawPayload); err != nil {
		return nil, err
	}
	payload := map[string]any{}
	if len(rawPayload) > 0 {
		if err := json.Unmarshal(rawPayload, &payload); err != nil {
			return nil, fmt.Errorf("invalid placement resolution payload: %w", err)
		}
	}
	if got, _ := payload["request_hash"].(string); got != requestHash {
		return nil, fmt.Errorf("%w: placement resolution idempotency key %q already recorded with a different request", ErrV2IdempotencyConflict, input.IdempotencyKey)
	}
	return &V2ResolvePlacementReviewResult{
		DecisionID:        outcomeID,
		IngestID:          stringFromPayload(payload, "ingest_id", input.IngestID),
		PlacementRunID:    placementRunID,
		PlacementItemID:   placementItemID,
		Status:            stringFromPayload(payload, "result_status", status),
		ImpactSummary:     stringFromPayload(payload, "impact_summary", ""),
		CheckAfterSeconds: intFromPayload(payload, "check_after_seconds"),
		Existing:          true,
	}, rows.Err()
}

func lockV2PlacementResolutionScope(
	ctx context.Context,
	tx *gorm.DB,
	input V2ResolvePlacementReviewInput,
) (v2PlacementResolutionScope, error) {
	scope := v2PlacementResolutionScope{
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		IngestID:       input.IngestID,
	}
	if input.PlacementItemID == "" {
		err := tx.WithContext(ctx).Raw(`
			SELECT placement_run_id::text, status
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND ingest_id = ?::uuid
			FOR UPDATE
		`, input.TeamID, input.OwnerProfileID, input.IngestID).Row().Scan(&scope.PlacementRunID, &scope.RunStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return scope, ErrV2PlacementResolutionNotFound
		}
		return scope, err
	}
	err := tx.WithContext(ctx).Raw(`
		SELECT run.placement_run_id::text, run.status,
		       item.placement_item_id::text, item.fragment_id::text, item.version,
		       item.status, item.category
		FROM placement_runs AS run
		JOIN placement_items AS item
		  ON item.team_id = run.team_id
		 AND item.placement_run_id = run.placement_run_id
		 AND item.ingest_id = run.ingest_id
		WHERE run.team_id = ?::uuid
		  AND run.owner_profile_id = ?::uuid
		  AND run.ingest_id = ?::uuid
		  AND item.owner_profile_id = ?::uuid
		  AND item.placement_item_id = ?::uuid
		FOR UPDATE OF run, item
	`, input.TeamID, input.OwnerProfileID, input.IngestID, input.OwnerProfileID,
		input.PlacementItemID).Row().Scan(
		&scope.PlacementRunID,
		&scope.RunStatus,
		&scope.PlacementItemID,
		&scope.FragmentID,
		&scope.ItemVersion,
		&scope.ItemStatus,
		&scope.ItemCategory,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return scope, ErrV2PlacementResolutionNotFound
	}
	return scope, err
}

func validateV2PlacementResolutionState(input V2ResolvePlacementReviewInput, scope v2PlacementResolutionScope) error {
	if scope.RunStatus == string(domain.V2PlacementRunProcessing) || scope.ItemStatus == "processing" {
		return fmt.Errorf("%w: placement is currently processing", ErrV2PlacementResolutionInvalidState)
	}
	if domain.V2ResolveAction(input.Action) == domain.V2ResolveReleaseQuarantine && scope.ItemStatus != "quarantined" && scope.ItemCategory != "quarantined" {
		return fmt.Errorf("%w: placement item is not quarantined", ErrV2PlacementResolutionInvalidState)
	}
	if input.PlacementItemVersion > 0 && scope.ItemVersion != input.PlacementItemVersion {
		return fmt.Errorf("%w: placement item version %d is stale; current version is %d", ErrV2PlacementResolutionStale, input.PlacementItemVersion, scope.ItemVersion)
	}
	return nil
}

func appendV2PlacementResolutionEvidence(
	ctx context.Context,
	tx *gorm.DB,
	input V2ResolvePlacementReviewInput,
) ([]string, error) {
	if len(input.Evidence) == 0 {
		return nil, nil
	}
	normalized := normalizeV2CreateIngestInput(V2CreateIngestInput{
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		Evidence:       input.Evidence,
	})
	if err := validateV2CreateIngestInput(normalized); err != nil {
		return nil, err
	}
	var nextIndex int
	if err := tx.WithContext(ctx).Raw(`
		SELECT COALESCE(MAX(evidence_index) + 1, 0)
		FROM evidence_fragments
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid
	`, input.TeamID, input.OwnerProfileID, input.IngestID).Scan(&nextIndex).Error; err != nil {
		return nil, err
	}
	sources := make(map[string]V2SourceRevisionResult)
	fragmentIDs := make([]string, 0, len(normalized.Evidence))
	for i, item := range normalized.Evidence {
		var source *V2SourceRevisionResult
		if item.SourceKey != "" {
			advanced, err := advanceV2SourceRevisionInTx(ctx, tx, V2AdvanceSourceRevisionInput{
				TeamID:                        input.TeamID,
				OwnerProfileID:                input.OwnerProfileID,
				SourceKey:                     item.SourceKey,
				SourceKind:                    v2SourceKindForEvidence(item.SourceType),
				Authority:                     item.Authority,
				RevisionToken:                 item.SourceRevisionToken,
				ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken,
				ContentHash:                   item.SourceRevisionContentHash,
				Envelope:                      item.SourceRevisionEnvelope,
			}, sources)
			if err != nil {
				return nil, err
			}
			source = advanced
		}
		fragment, err := insertV2EvidenceFragment(ctx, tx, normalized, input.IngestID, nextIndex+i, item, source)
		if err != nil {
			return nil, err
		}
		fragmentIDs = append(fragmentIDs, fragment.FragmentID)
		if item.InitialEvent != nil {
			eventInput := V2SecurityEventInput{
				TeamID:               input.TeamID,
				OwnerProfileID:       input.OwnerProfileID,
				IngestID:             input.IngestID,
				FragmentID:           fragment.FragmentID,
				V2SecurityEventDraft: *item.InitialEvent,
			}
			if _, err := insertV2SecurityEvent(ctx, tx, eventInput); err != nil {
				return nil, err
			}
			if item.InitialEvent.Decision == "quarantine" {
				if err := insertV2EvidenceQuarantine(ctx, tx, normalized, input.IngestID, fragment.FragmentID, item.InitialEvent.Reason); err != nil {
					return nil, err
				}
			}
		}
	}
	return fragmentIDs, nil
}

func applyV2PlacementResolutionState(
	ctx context.Context,
	tx *gorm.DB,
	input V2ResolvePlacementReviewInput,
	scope v2PlacementResolutionScope,
	outcomeID string,
	payload map[string]any,
) error {
	resolution := v2PlacementResolutionReviewTaskPayload(input, outcomeID)
	switch domain.V2ResolveAction(input.Action) {
	case domain.V2ResolveAcknowledge:
		return updateV2ReviewTasksForResolution(ctx, tx, input, "acknowledged", resolution)
	case domain.V2ResolveReject:
		if err := updateV2ReviewTasksForResolution(ctx, tx, input, "resolved", resolution); err != nil {
			return err
		}
		if err := updateV2PlacementItemForResolution(ctx, tx, scope, "completed", "candidate", payload); err != nil {
			return err
		}
		return finishV2PlacementRunAfterUserResolution(ctx, tx, scope)
	case domain.V2ResolveReleaseQuarantine:
		if err := releaseV2PlacementQuarantine(ctx, tx, input, scope); err != nil {
			return err
		}
		if err := updateV2ReviewTasksForResolution(ctx, tx, input, "resolved", resolution); err != nil {
			return err
		}
		if err := updateV2PlacementItemForResolution(ctx, tx, scope, "queued", "pending", payload); err != nil {
			return err
		}
		return requeueV2PlacementRunForUserResolution(ctx, tx, scope, string(domain.V2PlacementRunGuarded))
	default:
		if err := updateV2ReviewTasksForResolution(ctx, tx, input, "resolved", resolution); err != nil {
			return err
		}
		switch v2PlacementResolutionSecurityDecision(input) {
		case "quarantine":
			if err := updateV2PlacementItemForResolution(ctx, tx, scope, "quarantined", "quarantined", payload); err != nil {
				return err
			}
			return quarantineV2PlacementRunForUserResolution(ctx, tx, scope)
		case "guarded":
			if err := updateV2PlacementItemForResolution(ctx, tx, scope, "queued", "pending", payload); err != nil {
				return err
			}
			return requeueV2PlacementRunForUserResolution(ctx, tx, scope, string(domain.V2PlacementRunGuarded))
		}
		if err := updateV2PlacementItemForResolution(ctx, tx, scope, "queued", "pending", payload); err != nil {
			return err
		}
		return requeueV2PlacementRunForUserResolution(ctx, tx, scope, string(domain.V2PlacementRunQueued))
	}
}

func updateV2ReviewTasksForResolution(
	ctx context.Context,
	tx *gorm.DB,
	input V2ResolvePlacementReviewInput,
	status string,
	resolution map[string]any,
) error {
	resolutionJSON, err := marshalV2JSON(resolution)
	if err != nil {
		return err
	}
	resolvedAt := "NULL"
	if status == "resolved" || status == "canceled" {
		resolvedAt = "now()"
	}
	query := fmt.Sprintf(`
		UPDATE review_tasks
		SET status = ?,
		    resolution = ?::jsonb,
		    resolved_at = %s,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND status IN ('open', 'acknowledged')
	`, resolvedAt)
	args := []any{status, string(resolutionJSON), input.TeamID, input.OwnerProfileID}
	if input.IngestID != "" {
		query += ` AND ingest_id = ?::uuid`
		args = append(args, input.IngestID)
	}
	if input.PlacementItemID != "" {
		query += ` AND placement_item_id = ?::uuid`
		args = append(args, input.PlacementItemID)
	}
	if input.ObservationID != "" {
		query += ` AND observation_id = ?::uuid`
		args = append(args, input.ObservationID)
	}
	return tx.WithContext(ctx).Exec(query, args...).Error
}

func updateV2PlacementItemForResolution(
	ctx context.Context,
	tx *gorm.DB,
	scope v2PlacementResolutionScope,
	status string,
	category string,
	payload map[string]any,
) error {
	payloadJSON, err := marshalV2JSON(payload)
	if err != nil {
		return err
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE placement_items
		SET status = ?,
		    category = ?,
		    result = ?::jsonb,
		    version = version + 1,
		    error = '',
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_item_id = ?::uuid
	`, status, category, string(payloadJSON), scope.TeamID, scope.OwnerProfileID, scope.PlacementItemID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrV2PlacementResolutionNotFound
	}
	return nil
}

func releaseV2PlacementQuarantine(
	ctx context.Context,
	tx *gorm.DB,
	input V2ResolvePlacementReviewInput,
	scope v2PlacementResolutionScope,
) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE evidence_quarantines
		SET status = 'released',
		    released_by_profile_id = ?::uuid,
		    release_reason = ?,
		    released_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND fragment_id = ?::uuid
		  AND status = 'active'
	`, input.OwnerProfileID, input.Message, scope.TeamID, scope.OwnerProfileID, scope.FragmentID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: active quarantine not found", ErrV2PlacementResolutionInvalidState)
	}
	_, err := insertV2SecurityEvent(ctx, tx, V2SecurityEventInput{
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		IngestID:       input.IngestID,
		FragmentID:     scope.FragmentID,
		V2SecurityEventDraft: V2SecurityEventDraft{
			EventKind:      "quarantine_release",
			Decision:       "released",
			ScanPolicyHash: "manual-review",
			Reason:         input.Message,
			Metadata: map[string]any{
				"actor_role": input.ActorRole,
			},
		},
	})
	return err
}

func requeueV2PlacementRunForUserResolution(
	ctx context.Context,
	tx *gorm.DB,
	scope v2PlacementResolutionScope,
	status string,
) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE placement_runs
		SET status = ?,
		    error = '',
		    available_at = now(),
		    lease_until = NULL,
		    worker_id = '',
		    completed_at = NULL,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND status <> 'processing'
	`, status, scope.TeamID, scope.OwnerProfileID, scope.PlacementRunID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrV2PlacementResolutionInvalidState
	}
	return nil
}

func quarantineV2PlacementRunForUserResolution(ctx context.Context, tx *gorm.DB, scope v2PlacementResolutionScope) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE placement_runs
		SET status = 'quarantined',
		    error = '',
		    lease_until = NULL,
		    worker_id = '',
		    completed_at = now(),
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND status <> 'processing'
	`, scope.TeamID, scope.OwnerProfileID, scope.PlacementRunID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrV2PlacementResolutionInvalidState
	}
	return nil
}

func finishV2PlacementRunAfterUserResolution(ctx context.Context, tx *gorm.DB, scope v2PlacementResolutionScope) error {
	var openCount, reviewCount int64
	if err := tx.WithContext(ctx).Raw(`
		SELECT
		    COUNT(*) FILTER (WHERE status IN ('queued', 'processing')),
		    COUNT(*) FILTER (WHERE status = 'awaiting_review')
		FROM placement_items
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
	`, scope.TeamID, scope.PlacementRunID).Row().Scan(&openCount, &reviewCount); err != nil {
		return err
	}
	if openCount > 0 {
		return nil
	}
	runStatus := string(domain.V2PlacementRunCompleted)
	if reviewCount > 0 {
		runStatus = string(domain.V2PlacementRunAwaitingReview)
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE placement_runs
		SET status = ?,
		    error = '',
		    lease_until = NULL,
		    worker_id = '',
		    completed_at = now(),
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND status <> 'processing'
	`, runStatus, scope.TeamID, scope.OwnerProfileID, scope.PlacementRunID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrV2PlacementResolutionInvalidState
	}
	return nil
}

func v2PlacementResolutionPayload(
	input V2ResolvePlacementReviewInput,
	scope v2PlacementResolutionScope,
	requestHash string,
	evidenceIDs []string,
) map[string]any {
	impact := v2PlacementResolutionImpactSummary(input.Action, scope)
	payload := map[string]any{
		"contract_version":       domain.V2ContractVersion,
		"action":                 input.Action,
		"request_hash":           requestHash,
		"ingest_id":              input.IngestID,
		"placement_item_id":      input.PlacementItemID,
		"placement_item_version": input.PlacementItemVersion,
		"observation_id":         input.ObservationID,
		"entity_ref":             input.EntityRef,
		"candidate_entity_id":    input.CandidateEntityID,
		"predicate_key":          input.PredicateKey,
		"predicate_version":      input.PredicateVersion,
		"message":                input.Message,
		"actor_role":             input.ActorRole,
		"evidence_fragment_ids":  append([]string(nil), evidenceIDs...),
		"result_status":          v2PlacementResolutionResultStatusForInput(input),
		"impact_summary":         impact,
		"check_after_seconds":    v2PlacementResolutionCheckAfterSecondsForInput(input),
	}
	return payload
}

func v2PlacementResolutionReviewTaskPayload(input V2ResolvePlacementReviewInput, outcomeID string) map[string]any {
	return map[string]any{
		"contract_version":    domain.V2ContractVersion,
		"action":              input.Action,
		"outcome_id":          outcomeID,
		"entity_ref":          input.EntityRef,
		"candidate_entity_id": input.CandidateEntityID,
		"predicate_key":       input.PredicateKey,
		"predicate_version":   input.PredicateVersion,
		"message":             input.Message,
	}
}

func v2PlacementResolutionRequestHash(input V2ResolvePlacementReviewInput) (string, error) {
	evidenceHashes := make([]string, 0, len(input.Evidence))
	for _, item := range normalizeV2CreateIngestInput(V2CreateIngestInput{Evidence: input.Evidence}).Evidence {
		evidenceHashes = append(evidenceHashes, item.ContentHash)
	}
	payload := map[string]any{
		"action":                 input.Action,
		"ingest_id":              input.IngestID,
		"placement_item_id":      input.PlacementItemID,
		"placement_item_version": input.PlacementItemVersion,
		"observation_id":         input.ObservationID,
		"entity_ref":             input.EntityRef,
		"candidate_entity_id":    input.CandidateEntityID,
		"predicate_key":          input.PredicateKey,
		"predicate_version":      input.PredicateVersion,
		"message":                input.Message,
		"evidence_hashes":        evidenceHashes,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("placement resolution request hash: %w", err)
	}
	return sha256Hex(string(data)), nil
}

func v2PlacementResolutionOutcomeStatus(action string) string {
	switch domain.V2ResolveAction(action) {
	case domain.V2ResolveAcknowledge:
		return "acknowledged"
	case domain.V2ResolveSelectEntity:
		return "entity_selected"
	case domain.V2ResolveConfirmNewEntity:
		return "new_entity_confirmed"
	case domain.V2ResolveSelectPredicate:
		return "predicate_selected"
	case domain.V2ResolveAccept:
		return "accepted"
	case domain.V2ResolveReject:
		return "rejected"
	case domain.V2ResolveCorrect:
		return "correction_submitted"
	case domain.V2ResolveReleaseQuarantine:
		return "quarantine_released"
	default:
		return action
	}
}

func v2PlacementResolutionResultStatus(action string) string {
	return v2PlacementResolutionResultStatusForInput(V2ResolvePlacementReviewInput{Action: action})
}

func v2PlacementResolutionResultStatusForInput(input V2ResolvePlacementReviewInput) string {
	switch domain.V2ResolveAction(input.Action) {
	case domain.V2ResolveAcknowledge:
		return "acknowledged"
	case domain.V2ResolveReject:
		return string(domain.V2PlacementRunCompleted)
	case domain.V2ResolveReleaseQuarantine:
		return string(domain.V2PlacementRunGuarded)
	default:
		switch v2PlacementResolutionSecurityDecision(input) {
		case "quarantine":
			return string(domain.V2PlacementRunQuarantined)
		case "guarded":
			return string(domain.V2PlacementRunGuarded)
		}
		return string(domain.V2PlacementRunQueued)
	}
}

func v2PlacementResolutionCheckAfterSeconds(action string) int {
	return v2PlacementResolutionCheckAfterSecondsForInput(V2ResolvePlacementReviewInput{Action: action})
}

func v2PlacementResolutionCheckAfterSecondsForInput(input V2ResolvePlacementReviewInput) int {
	switch domain.V2ResolveAction(input.Action) {
	case domain.V2ResolveAcknowledge, domain.V2ResolveReject:
		return 0
	default:
		if v2PlacementResolutionSecurityDecision(input) == "quarantine" {
			return 0
		}
		return 60
	}
}

func v2PlacementResolutionImpactSummary(action string, scope v2PlacementResolutionScope) string {
	switch domain.V2ResolveAction(action) {
	case domain.V2ResolveAcknowledge:
		return fmt.Sprintf("placement ingest %s review was acknowledged", scope.IngestID)
	case domain.V2ResolveReject:
		return fmt.Sprintf("placement item %s was rejected without semantic commit", scope.PlacementItemID)
	case domain.V2ResolveReleaseQuarantine:
		return fmt.Sprintf("placement item %s quarantine was released and queued for guarded review", scope.PlacementItemID)
	default:
		return fmt.Sprintf("placement item %s resolution was recorded and queued for semantic review", scope.PlacementItemID)
	}
}

func v2PlacementResolutionResult(
	input V2ResolvePlacementReviewInput,
	scope v2PlacementResolutionScope,
	outcomeID string,
	existing bool,
) *V2ResolvePlacementReviewResult {
	return &V2ResolvePlacementReviewResult{
		DecisionID:        outcomeID,
		IngestID:          input.IngestID,
		PlacementRunID:    scope.PlacementRunID,
		PlacementItemID:   scope.PlacementItemID,
		Status:            v2PlacementResolutionResultStatusForInput(input),
		ImpactSummary:     v2PlacementResolutionImpactSummary(input.Action, scope),
		CheckAfterSeconds: v2PlacementResolutionCheckAfterSecondsForInput(input),
		Existing:          existing,
	}
}

func v2PlacementResolutionSecurityDecision(input V2ResolvePlacementReviewInput) string {
	decision := "pass"
	for _, item := range input.Evidence {
		if item.InitialEvent == nil {
			continue
		}
		switch item.InitialEvent.Decision {
		case "quarantine":
			return "quarantine"
		case "guarded":
			decision = "guarded"
		}
	}
	return decision
}

func stringFromPayload(payload map[string]any, key, fallback string) string {
	value, ok := payload[key].(string)
	if !ok || value == "" {
		return fallback
	}
	return value
}

func intFromPayload(payload map[string]any, key string) int {
	value, ok := payload[key].(float64)
	if !ok {
		return 0
	}
	return int(value)
}
