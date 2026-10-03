package postgres

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

func normalizeDreamCycleClaimInput(input DreamCycleClaimInput) DreamCycleClaimInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.InitiatedByProfileID = strings.TrimSpace(input.InitiatedByProfileID)
	input.RunDate = strings.TrimSpace(input.RunDate)
	input.WindowKey = strings.TrimSpace(input.WindowKey)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if !input.Lane.IsValid() {
		input.Lane = domain.DreamLaneGraph
	}
	if input.RunDate == "" {
		input.RunDate = time.Now().UTC().Format("2006-01-02")
	}
	if input.WindowKey == "" {
		input.WindowKey = input.RunDate
	}
	if input.LeaseUntil.IsZero() {
		input.LeaseUntil = time.Now().UTC().Add(30 * time.Second)
	}
	if input.LeaseToken == "" {
		input.LeaseToken = uuid.NewString()
	}
	return input
}

func validateDreamCycleClaimInput(input DreamCycleClaimInput, system bool) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if !system {
		if _, err := uuid.Parse(input.InitiatedByProfileID); err != nil {
			return fmt.Errorf("initiated_by_profile_id is required: %w", err)
		}
	}
	if input.RunDate == "" {
		return errors.New("run_date is required")
	}
	if input.WindowKey == "" {
		return errors.New("window_key is required")
	}
	if _, err := uuid.Parse(input.LeaseToken); err != nil {
		return fmt.Errorf("lease_token is required: %w", err)
	}
	if input.LeaseUntil.IsZero() {
		return errors.New("lease_until is required")
	}
	if err := dreamcontract.ValidateGenerationLane(input.Lane); err != nil {
		return err
	}
	return nil
}

func normalizeDreamCycleRecoveryClaimInput(input DreamCycleRecoveryClaimInput) DreamCycleRecoveryClaimInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if !input.Lane.IsValid() {
		input.Lane = domain.DreamLaneGraph
	}
	if input.LeaseToken == "" {
		input.LeaseToken = uuid.NewString()
	}
	if input.LeaseUntil.IsZero() {
		input.LeaseUntil = time.Now().UTC().Add(15 * time.Minute)
	}
	if input.MaxAttempts <= 0 {
		input.MaxAttempts = 3
	}
	return input
}

func validateDreamCycleRecoveryClaimInput(input DreamCycleRecoveryClaimInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.LeaseToken); err != nil {
		return fmt.Errorf("lease_token is required: %w", err)
	}
	if input.LeaseUntil.IsZero() {
		return errors.New("lease_until is required")
	}
	if input.MaxAttempts < 1 {
		return errors.New("max_attempts must be greater than zero")
	}
	if err := dreamcontract.ValidateGenerationLane(input.Lane); err != nil {
		return err
	}
	return nil
}

func normalizeDreamCycleCompleteInput(input DreamCycleCompleteInput) DreamCycleCompleteInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.InitiatedByProfileID = strings.TrimSpace(input.InitiatedByProfileID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.Status = strings.TrimSpace(input.Status)
	input.Error = strings.TrimSpace(input.Error)
	if !input.Lane.IsValid() {
		input.Lane = domain.DreamLaneGraph
	}
	if input.OutcomeSummary == nil {
		input.OutcomeSummary = map[string]int{}
	}
	if input.Status == "" {
		input.Status = "completed"
	}
	return input
}

func validateDreamCycleCompleteInput(input DreamCycleCompleteInput, system bool) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if !system {
		if _, err := uuid.Parse(input.InitiatedByProfileID); err != nil {
			return fmt.Errorf("initiated_by_profile_id is required: %w", err)
		}
	}
	if _, err := uuid.Parse(input.RunID); err != nil {
		return fmt.Errorf("run_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.LeaseToken); err != nil {
		return fmt.Errorf("lease_token is required: %w", err)
	}
	if input.ProviderTurns < 0 || input.ProviderInputTokens < 0 || input.ProviderOutputTokens < 0 ||
		input.AttemptedPaths < 0 || input.ProviderProposals < 0 {
		return errors.New("provider diagnostics must not be negative")
	}
	if input.EvidenceTargets < 0 || input.EvaluatedEvidenceTargets < 0 {
		return errors.New("evidence diagnostics must not be negative")
	}
	if !input.Lane.IsValid() {
		return fmt.Errorf("unsupported dream lane %q", input.Lane)
	}
	switch input.Status {
	case "completed", "failed", "skipped", "cancelled", "missed":
		return nil
	default:
		return fmt.Errorf("unsupported cycle status %q", input.Status)
	}
}

func normalizeDreamGenerationPersistInput(input DreamGenerationPersistInput) DreamGenerationPersistInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.CreatedByProfileID = strings.TrimSpace(input.CreatedByProfileID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.ProviderModel = strings.TrimSpace(input.ProviderModel)
	input.EvaluatedPaths = normalizeDreamPathEvaluationInputs(input.EvaluatedPaths)
	for index := range input.Proposals {
		input.Proposals[index].TeamID = input.TeamID
		input.Proposals[index].CreatedByProfileID = input.CreatedByProfileID
		input.Proposals[index].RunID = input.RunID
		input.Proposals[index] = dreamcontract.NormalizeUpsertHypothesisInput(input.Proposals[index])
	}
	return input
}

func validateDreamGenerationPersistInput(input DreamGenerationPersistInput, system bool) error {
	for label, value := range map[string]string{
		"team_id":     input.TeamID,
		"run_id":      input.RunID,
		"lease_token": input.LeaseToken,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if !system {
		if _, err := uuid.Parse(input.CreatedByProfileID); err != nil {
			return fmt.Errorf("created_by_profile_id is required: %w", err)
		}
	}
	if input.ProviderModel == "" {
		return errors.New("provider_model is required")
	}
	if len(input.EvaluatedPaths) == 0 {
		return errors.New("evaluated_paths is required")
	}
	if err := validateDreamPathEvaluationInputs(input.EvaluatedPaths); err != nil {
		return err
	}
	for index, proposal := range input.Proposals {
		if proposal.GeneratorKind != "provider" {
			return fmt.Errorf("proposals[%d] must be provider-generated", index)
		}
		if err := dreamcontract.ValidateUpsertHypothesisInput(proposal, system); err != nil {
			return fmt.Errorf("proposals[%d]: %w", index, err)
		}
	}
	return nil
}

func normalizeDreamInputListInput(input DreamInputListInput) DreamInputListInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	if input.Limit <= 0 {
		input.Limit = 50
	}
	if input.Limit > 500 {
		input.Limit = 500
	}
	return input
}

func validateDreamInputListInput(input DreamInputListInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	return nil
}

func normalizeListHypothesesInput(input ListHypothesesInput) ListHypothesesInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Status = strings.TrimSpace(input.Status)
	input.Cursor = strings.TrimSpace(input.Cursor)
	input.Sort = strings.ToLower(strings.TrimSpace(input.Sort))
	input.Direction = strings.ToLower(strings.TrimSpace(input.Direction))
	if input.Limit <= 0 {
		input.Limit = 20
	}
	if input.Limit > 100 {
		input.Limit = 100
	}
	if input.Sort == "" {
		input.Sort = "updated_at"
	}
	if input.Direction == "" {
		input.Direction = "desc"
	}
	return input
}

func validateListHypothesesInput(input ListHypothesesInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.Status != "" && !domain.DreamStatus(input.Status).IsValid() {
		return fmt.Errorf("unsupported hypothesis status %q", input.Status)
	}
	switch input.Sort {
	case "updated_at", "created_at":
	default:
		return fmt.Errorf("unsupported hypothesis sort %q", input.Sort)
	}
	switch input.Direction {
	case "asc", "desc":
	default:
		return fmt.Errorf("unsupported hypothesis direction %q", input.Direction)
	}
	return nil
}

func hypothesisListOrder(sort, direction string) string {
	column := "updated_at"
	switch sort {
	case "created_at":
		column = "created_at"
	}
	if direction == "asc" {
		return column + " ASC"
	}
	return column + " DESC"
}

func normalizeSubmitHypothesisInput(input SubmitHypothesisInput) SubmitHypothesisInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ActorProfileID = strings.TrimSpace(input.ActorProfileID)
	input.HypothesisID = strings.TrimSpace(input.HypothesisID)
	input.Decision = strings.TrimSpace(input.Decision)
	input.SubmittedIngestID = strings.TrimSpace(input.SubmittedIngestID)
	input.InvalidatedReason = strings.TrimSpace(input.InvalidatedReason)
	return input
}

func validateSubmitHypothesisInput(input SubmitHypothesisInput) error {
	for label, value := range map[string]string{
		"team_id":             input.TeamID,
		"actor_profile_id":    input.ActorProfileID,
		"hypothesis_id":       input.HypothesisID,
		"submitted_ingest_id": input.SubmittedIngestID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	switch input.Decision {
	case "confirm_true", "confirm_false", "promote_candidate":
		return nil
	default:
		return fmt.Errorf("unsupported feedback decision %q", input.Decision)
	}
}
