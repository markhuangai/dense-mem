package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type CompletePlacementReviewInput struct {
	TeamID           string
	OwnerProfileID   string
	IngestID         string
	PlacementRunID   string
	PlacementItemID  string
	WorkerID         string
	ExpectedAttempts int
	OutcomeKind      string
	Status           string
	Category         string
	Payload          map[string]any
}

type CompletePlacementReviewResult struct {
	Status           string
	OutcomeID        string
	FirstDisposition *PlacementFirstDisposition
}

type RequeuePlacementReviewInput struct {
	TeamID           string
	OwnerProfileID   string
	IngestID         string
	PlacementRunID   string
	PlacementItemID  string
	WorkerID         string
	ExpectedAttempts int
	OutcomeKind      string
	Payload          map[string]any
	RetryAfter       time.Duration
	// ReleaseAssessorAttempt releases a known failed assessor conversation only
	// as part of this lease-bound requeue transaction.
	ReleaseAssessorAttempt bool
}

type RequeuePlacementReviewResult struct {
	Status           string
	OutcomeID        string
	FirstDisposition *PlacementFirstDisposition
}

func (r *LedgerRepositoryImpl) CompletePlacementReviewResult(
	ctx context.Context,
	input CompletePlacementReviewInput,
) (*CompletePlacementReviewResult, error) {
	input = normalizeCompletePlacementReviewInput(input)
	if err := validateCompletePlacementReviewInput(input); err != nil {
		return nil, err
	}
	scope := placementCommitScope(input)
	result := &CompletePlacementReviewResult{Status: input.Status}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockPlacementRunForCommit(ctx, tx, scope); err != nil {
			return err
		}
		if err := lockPlacementItemForCommit(ctx, tx, scope); err != nil {
			return err
		}
		if err := ensurePlacementItemCurrent(ctx, tx, scope); err != nil {
			if errors.Is(err, ErrPlacementStaleSource) {
				outcomeID, outcomeErr := appendSupersededPlacementOutcome(ctx, tx, scope)
				if outcomeErr != nil {
					return outcomeErr
				}
				firstDisposition, finishErr := finishPlacementRunIfTerminal(ctx, tx, scope, string(domain.PlacementRunFailed))
				if finishErr != nil {
					return finishErr
				}
				result.FirstDisposition = firstDisposition
				result.Status = "superseded"
				result.OutcomeID = outcomeID
				return nil
			}
			return err
		}
		itemStatus, category, runStatus := terminalPlacementStatuses(input.Status, input.Category)
		payload := terminalPlacementPayload(input.Payload, input.Status)
		if placementEvidenceSearchableStatus(input.Status) {
			placementFragmentID, err := loadPlacementItemFragmentID(ctx, tx, scope)
			if err != nil {
				return err
			}
			deletionOnly, err := isConflictResolutionDeletionOnlyFragment(ctx, tx, input.TeamID, placementFragmentID)
			if err != nil {
				return err
			}
			if !deletionOnly {
				if _, err := upsertPlacementItemEvidenceSearchDocument(
					ctx,
					tx,
					scope,
					placementFragmentID,
					r.embeddingJobMaxAttempts,
				); err != nil {
					return err
				}
			} else {
				payload["conflict_resolution_deletion_only"] = true
				payload["semantic_projection"] = "not_allowed"
			}
		}
		outcomeID, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
			TeamID:          input.TeamID,
			OwnerProfileID:  input.OwnerProfileID,
			PlacementRunID:  input.PlacementRunID,
			PlacementItemID: input.PlacementItemID,
			OutcomeKind:     input.OutcomeKind,
			Status:          input.Status,
			Payload:         payload,
		})
		if err != nil {
			return err
		}
		if err := updatePlacementItemOutcome(ctx, tx, PlacementOutcomeInput{
			TeamID:             input.TeamID,
			OwnerProfileID:     input.OwnerProfileID,
			PlacementItemID:    input.PlacementItemID,
			UpdateItemStatus:   itemStatus,
			UpdateItemCategory: category,
			Payload:            payload,
		}); err != nil {
			return err
		}
		firstDisposition, err := finishPlacementRunIfTerminal(ctx, tx, scope, runStatus)
		if err != nil {
			return err
		}
		result.FirstDisposition = firstDisposition
		result.OutcomeID = outcomeID
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("placement review completion: %w", err)
	}
	return result, nil
}

func (r *LedgerRepositoryImpl) RequeuePlacementReviewResult(
	ctx context.Context,
	input RequeuePlacementReviewInput,
) (*RequeuePlacementReviewResult, error) {
	input = normalizeRequeuePlacementReviewInput(input)
	if err := validateRequeuePlacementReviewInput(input); err != nil {
		return nil, err
	}
	scope := placementRetryScope(input)
	result := &RequeuePlacementReviewResult{Status: string(domain.SemanticReviewRetryable)}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockPlacementRunForCommit(ctx, tx, scope); err != nil {
			return err
		}
		if err := lockPlacementItemForCommit(ctx, tx, scope); err != nil {
			return err
		}
		if err := ensurePlacementItemCurrent(ctx, tx, scope); err != nil {
			if errors.Is(err, ErrPlacementStaleSource) {
				outcomeID, outcomeErr := appendSupersededPlacementOutcome(ctx, tx, scope)
				if outcomeErr != nil {
					return outcomeErr
				}
				firstDisposition, finishErr := finishPlacementRunIfTerminal(ctx, tx, scope, string(domain.PlacementRunFailed))
				if finishErr != nil {
					return finishErr
				}
				result.FirstDisposition = firstDisposition
				result.Status = "superseded"
				result.OutcomeID = outcomeID
				return nil
			}
			return err
		}
		if input.ReleaseAssessorAttempt {
			if err := releasePlacementAssessmentProviderAttempt(ctx, tx, scope); err != nil {
				return err
			}
		}
		if len(input.Payload) > 0 {
			payload := retryPlacementPayload(input.Payload)
			outcomeID, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
				TeamID:          input.TeamID,
				OwnerProfileID:  input.OwnerProfileID,
				PlacementRunID:  input.PlacementRunID,
				PlacementItemID: input.PlacementItemID,
				OutcomeKind:     input.OutcomeKind,
				Status:          string(domain.SemanticReviewRetryable),
				Payload:         payload,
			})
			if err != nil {
				return err
			}
			if err := updatePlacementItemOutcome(ctx, tx, PlacementOutcomeInput{
				TeamID:           input.TeamID,
				OwnerProfileID:   input.OwnerProfileID,
				PlacementItemID:  input.PlacementItemID,
				UpdateItemStatus: string(domain.PlacementRunQueued),
				Payload:          payload,
			}); err != nil {
				return err
			}
			result.OutcomeID = outcomeID
		}
		return requeuePlacementRunForRetry(ctx, tx, scope)
	})
	if err != nil {
		return nil, fmt.Errorf("placement review retry: %w", err)
	}
	return result, nil
}

func normalizeCompletePlacementReviewInput(input CompletePlacementReviewInput) CompletePlacementReviewInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.PlacementRunID = strings.TrimSpace(input.PlacementRunID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.OutcomeKind = strings.TrimSpace(input.OutcomeKind)
	input.Status = strings.TrimSpace(input.Status)
	input.Category = strings.TrimSpace(input.Category)
	if input.OutcomeKind == "" {
		input.OutcomeKind = "semantic_review_terminal"
	}
	return input
}

func normalizeRequeuePlacementReviewInput(input RequeuePlacementReviewInput) RequeuePlacementReviewInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.PlacementRunID = strings.TrimSpace(input.PlacementRunID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.OutcomeKind = strings.TrimSpace(input.OutcomeKind)
	if input.OutcomeKind == "" {
		input.OutcomeKind = "placement_retry"
	}
	if input.RetryAfter < 0 {
		input.RetryAfter = 0
	}
	if input.RetryAfter > placementRetryMaxDelay {
		input.RetryAfter = placementRetryMaxDelay
	}
	return input
}

func validateCompletePlacementReviewInput(input CompletePlacementReviewInput) error {
	for label, value := range map[string]string{
		"team_id":           input.TeamID,
		"owner_profile_id":  input.OwnerProfileID,
		"ingest_id":         input.IngestID,
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
	switch input.Status {
	case string(domain.SemanticReviewReviewRequired),
		string(domain.SemanticReviewRejected),
		string(domain.SemanticReviewQuarantined),
		string(domain.SemanticReviewTerminalFailure):
	default:
		return fmt.Errorf("unsupported terminal review status %q", input.Status)
	}
	return nil
}

func validateRequeuePlacementReviewInput(input RequeuePlacementReviewInput) error {
	for label, value := range map[string]string{
		"team_id":           input.TeamID,
		"owner_profile_id":  input.OwnerProfileID,
		"ingest_id":         input.IngestID,
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

func placementCommitScope(input CompletePlacementReviewInput) CommitPlacementSemanticInput {
	return CommitPlacementSemanticInput{
		TeamID:           input.TeamID,
		OwnerProfileID:   input.OwnerProfileID,
		IngestID:         input.IngestID,
		PlacementRunID:   input.PlacementRunID,
		PlacementItemID:  input.PlacementItemID,
		WorkerID:         input.WorkerID,
		ExpectedAttempts: input.ExpectedAttempts,
	}
}

func placementRetryScope(input RequeuePlacementReviewInput) CommitPlacementSemanticInput {
	return CommitPlacementSemanticInput{
		TeamID:           input.TeamID,
		OwnerProfileID:   input.OwnerProfileID,
		IngestID:         input.IngestID,
		PlacementRunID:   input.PlacementRunID,
		PlacementItemID:  input.PlacementItemID,
		WorkerID:         input.WorkerID,
		ExpectedAttempts: input.ExpectedAttempts,
		RetryAfter:       input.RetryAfter,
	}
}

func retryPlacementPayload(base map[string]any) map[string]any {
	payload := map[string]any{
		"contract_version": domain.ContractVersion,
		"status":           string(domain.SemanticReviewRetryable),
	}
	for key, value := range base {
		payload[key] = value
	}
	return payload
}

func terminalPlacementStatuses(status string, category string) (string, string, string) {
	if category == "" {
		category = "candidate"
	}
	switch status {
	case string(domain.SemanticReviewQuarantined):
		return "quarantined", "quarantined", string(domain.PlacementRunQuarantined)
	case string(domain.SemanticReviewTerminalFailure):
		return "failed", "failed", string(domain.PlacementRunFailed)
	default:
		return "completed", category, string(domain.PlacementRunCompleted)
	}
}

func terminalPlacementPayload(base map[string]any, status string) map[string]any {
	payload := map[string]any{
		"contract_version": domain.ContractVersion,
		"status":           status,
	}
	for key, value := range base {
		payload[key] = value
	}
	return payload
}
