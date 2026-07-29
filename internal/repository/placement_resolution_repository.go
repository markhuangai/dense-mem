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
	ErrPlacementResolutionNotFound     = errors.New("placement resolution target not found")
	ErrPlacementResolutionInvalidState = errors.New("placement resolution invalid state")
	ErrPlacementResolutionStale        = errors.New("placement resolution stale")
)

type ResolvePlacementReviewInput struct {
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
	Evidence             []EvidenceInput
	IdempotencyKey       string
}

type ResolvePlacementReviewResult struct {
	DecisionID        string
	IngestID          string
	PlacementRunID    string
	PlacementItemID   string
	Status            string
	ImpactSummary     string
	CheckAfterSeconds int
	Existing          bool
}

type placementResolutionScope struct {
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

func (r *LedgerRepositoryImpl) ResolvePlacementReview(
	ctx context.Context,
	input ResolvePlacementReviewInput,
) (*ResolvePlacementReviewResult, error) {
	input = normalizeResolvePlacementReviewInput(input)
	if err := validateResolvePlacementReviewInput(input); err != nil {
		return nil, err
	}
	requestHash, err := placementResolutionRequestHash(input)
	if err != nil {
		return nil, err
	}
	var result *ResolvePlacementReviewResult
	err = r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if existing, err := loadPlacementResolutionByIdempotency(ctx, tx, input, requestHash); err != nil || existing != nil {
			result = existing
			return err
		}
		scope, err := lockPlacementResolutionScope(ctx, tx, input)
		if err != nil {
			return err
		}
		if err := validatePlacementResolutionState(input, scope); err != nil {
			return err
		}
		evidenceIDs, err := appendPlacementResolutionEvidence(ctx, tx, input)
		if err != nil {
			return err
		}
		payload := placementResolutionPayload(input, scope, requestHash, evidenceIDs)
		outcomeID, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
			TeamID:          input.TeamID,
			OwnerProfileID:  input.OwnerProfileID,
			PlacementRunID:  scope.PlacementRunID,
			PlacementItemID: scope.PlacementItemID,
			OutcomeKind:     "placement_resolution",
			Status:          placementResolutionOutcomeStatus(input.Action),
			IdempotencyKey:  input.IdempotencyKey,
			Payload:         payload,
		})
		if err != nil {
			return err
		}
		if err := applyPlacementResolutionState(ctx, tx, input, scope, outcomeID, payload); err != nil {
			return err
		}
		result = placementResolutionResult(input, scope, outcomeID, false)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("placement resolution: %w", err)
	}
	return result, nil
}

func normalizeResolvePlacementReviewInput(input ResolvePlacementReviewInput) ResolvePlacementReviewInput {
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

func validateResolvePlacementReviewInput(input ResolvePlacementReviewInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.IdempotencyKey == "" {
		return errors.New("idempotency_key is required")
	}
	switch domain.ResolveAction(input.Action) {
	case domain.ResolveAcknowledge:
		return validateResolutionUUIDs(input.IngestID)
	case domain.ResolveSelectEntity:
		if err := validateResolutionUUIDs(input.IngestID, input.PlacementItemID, input.CandidateEntityID); err != nil {
			return err
		}
		if err := validatePlacementItemVersion(input); err != nil {
			return err
		}
		if input.EntityRef == "" {
			return errors.New("entity_ref is required")
		}
	case domain.ResolveConfirmNewEntity:
		if err := validateResolutionUUIDs(input.IngestID, input.PlacementItemID); err != nil {
			return err
		}
		if err := validatePlacementItemVersion(input); err != nil {
			return err
		}
		if input.EntityRef == "" {
			return errors.New("entity_ref is required")
		}
		if len(input.Evidence) == 0 {
			return errors.New("evidence is required")
		}
	case domain.ResolveSelectPredicate:
		if err := validateResolutionUUIDs(input.IngestID, input.PlacementItemID, input.ObservationID); err != nil {
			return err
		}
		if err := validatePlacementItemVersion(input); err != nil {
			return err
		}
		if input.PredicateKey == "" {
			return errors.New("predicate_key is required")
		}
		if input.PredicateVersion < 1 {
			return errors.New("predicate_version must be greater than zero")
		}
	case domain.ResolveAccept, domain.ResolveCorrect:
		if err := validateResolutionUUIDs(input.IngestID, input.PlacementItemID); err != nil {
			return err
		}
		if err := validatePlacementItemVersion(input); err != nil {
			return err
		}
		if len(input.Evidence) == 0 {
			return errors.New("evidence is required")
		}
	case domain.ResolveReject, domain.ResolveReleaseQuarantine:
		if err := validateResolutionUUIDs(input.IngestID, input.PlacementItemID); err != nil {
			return err
		}
		if err := validatePlacementItemVersion(input); err != nil {
			return err
		}
		if input.Message == "" {
			return errors.New("message is required")
		}
	case domain.ResolveForget:
		return errors.New("forget is handled by semantic lifecycle")
	default:
		return fmt.Errorf("unsupported placement resolution action %q", input.Action)
	}
	return validateResolutionEvidence(input)
}

func validatePlacementItemVersion(input ResolvePlacementReviewInput) error {
	if input.PlacementItemVersion < 1 {
		return errors.New("placement_item_version is required")
	}
	return nil
}

func validateResolutionUUIDs(values ...string) error {
	for _, value := range values {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("uuid is required: %w", err)
		}
	}
	return nil
}

func validateResolutionEvidence(input ResolvePlacementReviewInput) error {
	if len(input.Evidence) == 0 {
		return nil
	}
	normalized := normalizeCreateIngestInput(CreateIngestInput{
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		Evidence:       input.Evidence,
	})
	return validateCreateIngestInput(normalized)
}

func loadPlacementResolutionByIdempotency(
	ctx context.Context,
	tx *gorm.DB,
	input ResolvePlacementReviewInput,
	requestHash string,
) (*ResolvePlacementReviewResult, error) {
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
		return nil, fmt.Errorf("%w: placement resolution idempotency key %q already recorded with a different request", ErrIdempotencyConflict, input.IdempotencyKey)
	}
	return &ResolvePlacementReviewResult{
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

func lockPlacementResolutionScope(
	ctx context.Context,
	tx *gorm.DB,
	input ResolvePlacementReviewInput,
) (placementResolutionScope, error) {
	scope := placementResolutionScope{
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
			return scope, ErrPlacementResolutionNotFound
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
		return scope, ErrPlacementResolutionNotFound
	}
	return scope, err
}

func validatePlacementResolutionState(input ResolvePlacementReviewInput, scope placementResolutionScope) error {
	if scope.RunStatus == string(domain.PlacementRunProcessing) || scope.ItemStatus == "processing" {
		return fmt.Errorf("%w: placement is currently processing", ErrPlacementResolutionInvalidState)
	}
	if domain.ResolveAction(input.Action) == domain.ResolveReleaseQuarantine && scope.ItemStatus != "quarantined" && scope.ItemCategory != "quarantined" {
		return fmt.Errorf("%w: placement item is not quarantined", ErrPlacementResolutionInvalidState)
	}
	if input.PlacementItemVersion > 0 && scope.ItemVersion != input.PlacementItemVersion {
		return fmt.Errorf("%w: placement item version %d is stale; current version is %d", ErrPlacementResolutionStale, input.PlacementItemVersion, scope.ItemVersion)
	}
	return nil
}

func appendPlacementResolutionEvidence(
	ctx context.Context,
	tx *gorm.DB,
	input ResolvePlacementReviewInput,
) ([]string, error) {
	if len(input.Evidence) == 0 {
		return nil, nil
	}
	normalized := normalizeCreateIngestInput(CreateIngestInput{
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		Evidence:       input.Evidence,
	})
	if err := validateCreateIngestInput(normalized); err != nil {
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
	sources := make(map[string]SourceRevisionResult)
	fragmentIDs := make([]string, 0, len(normalized.Evidence))
	for i, item := range normalized.Evidence {
		var source *SourceRevisionResult
		if item.SourceKey != "" {
			advanced, err := advanceSourceRevisionInTx(ctx, tx, AdvanceSourceRevisionInput{
				TeamID:                        input.TeamID,
				OwnerProfileID:                input.OwnerProfileID,
				SourceKey:                     item.SourceKey,
				SourceKind:                    sourceKindForEvidence(item.SourceType),
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
		fragment, err := insertEvidenceFragment(ctx, tx, normalized, input.IngestID, nextIndex+i, item, source)
		if err != nil {
			return nil, err
		}
		fragmentIDs = append(fragmentIDs, fragment.FragmentID)
		if item.InitialEvent != nil {
			eventInput := SecurityEventInput{
				TeamID:             input.TeamID,
				OwnerProfileID:     input.OwnerProfileID,
				IngestID:           input.IngestID,
				FragmentID:         fragment.FragmentID,
				SecurityEventDraft: *item.InitialEvent,
			}
			if _, err := insertSecurityEvent(ctx, tx, eventInput); err != nil {
				return nil, err
			}
			if item.InitialEvent.Decision == "quarantine" {
				if err := insertEvidenceQuarantine(ctx, tx, normalized, input.IngestID, fragment.FragmentID, item.InitialEvent.Reason); err != nil {
					return nil, err
				}
			}
		}
	}
	return fragmentIDs, nil
}

func applyPlacementResolutionState(
	ctx context.Context,
	tx *gorm.DB,
	input ResolvePlacementReviewInput,
	scope placementResolutionScope,
	outcomeID string,
	payload map[string]any,
) error {
	resolution := placementResolutionReviewTaskPayload(input, outcomeID)
	switch domain.ResolveAction(input.Action) {
	case domain.ResolveAcknowledge:
		return updateReviewTasksForResolution(ctx, tx, input, "acknowledged", resolution)
	case domain.ResolveReject:
		if err := updateReviewTasksForResolution(ctx, tx, input, "resolved", resolution); err != nil {
			return err
		}
		if err := updatePlacementItemForResolution(ctx, tx, scope, "completed", "candidate", payload); err != nil {
			return err
		}
		return finishPlacementRunAfterUserResolution(ctx, tx, scope)
	case domain.ResolveReleaseQuarantine:
		if err := releasePlacementQuarantine(ctx, tx, input, scope); err != nil {
			return err
		}
		if err := updateReviewTasksForResolution(ctx, tx, input, "resolved", resolution); err != nil {
			return err
		}
		if err := updatePlacementItemForResolution(ctx, tx, scope, "queued", "pending", payload); err != nil {
			return err
		}
		return requeuePlacementRunForUserResolution(ctx, tx, scope, string(domain.PlacementRunGuarded))
	default:
		if err := updateReviewTasksForResolution(ctx, tx, input, "resolved", resolution); err != nil {
			return err
		}
		switch placementResolutionSecurityDecision(input) {
		case "quarantine":
			if err := updatePlacementItemForResolution(ctx, tx, scope, "quarantined", "quarantined", payload); err != nil {
				return err
			}
			return quarantinePlacementRunForUserResolution(ctx, tx, scope)
		case "guarded":
			if err := updatePlacementItemForResolution(ctx, tx, scope, "queued", "pending", payload); err != nil {
				return err
			}
			return requeuePlacementRunForUserResolution(ctx, tx, scope, string(domain.PlacementRunGuarded))
		}
		if err := updatePlacementItemForResolution(ctx, tx, scope, "queued", "pending", payload); err != nil {
			return err
		}
		return requeuePlacementRunForUserResolution(ctx, tx, scope, string(domain.PlacementRunQueued))
	}
}

func updateReviewTasksForResolution(
	ctx context.Context,
	tx *gorm.DB,
	input ResolvePlacementReviewInput,
	status string,
	resolution map[string]any,
) error {
	resolutionJSON, err := marshalJSON(resolution)
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

func updatePlacementItemForResolution(
	ctx context.Context,
	tx *gorm.DB,
	scope placementResolutionScope,
	status string,
	category string,
	payload map[string]any,
) error {
	payloadJSON, err := marshalJSON(payload)
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
		return ErrPlacementResolutionNotFound
	}
	return nil
}

func releasePlacementQuarantine(
	ctx context.Context,
	tx *gorm.DB,
	input ResolvePlacementReviewInput,
	scope placementResolutionScope,
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
		return fmt.Errorf("%w: active quarantine not found", ErrPlacementResolutionInvalidState)
	}
	_, err := insertSecurityEvent(ctx, tx, SecurityEventInput{
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		IngestID:       input.IngestID,
		FragmentID:     scope.FragmentID,
		SecurityEventDraft: SecurityEventDraft{
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

func requeuePlacementRunForUserResolution(
	ctx context.Context,
	tx *gorm.DB,
	scope placementResolutionScope,
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
		return ErrPlacementResolutionInvalidState
	}
	return nil
}

func quarantinePlacementRunForUserResolution(ctx context.Context, tx *gorm.DB, scope placementResolutionScope) error {
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
		return ErrPlacementResolutionInvalidState
	}
	return nil
}

func finishPlacementRunAfterUserResolution(ctx context.Context, tx *gorm.DB, scope placementResolutionScope) error {
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
	runStatus := string(domain.PlacementRunCompleted)
	if reviewCount > 0 {
		runStatus = string(domain.PlacementRunAwaitingReview)
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
		return ErrPlacementResolutionInvalidState
	}
	return nil
}

func placementResolutionPayload(
	input ResolvePlacementReviewInput,
	scope placementResolutionScope,
	requestHash string,
	evidenceIDs []string,
) map[string]any {
	impact := placementResolutionImpactSummary(input.Action, scope)
	payload := map[string]any{
		"contract_version":       domain.ContractVersion,
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
		"result_status":          placementResolutionResultStatusForInput(input),
		"impact_summary":         impact,
		"check_after_seconds":    placementResolutionCheckAfterSecondsForInput(input),
	}
	return payload
}

func placementResolutionReviewTaskPayload(input ResolvePlacementReviewInput, outcomeID string) map[string]any {
	return map[string]any{
		"contract_version":    domain.ContractVersion,
		"action":              input.Action,
		"outcome_id":          outcomeID,
		"entity_ref":          input.EntityRef,
		"candidate_entity_id": input.CandidateEntityID,
		"predicate_key":       input.PredicateKey,
		"predicate_version":   input.PredicateVersion,
		"message":             input.Message,
	}
}

func placementResolutionRequestHash(input ResolvePlacementReviewInput) (string, error) {
	evidenceHashes := make([]string, 0, len(input.Evidence))
	for _, item := range normalizeCreateIngestInput(CreateIngestInput{Evidence: input.Evidence}).Evidence {
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

func placementResolutionOutcomeStatus(action string) string {
	switch domain.ResolveAction(action) {
	case domain.ResolveAcknowledge:
		return "acknowledged"
	case domain.ResolveSelectEntity:
		return "entity_selected"
	case domain.ResolveConfirmNewEntity:
		return "new_entity_confirmed"
	case domain.ResolveSelectPredicate:
		return "predicate_selected"
	case domain.ResolveAccept:
		return "accepted"
	case domain.ResolveReject:
		return "rejected"
	case domain.ResolveCorrect:
		return "correction_submitted"
	case domain.ResolveReleaseQuarantine:
		return "quarantine_released"
	default:
		return action
	}
}

func placementResolutionResultStatus(action string) string {
	return placementResolutionResultStatusForInput(ResolvePlacementReviewInput{Action: action})
}

func placementResolutionResultStatusForInput(input ResolvePlacementReviewInput) string {
	switch domain.ResolveAction(input.Action) {
	case domain.ResolveAcknowledge:
		return "acknowledged"
	case domain.ResolveReject:
		return string(domain.PlacementRunCompleted)
	case domain.ResolveReleaseQuarantine:
		return string(domain.PlacementRunGuarded)
	default:
		switch placementResolutionSecurityDecision(input) {
		case "quarantine":
			return string(domain.PlacementRunQuarantined)
		case "guarded":
			return string(domain.PlacementRunGuarded)
		}
		return string(domain.PlacementRunQueued)
	}
}

func placementResolutionCheckAfterSeconds(action string) int {
	return placementResolutionCheckAfterSecondsForInput(ResolvePlacementReviewInput{Action: action})
}

func placementResolutionCheckAfterSecondsForInput(input ResolvePlacementReviewInput) int {
	switch domain.ResolveAction(input.Action) {
	case domain.ResolveAcknowledge, domain.ResolveReject:
		return 0
	default:
		if placementResolutionSecurityDecision(input) == "quarantine" {
			return 0
		}
		return 60
	}
}

func placementResolutionImpactSummary(action string, scope placementResolutionScope) string {
	switch domain.ResolveAction(action) {
	case domain.ResolveAcknowledge:
		return fmt.Sprintf("placement ingest %s review was acknowledged", scope.IngestID)
	case domain.ResolveReject:
		return fmt.Sprintf("placement item %s was rejected without semantic commit", scope.PlacementItemID)
	case domain.ResolveReleaseQuarantine:
		return fmt.Sprintf("placement item %s quarantine was released and queued for guarded review", scope.PlacementItemID)
	default:
		return fmt.Sprintf("placement item %s resolution was recorded and queued for semantic review", scope.PlacementItemID)
	}
}

func placementResolutionResult(
	input ResolvePlacementReviewInput,
	scope placementResolutionScope,
	outcomeID string,
	existing bool,
) *ResolvePlacementReviewResult {
	return &ResolvePlacementReviewResult{
		DecisionID:        outcomeID,
		IngestID:          input.IngestID,
		PlacementRunID:    scope.PlacementRunID,
		PlacementItemID:   scope.PlacementItemID,
		Status:            placementResolutionResultStatusForInput(input),
		ImpactSummary:     placementResolutionImpactSummary(input.Action, scope),
		CheckAfterSeconds: placementResolutionCheckAfterSecondsForInput(input),
		Existing:          existing,
	}
}

func placementResolutionSecurityDecision(input ResolvePlacementReviewInput) string {
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
