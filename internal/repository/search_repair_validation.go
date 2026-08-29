package repository

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func normalizeSearchRepairRunInput(input SearchRepairRunInput) SearchRepairRunInput {
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if input.Lease <= 0 {
		input.Lease = 15 * time.Minute
	}
	return input
}

func validateSearchRepairRunInput(input SearchRepairRunInput) error {
	if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
		return fmt.Errorf("embedding_contract_id is invalid: %w", err)
	}
	if input.EmbeddingDimensions < 1 || input.LocalRunDate.IsZero() || input.WorkerID == "" || input.Lease < time.Second {
		return errors.New("search repair run input is invalid")
	}
	return nil
}

func normalizeSearchRepairSelectionInput(input SearchRepairSelectionInput) SearchRepairSelectionInput {
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if input.Cursor != nil {
		input.Cursor.TeamID = strings.TrimSpace(input.Cursor.TeamID)
		input.Cursor.SourceKind = strings.TrimSpace(input.Cursor.SourceKind)
		input.Cursor.SourceID = strings.TrimSpace(input.Cursor.SourceID)
		input.Cursor.SearchDocumentID = strings.TrimSpace(input.Cursor.SearchDocumentID)
	}
	return input
}

func validateSearchRepairSelectionInput(input SearchRepairSelectionInput) error {
	if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
		return fmt.Errorf("embedding_contract_id is invalid: %w", err)
	}
	if input.EmbeddingDimensions < 1 {
		return errors.New("embedding_dimensions must be positive")
	}
	if (input.RunID == "") != (input.LeaseToken == "") {
		return errors.New("run_id and lease_token must be provided together")
	}
	if input.RunID != "" {
		if _, err := uuid.Parse(input.RunID); err != nil {
			return fmt.Errorf("run_id is invalid: %w", err)
		}
		if _, err := uuid.Parse(input.LeaseToken); err != nil {
			return fmt.Errorf("lease_token is invalid: %w", err)
		}
	}
	if input.Cursor != nil {
		if input.Cursor.ObservedAt.IsZero() {
			return errors.New("selection cursor timestamp is required")
		}
		if input.Cursor.SourceKind == "" {
			return errors.New("selection cursor source kind is required")
		}
		for field, value := range map[string]string{
			"selection cursor team_id":   input.Cursor.TeamID,
			"selection cursor source_id": input.Cursor.SourceID,
		} {
			if _, err := uuid.Parse(value); err != nil {
				return fmt.Errorf("%s is invalid: %w", field, err)
			}
		}
		if input.Cursor.SearchDocumentID != "" {
			if _, err := uuid.Parse(input.Cursor.SearchDocumentID); err != nil {
				return fmt.Errorf("selection cursor search_document_id is invalid: %w", err)
			}
		}
	}
	return nil
}

func normalizeApplySearchRepairInput(input ApplySearchRepairInput) ApplySearchRepairInput {
	input.RunID = strings.TrimSpace(input.RunID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	input.SearchIndexGenerationID = strings.TrimSpace(input.SearchIndexGenerationID)
	return input
}

func validateApplySearchRepairInput(input ApplySearchRepairInput) error {
	if _, err := uuid.Parse(input.RunID); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if _, err := uuid.Parse(input.LeaseToken); err != nil {
		return fmt.Errorf("lease_token is invalid: %w", err)
	}
	if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
		return fmt.Errorf("embedding_contract_id is invalid: %w", err)
	}
	if _, err := uuid.Parse(input.SearchIndexGenerationID); err != nil {
		return fmt.Errorf("search_index_generation_id is invalid: %w", err)
	}
	if input.EmbeddingDimensions < 1 || input.IndexGeneration < 1 || len(input.Documents) > searchRepairCandidateLimit {
		return errors.New("search repair apply input is invalid")
	}
	return nil
}

func normalizeFinishSearchRepairRunInput(input FinishSearchRepairRunInput) FinishSearchRepairRunInput {
	input.RunID = strings.TrimSpace(input.RunID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.LastError = strings.TrimSpace(input.LastError)
	if runes := []rune(input.LastError); len(runes) > 128 {
		input.LastError = string(runes[:128])
	} else {
		input.LastError = string(runes)
	}
	return input
}

func validateFinishSearchRepairRunInput(input FinishSearchRepairRunInput) error {
	if _, err := uuid.Parse(input.RunID); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if _, err := uuid.Parse(input.LeaseToken); err != nil {
		return fmt.Errorf("lease_token is invalid: %w", err)
	}
	if input.Status != "completed" && input.Status != "deferred" && input.Status != "failed" && input.Status != "ambiguous" ||
		input.SelectedCount < 0 || input.EmbeddedCount < 0 || input.UpdatedCount < 0 || input.DriftedCount < 0 {
		return errors.New("search repair completion input is invalid")
	}
	return nil
}
