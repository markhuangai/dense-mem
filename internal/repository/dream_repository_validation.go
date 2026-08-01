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
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
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
	return nil
}

func normalizeDreamCycleRecoveryClaimInput(input DreamCycleRecoveryClaimInput) DreamCycleRecoveryClaimInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
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
	return nil
}

func normalizeDreamCycleCompleteInput(input DreamCycleCompleteInput) DreamCycleCompleteInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.InitiatedByProfileID = strings.TrimSpace(input.InitiatedByProfileID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.Status = strings.TrimSpace(input.Status)
	input.Error = strings.TrimSpace(input.Error)
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
		input.Proposals[index] = normalizeUpsertHypothesisInput(input.Proposals[index])
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
		if err := validateUpsertHypothesisInput(proposal, system); err != nil {
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
	input.TargetIdentity = strings.TrimSpace(input.TargetIdentity)
	input.GeneratorKind = strings.TrimSpace(input.GeneratorKind)
	input.GeneratorVersion = strings.TrimSpace(input.GeneratorVersion)
	for index := range input.Derivations {
		input.Derivations[index] = normalizeDreamDerivationSource(input.Derivations[index])
	}
	if input.GeneratorKind == "" {
		input.GeneratorKind = "deterministic"
	}
	if input.GeneratorVersion == "" {
		input.GeneratorVersion = "dream-v2"
	}
	input.SourceOwnerProfileIDs = normalizeStringSet(input.SourceOwnerProfileIDs)
	if input.TargetIdentity == "" {
		input.TargetIdentity = hypothesisTargetIdentity(input.TeamID, input.SubjectEntityID, input.PredicateKey, input.ObjectEntityID, input.ObjectValueID)
	}
	return input
}

func normalizeDreamDerivationSource(input DreamDerivationSource) DreamDerivationSource {
	input.RelationshipID = strings.TrimSpace(input.RelationshipID)
	input.SupportID = strings.TrimSpace(input.SupportID)
	input.ObservationID = strings.TrimSpace(input.ObservationID)
	input.FragmentID = strings.TrimSpace(input.FragmentID)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.SourceRevisionID = strings.TrimSpace(input.SourceRevisionID)
	input.SourceGroupKey = strings.TrimSpace(input.SourceGroupKey)
	input.Authority = strings.TrimSpace(input.Authority)
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
	expectedTargetIdentity := hypothesisTargetIdentity(input.TeamID, input.SubjectEntityID, input.PredicateKey, input.ObjectEntityID, input.ObjectValueID)
	if input.TargetIdentity == "" || input.TargetIdentity != expectedTargetIdentity {
		return errors.New("target_identity must match the canonical hypothesis target")
	}
	if input.GeneratorKind != "evaluation_seed" && len(input.Derivations) == 0 {
		return errors.New("derivations are required")
	}
	premisePositions := make(map[int]struct{}, 2)
	for index, derivation := range input.Derivations {
		if derivation.PremisePosition != 1 && derivation.PremisePosition != 2 {
			return fmt.Errorf("derivations[%d].premise_position must be 1 or 2", index)
		}
		premisePositions[derivation.PremisePosition] = struct{}{}
		if _, err := uuid.Parse(strings.TrimSpace(derivation.RelationshipID)); err != nil {
			return fmt.Errorf("derivations[%d].relationship_id is invalid: %w", index, err)
		}
		if derivation.RelationshipVersion < 1 {
			return fmt.Errorf("derivations[%d].relationship_version must be greater than zero", index)
		}
		if (strings.TrimSpace(derivation.SupportID) == "") == (strings.TrimSpace(derivation.ObservationID) == "") {
			return fmt.Errorf("derivations[%d] must identify exactly one support or observation", index)
		}
		if derivation.SupportID != "" {
			if _, err := uuid.Parse(derivation.SupportID); err != nil {
				return fmt.Errorf("derivations[%d].support_id is invalid: %w", index, err)
			}
		}
		if derivation.ObservationID != "" {
			if _, err := uuid.Parse(derivation.ObservationID); err != nil {
				return fmt.Errorf("derivations[%d].observation_id is invalid: %w", index, err)
			}
		}
		for field, value := range map[string]string{
			"fragment_id":      derivation.FragmentID,
			"source_group_key": derivation.SourceGroupKey,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("derivations[%d].%s is required", index, field)
			}
		}
		if _, err := uuid.Parse(strings.TrimSpace(derivation.FragmentID)); err != nil {
			return fmt.Errorf("derivations[%d].fragment_id is invalid: %w", index, err)
		}
		if (derivation.SourceID == "") != (derivation.SourceRevisionID == "") {
			return fmt.Errorf("derivations[%d] must pair source_id and source_revision_id", index)
		}
		if derivation.SourceID != "" {
			if _, err := uuid.Parse(strings.TrimSpace(derivation.SourceID)); err != nil {
				return fmt.Errorf("derivations[%d].source_id is invalid: %w", index, err)
			}
			if _, err := uuid.Parse(strings.TrimSpace(derivation.SourceRevisionID)); err != nil {
				return fmt.Errorf("derivations[%d].source_revision_id is invalid: %w", index, err)
			}
		}
		if derivation.SpanStart < 0 || derivation.SpanEnd <= derivation.SpanStart {
			return fmt.Errorf("derivations[%d] has an invalid evidence span", index)
		}
		if strings.TrimSpace(derivation.Quote) == "" || strings.TrimSpace(derivation.Authority) == "" {
			return fmt.Errorf("derivations[%d] requires an exact quote and authority", index)
		}
		switch derivation.Authority {
		case "authoritative", "primary", "secondary", "inferred", "unknown":
		default:
			return fmt.Errorf("derivations[%d].authority is unsupported", index)
		}
	}
	if input.GeneratorKind != "evaluation_seed" && len(premisePositions) != 2 {
		return errors.New("dream derivations must cover both premise positions")
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
	case "reject":
		if input.Status != "rejected" {
			return fmt.Errorf("decision %q requires status %q", input.Decision, "rejected")
		}
	case "stale":
		if input.Status != "stale" {
			return fmt.Errorf("decision %q requires status %q", input.Decision, "stale")
		}
	case "reinforce":
		if input.Status != "reinforced" {
			return fmt.Errorf("decision %q requires status %q", input.Decision, "reinforced")
		}
	default:
		return fmt.Errorf("unsupported feedback decision %q", input.Decision)
	}
	return nil
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
