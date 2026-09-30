package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	maxEvidenceDiscoveryDerivations = 10
	maxEvidenceDiscoveryEvidenceIDs = 10
)

func NormalizeUpsertHypothesisInput(input UpsertHypothesisInput) UpsertHypothesisInput {
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
	if !input.Lane.IsValid() {
		input.Lane = domain.DreamLaneGraph
	}
	input.SourceEvidenceIDs = normalizeStringSet(input.SourceEvidenceIDs)
	if input.EvidenceDerivations != nil {
		input.EvidenceDerivations = append([]EvidenceDerivationSource{}, input.EvidenceDerivations...)
	}
	if input.Derivations != nil {
		input.Derivations = append([]DreamDerivationSource{}, input.Derivations...)
	}
	for index := range input.EvidenceDerivations {
		input.EvidenceDerivations[index] = normalizeEvidenceDerivationSource(input.EvidenceDerivations[index])
	}
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
		input.TargetIdentity = HypothesisTargetIdentity(input.TeamID, input.SubjectEntityID, input.PredicateKey, input.ObjectEntityID, input.ObjectValueID)
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

func normalizeEvidenceDerivationSource(input EvidenceDerivationSource) EvidenceDerivationSource {
	input.EvidenceID = strings.TrimSpace(input.EvidenceID)
	input.FragmentID = strings.TrimSpace(input.FragmentID)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.SourceRevisionID = strings.TrimSpace(input.SourceRevisionID)
	input.SourceGroupKey = strings.TrimSpace(input.SourceGroupKey)
	input.Authority = strings.TrimSpace(input.Authority)
	return input
}

func ValidateUpsertHypothesisInput(input UpsertHypothesisInput, system bool) error {
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
	if input.Lane != domain.DreamLaneEvidenceDiscovery && len(input.SourceVersions) == 0 {
		return errors.New("source_versions is required")
	}
	if input.ContentHash == "" {
		return errors.New("content_hash is required")
	}
	if !input.Lane.IsValid() {
		return fmt.Errorf("unsupported dream lane %q", input.Lane)
	}
	expectedTargetIdentity := HypothesisTargetIdentity(input.TeamID, input.SubjectEntityID, input.PredicateKey, input.ObjectEntityID, input.ObjectValueID)
	if input.TargetIdentity == "" || input.TargetIdentity != expectedTargetIdentity {
		return errors.New("target_identity must match the canonical hypothesis target")
	}
	if input.Lane == domain.DreamLaneEvidenceDiscovery {
		if len(input.EvidenceDerivations) == 0 || len(input.EvidenceDerivations) > maxEvidenceDiscoveryDerivations ||
			len(input.SourceEvidenceIDs) == 0 || len(input.SourceEvidenceIDs) > maxEvidenceDiscoveryEvidenceIDs {
			return errors.New("evidence discovery hypotheses require evidence derivations")
		}
		seenEvidenceIDs := make(map[string]struct{}, len(input.SourceEvidenceIDs))
		for index, evidenceID := range input.SourceEvidenceIDs {
			if _, err := uuid.Parse(evidenceID); err != nil {
				return fmt.Errorf("source_evidence_ids[%d] is invalid: %w", index, err)
			}
			if _, exists := seenEvidenceIDs[evidenceID]; exists {
				return fmt.Errorf("source_evidence_ids[%d] is duplicated", index)
			}
			seenEvidenceIDs[evidenceID] = struct{}{}
		}
		derivationEvidenceIDs := make(map[string]struct{}, len(input.EvidenceDerivations))
		for index, derivation := range input.EvidenceDerivations {
			derivationEvidenceIDs[derivation.EvidenceID] = struct{}{}
			if _, err := uuid.Parse(derivation.EvidenceID); err != nil {
				return fmt.Errorf("evidence_derivations[%d].evidence_id is invalid: %w", index, err)
			}
			if _, err := uuid.Parse(derivation.FragmentID); err != nil {
				return fmt.Errorf("evidence_derivations[%d].fragment_id is invalid: %w", index, err)
			}
			if derivation.EvidenceID != derivation.FragmentID {
				return fmt.Errorf("evidence_derivations[%d] evidence_id and fragment_id must match", index)
			}
			if (derivation.SourceID == "") != (derivation.SourceRevisionID == "") {
				return fmt.Errorf("evidence_derivations[%d] must pair source_id and source_revision_id", index)
			}
			if derivation.SourceID != "" {
				if _, err := uuid.Parse(derivation.SourceID); err != nil {
					return fmt.Errorf("evidence_derivations[%d].source_id is invalid: %w", index, err)
				}
				if _, err := uuid.Parse(derivation.SourceRevisionID); err != nil {
					return fmt.Errorf("evidence_derivations[%d].source_revision_id is invalid: %w", index, err)
				}
			}
			if !domain.Authority(derivation.Authority).IsValid() {
				return fmt.Errorf("evidence_derivations[%d].authority is unsupported", index)
			}
			if derivation.SourceGroupKey == "" || derivation.Quote == "" || derivation.SpanStart < 0 || derivation.SpanEnd <= derivation.SpanStart {
				return fmt.Errorf("evidence_derivations[%d] is incomplete", index)
			}
		}
		for evidenceID := range seenEvidenceIDs {
			if _, exists := derivationEvidenceIDs[evidenceID]; !exists {
				return fmt.Errorf("source_evidence_ids contains evidence without a derivation")
			}
		}
		for evidenceID := range derivationEvidenceIDs {
			if _, exists := seenEvidenceIDs[evidenceID]; !exists {
				return fmt.Errorf("evidence derivations contain evidence missing from source_evidence_ids")
			}
		}
		return nil
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

func HypothesisTargetIdentity(teamID, subjectEntityID, predicateKey, objectEntityID, objectValueID string) string {
	object := "value:" + strings.TrimSpace(objectValueID)
	if strings.TrimSpace(objectEntityID) != "" {
		object = "entity:" + strings.TrimSpace(objectEntityID)
	}
	raw := strings.Join([]string{
		strings.TrimSpace(teamID),
		strings.TrimSpace(subjectEntityID),
		strings.ToLower(strings.TrimSpace(predicateKey)),
		object,
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
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
