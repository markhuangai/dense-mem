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
	input.InitiatedByProfileID = strings.TrimSpace(input.InitiatedByProfileID)
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
	return nil
}

func normalizeDreamCycleCompleteInput(input DreamCycleCompleteInput) DreamCycleCompleteInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.InitiatedByProfileID = strings.TrimSpace(input.InitiatedByProfileID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.Status = strings.TrimSpace(input.Status)
	input.Error = strings.TrimSpace(input.Error)
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
	switch input.Status {
	case "completed", "failed", "skipped", "cancelled", "missed":
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
	input.CreatedByProfileID = strings.TrimSpace(input.CreatedByProfileID)
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

func validateUpsertHypothesisInput(input UpsertHypothesisInput, system bool) error {
	for label, value := range map[string]string{
		"team_id":           input.TeamID,
		"run_id":            input.RunID,
		"subject_entity_id": input.SubjectEntityID,
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
	if input.Status != "" && !hypothesisStatusValid(input.Status) {
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

func normalizeUpdateHypothesisStatusInput(input UpdateHypothesisStatusInput) UpdateHypothesisStatusInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ActorProfileID = strings.TrimSpace(input.ActorProfileID)
	input.HypothesisID = strings.TrimSpace(input.HypothesisID)
	input.Status = strings.TrimSpace(input.Status)
	input.Decision = strings.TrimSpace(input.Decision)
	input.InvalidatedReason = strings.TrimSpace(input.InvalidatedReason)
	return input
}

func validateUpdateHypothesisStatusInput(input UpdateHypothesisStatusInput) error {
	for label, value := range map[string]string{
		"team_id":          input.TeamID,
		"actor_profile_id": input.ActorProfileID,
		"hypothesis_id":    input.HypothesisID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if !hypothesisStatusValid(input.Status) || input.Status == "submitted" {
		return fmt.Errorf("unsupported hypothesis status %q", input.Status)
	}
	switch input.Decision {
	case "reject", "stale", "reinforce":
		return nil
	default:
		return fmt.Errorf("unsupported feedback decision %q", input.Decision)
	}
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
