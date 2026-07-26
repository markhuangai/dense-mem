package repository

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func normalizeDreamCycleClaimInput(input DreamCycleClaimInput) DreamCycleClaimInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.RunDate = strings.TrimSpace(input.RunDate)
	input.WindowKey = strings.TrimSpace(input.WindowKey)
	if input.RunDate == "" {
		input.RunDate = time.Now().UTC().Format("2006-01-02")
	}
	if input.WindowKey == "" {
		input.WindowKey = input.RunDate
	}
	if input.LeaseUntil.IsZero() {
		input.LeaseUntil = time.Now().UTC().Add(30 * time.Second)
	}
	return input
}

func validateDreamCycleClaimInput(input DreamCycleClaimInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.RunDate == "" {
		return errors.New("run_date is required")
	}
	if input.WindowKey == "" {
		return errors.New("window_key is required")
	}
	return nil
}

func normalizeDreamCycleCompleteInput(input DreamCycleCompleteInput) DreamCycleCompleteInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.Status = strings.TrimSpace(input.Status)
	input.Error = strings.TrimSpace(input.Error)
	if input.Status == "" {
		input.Status = "completed"
	}
	return input
}

func validateDreamCycleCompleteInput(input DreamCycleCompleteInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.RunID); err != nil {
		return fmt.Errorf("run_id is required: %w", err)
	}
	switch input.Status {
	case "completed", "failed", "skipped", "cancelled":
		return nil
	default:
		return fmt.Errorf("unsupported cycle status %q", input.Status)
	}
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

func normalizeUpsertHypothesisInput(input UpsertHypothesisInput) UpsertHypothesisInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.Statement = strings.TrimSpace(input.Statement)
	input.Rationale = strings.TrimSpace(input.Rationale)
	input.SubjectEntityID = strings.TrimSpace(input.SubjectEntityID)
	input.PredicateKey = strings.TrimSpace(input.PredicateKey)
	if input.PredicateVersion == 0 {
		input.PredicateVersion = 1
	}
	input.ObjectEntityID = strings.TrimSpace(input.ObjectEntityID)
	input.ObjectValueID = strings.TrimSpace(input.ObjectValueID)
	input.ContentHash = strings.TrimSpace(input.ContentHash)
	input.GeneratorKind = strings.TrimSpace(input.GeneratorKind)
	input.GeneratorVersion = strings.TrimSpace(input.GeneratorVersion)
	if input.GeneratorKind == "" {
		input.GeneratorKind = "deterministic"
	}
	if input.GeneratorVersion == "" {
		input.GeneratorVersion = "dream-v2"
	}
	input.SourceOwnerProfileIDs = normalizeStringSet(input.SourceOwnerProfileIDs)
	return input
}

func validateUpsertHypothesisInput(input UpsertHypothesisInput) error {
	for label, value := range map[string]string{
		"team_id":           input.TeamID,
		"owner_profile_id":  input.OwnerProfileID,
		"run_id":            input.RunID,
		"subject_entity_id": input.SubjectEntityID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if input.Statement == "" {
		return errors.New("statement is required")
	}
	if input.PredicateKey == "" {
		return errors.New("predicate_key is required")
	}
	if input.PredicateVersion < 1 {
		return errors.New("predicate_version must be greater than zero")
	}
	if (input.ObjectEntityID == "") == (input.ObjectValueID == "") {
		return errors.New("exactly one object endpoint is required")
	}
	if input.ObjectEntityID != "" {
		if _, err := uuid.Parse(input.ObjectEntityID); err != nil {
			return fmt.Errorf("object_entity_id is invalid: %w", err)
		}
	}
	if input.ObjectValueID != "" {
		if _, err := uuid.Parse(input.ObjectValueID); err != nil {
			return fmt.Errorf("object_value_id is invalid: %w", err)
		}
	}
	if len(input.SourceVersions) == 0 {
		return errors.New("source_versions is required")
	}
	if input.ContentHash == "" {
		return errors.New("content_hash is required")
	}
	return nil
}

func normalizeListHypothesesInput(input ListHypothesesInput) ListHypothesesInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Status = strings.TrimSpace(input.Status)
	input.Cursor = strings.TrimSpace(input.Cursor)
	if input.Limit <= 0 {
		input.Limit = 20
	}
	if input.Limit > 100 {
		input.Limit = 100
	}
	return input
}

func validateListHypothesesInput(input ListHypothesesInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.Status != "" && !hypothesisStatusValid(input.Status) {
		return fmt.Errorf("unsupported hypothesis status %q", input.Status)
	}
	return nil
}

func normalizeRefreshHypothesisStalenessInput(input RefreshHypothesisStalenessInput) RefreshHypothesisStalenessInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	if input.Limit <= 0 {
		input.Limit = 100
	}
	if input.Limit > 500 {
		input.Limit = 500
	}
	return input
}

func validateRefreshHypothesisStalenessInput(input RefreshHypothesisStalenessInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	return nil
}

func normalizeUpdateHypothesisStatusInput(input UpdateHypothesisStatusInput) UpdateHypothesisStatusInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.HypothesisID = strings.TrimSpace(input.HypothesisID)
	input.Status = strings.TrimSpace(input.Status)
	input.InvalidatedReason = strings.TrimSpace(input.InvalidatedReason)
	return input
}

func validateUpdateHypothesisStatusInput(input UpdateHypothesisStatusInput) error {
	for label, value := range map[string]string{
		"team_id":          input.TeamID,
		"owner_profile_id": input.OwnerProfileID,
		"hypothesis_id":    input.HypothesisID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if !hypothesisStatusValid(input.Status) || input.Status == "submitted" {
		return fmt.Errorf("unsupported hypothesis status %q", input.Status)
	}
	return nil
}

func normalizeSubmitHypothesisInput(input SubmitHypothesisInput) SubmitHypothesisInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.HypothesisID = strings.TrimSpace(input.HypothesisID)
	input.SubmittedIngestID = strings.TrimSpace(input.SubmittedIngestID)
	input.InvalidatedReason = strings.TrimSpace(input.InvalidatedReason)
	return input
}

func validateSubmitHypothesisInput(input SubmitHypothesisInput) error {
	for label, value := range map[string]string{
		"team_id":             input.TeamID,
		"owner_profile_id":    input.OwnerProfileID,
		"hypothesis_id":       input.HypothesisID,
		"submitted_ingest_id": input.SubmittedIngestID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	return nil
}

func hypothesisStatusValid(status string) bool {
	switch status {
	case "proposed", "reinforced", "stale", "rejected", "submitted":
		return true
	default:
		return false
	}
}

func normalizeStringSet(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
