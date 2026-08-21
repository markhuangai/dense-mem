package repository

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func embeddingRetryBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	seconds := 5
	for i := 1; i < attempts; i++ {
		seconds *= 2
		if seconds >= 300 {
			return 5 * time.Minute
		}
	}
	return time.Duration(seconds) * time.Second
}

func normalizeFailEmbeddingJobInput(input FailEmbeddingJobInput) FailEmbeddingJobInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.EmbeddingJobID = strings.TrimSpace(input.EmbeddingJobID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	if input.RetryAfter < 0 {
		input.RetryAfter = 0
	}
	if input.RetryAfter > 24*time.Hour {
		input.RetryAfter = 24 * time.Hour
	}
	input.FailureClass, input.FailureCode = normalizeEmbeddingFailureContract(input.FailureClass, input.FailureCode)
	input.Error = domain.EmbeddingFailureMessage(input.FailureCode)
	return input
}

func normalizeEmbeddingFailureContract(failureClass, failureCode string) (string, string) {
	failureClass = strings.TrimSpace(failureClass)
	failureCode = strings.TrimSpace(failureCode)
	if !domain.EmbeddingFailureContractValid(failureClass, failureCode) {
		return string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureUnknown)
	}
	return failureClass, failureCode
}

func validateFailEmbeddingJobInput(input FailEmbeddingJobInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if err := validateOptionalSpaceID(input.SpaceID); err != nil {
		return err
	}
	if _, err := uuid.Parse(input.EmbeddingJobID); err != nil {
		return fmt.Errorf("embedding_job_id is required: %w", err)
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if input.ExpectedAttempts < 1 {
		return errors.New("expected_attempts must be greater than zero")
	}
	if input.Error == "" {
		return errors.New("error is required")
	}
	return nil
}

func normalizeEmbeddingQueueStatsInput(input EmbeddingQueueStatsInput) EmbeddingQueueStatsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	return input
}

func validateEmbeddingQueueStatsInput(input EmbeddingQueueStatsInput) error {
	if input.TeamID != "" {
		if _, err := uuid.Parse(input.TeamID); err != nil {
			return fmt.Errorf("team_id is invalid: %w", err)
		}
	}
	if input.EmbeddingContractID != "" {
		if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
			return fmt.Errorf("embedding_contract_id is invalid: %w", err)
		}
	}
	if input.EmbeddingDimensions < 0 {
		return errors.New("embedding_dimensions must not be negative")
	}
	return nil
}
