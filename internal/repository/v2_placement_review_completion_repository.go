package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type V2CompletePlacementReviewInput struct {
	TeamID           string
	OwnerProfileID   string
	IngestID         string
	PlacementRunID   string
	PlacementItemID  string
	WorkerID         string
	ExpectedAttempts int
	MigrationRunID   string
	MigrationEpoch   int
	OutcomeKind      string
	Status           string
	Category         string
	Payload          map[string]any
}

type V2CompletePlacementReviewResult struct {
	Status    string
	OutcomeID string
}

type V2RequeuePlacementReviewInput struct {
	TeamID           string
	OwnerProfileID   string
	IngestID         string
	PlacementRunID   string
	PlacementItemID  string
	WorkerID         string
	ExpectedAttempts int
	MigrationRunID   string
	MigrationEpoch   int
}

type V2RequeuePlacementReviewResult struct {
	Status    string
	OutcomeID string
}

func (r *V2LedgerRepositoryImpl) CompletePlacementReviewResult(
	ctx context.Context,
	input V2CompletePlacementReviewInput,
) (*V2CompletePlacementReviewResult, error) {
	input = normalizeV2CompletePlacementReviewInput(input)
	if err := validateV2CompletePlacementReviewInput(input); err != nil {
		return nil, err
	}
	scope := v2PlacementCommitScope(input)
	result := &V2CompletePlacementReviewResult{Status: input.Status}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockV2PlacementRunForCommit(ctx, tx, scope); err != nil {
			return err
		}
		if err := lockV2PlacementItemForCommit(ctx, tx, scope); err != nil {
			return err
		}
		if err := ensureV2PlacementItemCurrent(ctx, tx, scope); err != nil {
			if errors.Is(err, ErrV2PlacementStaleSource) {
				outcomeID, outcomeErr := appendV2SupersededPlacementOutcome(ctx, tx, scope)
				if outcomeErr != nil {
					return outcomeErr
				}
				if finishErr := finishV2PlacementRunIfTerminal(ctx, tx, scope, string(domain.V2PlacementRunFailed)); finishErr != nil {
					return finishErr
				}
				result.Status = "superseded"
				result.OutcomeID = outcomeID
				return nil
			}
			return err
		}
		itemStatus, category, runStatus := v2TerminalPlacementStatuses(input.Status, input.Category)
		payload := v2TerminalPlacementPayload(input.Payload, input.Status)
		if v2PlacementEvidenceSearchableStatus(input.Status) {
			placementFragmentID, err := loadV2PlacementItemFragmentID(ctx, tx, scope)
			if err != nil {
				return err
			}
			if _, err := upsertV2PlacementItemEvidenceSearchDocument(ctx, tx, scope, placementFragmentID); err != nil {
				return err
			}
		}
		outcomeID, err := insertV2PlacementOutcome(ctx, tx, V2PlacementOutcomeInput{
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
		if err := updateV2PlacementItemOutcome(ctx, tx, V2PlacementOutcomeInput{
			TeamID:             input.TeamID,
			OwnerProfileID:     input.OwnerProfileID,
			PlacementItemID:    input.PlacementItemID,
			UpdateItemStatus:   itemStatus,
			UpdateItemCategory: category,
			Payload:            payload,
		}); err != nil {
			return err
		}
		if err := finishV2PlacementRunIfTerminal(ctx, tx, scope, runStatus); err != nil {
			return err
		}
		result.OutcomeID = outcomeID
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 placement review completion: %w", err)
	}
	return result, nil
}

func (r *V2LedgerRepositoryImpl) RequeuePlacementReviewResult(
	ctx context.Context,
	input V2RequeuePlacementReviewInput,
) (*V2RequeuePlacementReviewResult, error) {
	input = normalizeV2RequeuePlacementReviewInput(input)
	if err := validateV2RequeuePlacementReviewInput(input); err != nil {
		return nil, err
	}
	scope := v2PlacementRetryScope(input)
	result := &V2RequeuePlacementReviewResult{Status: string(domain.V2SemanticReviewRetryable)}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockV2PlacementRunForCommit(ctx, tx, scope); err != nil {
			return err
		}
		if err := lockV2PlacementItemForCommit(ctx, tx, scope); err != nil {
			return err
		}
		if err := ensureV2PlacementItemCurrent(ctx, tx, scope); err != nil {
			if errors.Is(err, ErrV2PlacementStaleSource) {
				outcomeID, outcomeErr := appendV2SupersededPlacementOutcome(ctx, tx, scope)
				if outcomeErr != nil {
					return outcomeErr
				}
				if finishErr := finishV2PlacementRunIfTerminal(ctx, tx, scope, string(domain.V2PlacementRunFailed)); finishErr != nil {
					return finishErr
				}
				result.Status = "superseded"
				result.OutcomeID = outcomeID
				return nil
			}
			return err
		}
		return requeueV2PlacementRunForRetry(ctx, tx, scope)
	})
	if err != nil {
		return nil, fmt.Errorf("v2 placement review retry: %w", err)
	}
	return result, nil
}

func normalizeV2CompletePlacementReviewInput(input V2CompletePlacementReviewInput) V2CompletePlacementReviewInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.PlacementRunID = strings.TrimSpace(input.PlacementRunID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.MigrationRunID = strings.TrimSpace(input.MigrationRunID)
	input.OutcomeKind = strings.TrimSpace(input.OutcomeKind)
	input.Status = strings.TrimSpace(input.Status)
	input.Category = strings.TrimSpace(input.Category)
	if input.OutcomeKind == "" {
		input.OutcomeKind = "semantic_review_terminal"
	}
	return input
}

func normalizeV2RequeuePlacementReviewInput(input V2RequeuePlacementReviewInput) V2RequeuePlacementReviewInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.PlacementRunID = strings.TrimSpace(input.PlacementRunID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.MigrationRunID = strings.TrimSpace(input.MigrationRunID)
	return input
}

func validateV2CompletePlacementReviewInput(input V2CompletePlacementReviewInput) error {
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
	if err := validateV2PlacementMigrationFence(input.MigrationRunID, input.MigrationEpoch); err != nil {
		return err
	}
	switch input.Status {
	case string(domain.V2SemanticReviewReviewRequired),
		string(domain.V2SemanticReviewRejected),
		string(domain.V2SemanticReviewQuarantined),
		string(domain.V2SemanticReviewTerminalFailure):
	default:
		return fmt.Errorf("unsupported terminal review status %q", input.Status)
	}
	return nil
}

func validateV2RequeuePlacementReviewInput(input V2RequeuePlacementReviewInput) error {
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
	if err := validateV2PlacementMigrationFence(input.MigrationRunID, input.MigrationEpoch); err != nil {
		return err
	}
	return nil
}

func v2PlacementCommitScope(input V2CompletePlacementReviewInput) V2CommitPlacementSemanticInput {
	return V2CommitPlacementSemanticInput{
		TeamID:           input.TeamID,
		OwnerProfileID:   input.OwnerProfileID,
		IngestID:         input.IngestID,
		PlacementRunID:   input.PlacementRunID,
		PlacementItemID:  input.PlacementItemID,
		WorkerID:         input.WorkerID,
		ExpectedAttempts: input.ExpectedAttempts,
		MigrationRunID:   input.MigrationRunID,
		MigrationEpoch:   input.MigrationEpoch,
	}
}

func v2PlacementRetryScope(input V2RequeuePlacementReviewInput) V2CommitPlacementSemanticInput {
	return V2CommitPlacementSemanticInput{
		TeamID:           input.TeamID,
		OwnerProfileID:   input.OwnerProfileID,
		IngestID:         input.IngestID,
		PlacementRunID:   input.PlacementRunID,
		PlacementItemID:  input.PlacementItemID,
		WorkerID:         input.WorkerID,
		ExpectedAttempts: input.ExpectedAttempts,
		MigrationRunID:   input.MigrationRunID,
		MigrationEpoch:   input.MigrationEpoch,
	}
}

func v2TerminalPlacementStatuses(status string, category string) (string, string, string) {
	if category == "" {
		category = "candidate"
	}
	switch status {
	case string(domain.V2SemanticReviewQuarantined):
		return "quarantined", "quarantined", string(domain.V2PlacementRunQuarantined)
	case string(domain.V2SemanticReviewTerminalFailure):
		return "failed", "failed", string(domain.V2PlacementRunFailed)
	default:
		return "completed", category, string(domain.V2PlacementRunCompleted)
	}
}

func v2TerminalPlacementPayload(base map[string]any, status string) map[string]any {
	payload := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"status":           status,
	}
	for key, value := range base {
		payload[key] = value
	}
	return payload
}
