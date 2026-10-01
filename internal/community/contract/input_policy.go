package contract

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func NormalizeCommunitySummaryUUIDs(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, err := uuid.Parse(value); err != nil {
			continue
		}
		out = append(out, value)
	}
	return out
}

func NormalizeCommunityRunClaimInput(input CommunityRunClaimInput, now time.Time) CommunityRunClaimInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.WindowKey = strings.TrimSpace(input.WindowKey)
	input.AlgorithmKind = strings.TrimSpace(input.AlgorithmKind)
	input.AlgorithmVersion = strings.TrimSpace(input.AlgorithmVersion)
	input.ProfileVersion = strings.TrimSpace(input.ProfileVersion)
	input.ConfigurationHash = strings.TrimSpace(input.ConfigurationHash)
	input.SourceFingerprint = strings.TrimSpace(input.SourceFingerprint)
	if input.WindowKey == "" {
		input.WindowKey = now.UTC().Format("2006-01-02")
	}
	if input.AlgorithmKind == "" {
		input.AlgorithmKind = CommunityAlgorithmKind
	}
	if input.AlgorithmVersion == "" {
		input.AlgorithmVersion = CommunityAlgorithmVersion
	}
	if input.ProfileVersion == "" {
		input.ProfileVersion = CommunityProfileVersion
	}
	if input.LeaseUntil.IsZero() {
		input.LeaseUntil = now.UTC().Add(30 * time.Second)
	}
	return input
}

func ValidateCommunityRunClaimInput(input CommunityRunClaimInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.WindowKey == "" {
		return errors.New("window_key is required")
	}
	if input.AlgorithmKind == "" || input.AlgorithmVersion == "" {
		return errors.New("algorithm kind and version are required")
	}
	if input.MaxNodes < 0 || input.MaxEdges < 0 {
		return errors.New("max nodes and edges must be non-negative")
	}
	return nil
}

func NormalizeCommunityRunCompleteInput(input CommunityRunCompleteInput) CommunityRunCompleteInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.Status = strings.TrimSpace(input.Status)
	input.Error = strings.TrimSpace(input.Error)
	if input.Status == "" {
		input.Status = "completed"
	}
	return input
}

func ValidateCommunityRunCompleteInput(input CommunityRunCompleteInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.RunID); err != nil {
		return fmt.Errorf("run_id is required: %w", err)
	}
	switch input.Status {
	case "completed", "failed", "skipped", "too_large", "cancelled":
	default:
		return fmt.Errorf("unsupported community run status %q", input.Status)
	}
	if input.NodeCount < 0 || input.EdgeCount < 0 || input.CommunityCount < 0 {
		return errors.New("counts must be non-negative")
	}
	return nil
}

func NormalizeCommunityInputListInput(input CommunityInputListInput) CommunityInputListInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	if input.Limit <= 0 {
		input.Limit = 500
	}
	if input.Limit > 5001 {
		input.Limit = 5001
	}
	return input
}

func ValidateCommunityInputListInput(input CommunityInputListInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	return nil
}

func NormalizeCommunitySnapshotPublishInput(input CommunitySnapshotPublishInput) CommunitySnapshotPublishInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.AlgorithmKind = strings.TrimSpace(input.AlgorithmKind)
	input.AlgorithmVersion = strings.TrimSpace(input.AlgorithmVersion)
	input.ProfileVersion = strings.TrimSpace(input.ProfileVersion)
	input.ConfigurationHash = strings.TrimSpace(input.ConfigurationHash)
	input.SourceFingerprint = strings.TrimSpace(input.SourceFingerprint)
	if input.AlgorithmKind == "" {
		input.AlgorithmKind = CommunityAlgorithmKind
	}
	if input.AlgorithmVersion == "" {
		input.AlgorithmVersion = CommunityAlgorithmVersion
	}
	if input.ProfileVersion == "" {
		input.ProfileVersion = CommunityProfileVersion
	}
	for i := range input.Communities {
		input.Communities[i].CommunityID = strings.TrimSpace(input.Communities[i].CommunityID)
		input.Communities[i].LogicalCommunityID = strings.TrimSpace(input.Communities[i].LogicalCommunityID)
		if input.Communities[i].LogicalCommunityID == "" {
			input.Communities[i].LogicalCommunityID = input.Communities[i].CommunityID
		}
		input.Communities[i].Summary = strings.TrimSpace(input.Communities[i].Summary)
		input.Communities[i].SummaryVersion = strings.TrimSpace(input.Communities[i].SummaryVersion)
		input.Communities[i].SourceFingerprint = strings.TrimSpace(input.Communities[i].SourceFingerprint)
		for j := range input.Communities[i].Memberships {
			input.Communities[i].Memberships[j].EntityID = strings.TrimSpace(input.Communities[i].Memberships[j].EntityID)
		}
		for j := range input.Communities[i].Sources {
			input.Communities[i].Sources[j].RelationshipID = strings.TrimSpace(input.Communities[i].Sources[j].RelationshipID)
			input.Communities[i].Sources[j].OwnerProfileID = strings.TrimSpace(input.Communities[i].Sources[j].OwnerProfileID)
			input.Communities[i].Sources[j].SemanticGroupKey = strings.TrimSpace(input.Communities[i].Sources[j].SemanticGroupKey)
			input.Communities[i].Sources[j].SourceStateHash = strings.TrimSpace(input.Communities[i].Sources[j].SourceStateHash)
		}
	}
	return input
}

func ValidateCommunitySnapshotPublishInput(input CommunitySnapshotPublishInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.RunID); err != nil {
		return fmt.Errorf("run_id is required: %w", err)
	}
	if input.SourceFingerprint == "" {
		return errors.New("source_fingerprint is required")
	}
	if input.NodeCount < 0 || input.EdgeCount < 0 {
		return errors.New("node and edge counts must be non-negative")
	}
	if len(input.Communities) == 0 {
		return nil
	}
	for _, community := range input.Communities {
		if _, err := uuid.Parse(community.CommunityID); err != nil {
			return fmt.Errorf("community_id is required: %w", err)
		}
		if _, err := uuid.Parse(community.LogicalCommunityID); err != nil {
			return fmt.Errorf("logical_community_id is required: %w", err)
		}
		if community.Summary == "" {
			return errors.New("community summary is required")
		}
		if len(community.Memberships) == 0 || len(community.Sources) == 0 {
			return errors.New("community memberships and sources are required")
		}
		for _, membership := range community.Memberships {
			if _, err := uuid.Parse(membership.EntityID); err != nil {
				return fmt.Errorf("membership entity_id is required: %w", err)
			}
			if membership.MembershipScore < 0 || membership.MembershipScore > 1 {
				return errors.New("membership_score must be between zero and one")
			}
		}
		for _, source := range community.Sources {
			if _, err := uuid.Parse(source.RelationshipID); err != nil {
				return fmt.Errorf("source relationship_id is required: %w", err)
			}
			if _, err := uuid.Parse(source.OwnerProfileID); err != nil {
				return fmt.Errorf("source owner_profile_id is required: %w", err)
			}
			if source.RelationshipVersion < 1 {
				return errors.New("source relationship_version must be greater than zero")
			}
		}
	}
	return nil
}

func NormalizeCommunityStalenessInput(input CommunityStalenessInput) CommunityStalenessInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	if input.Limit <= 0 {
		input.Limit = 500
	}
	if input.Limit > 5000 {
		input.Limit = 5000
	}
	return input
}

func ValidateCommunityStalenessInput(input CommunityStalenessInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	return nil
}

func NormalizeCommunityListInput(input CommunityListInput) CommunityListInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = "current"
	}
	if input.Limit <= 0 {
		input.Limit = 20
	}
	if input.Limit > 100 {
		input.Limit = 100
	}
	return input
}

func ValidateCommunityListInput(input CommunityListInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if !communityStatusValid(input.Status) {
		return fmt.Errorf("unsupported community status %q", input.Status)
	}
	return nil
}

func NormalizeCommunityGetInput(input CommunityGetInput) CommunityGetInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.CommunityID = strings.TrimSpace(input.CommunityID)
	return input
}

func ValidateCommunityGetInput(input CommunityGetInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.CommunityID); err != nil {
		return fmt.Errorf("community_id is required: %w", err)
	}
	return nil
}

func NormalizeCommunityDiscoveryInput(input CommunityDiscoveryInput) CommunityDiscoveryInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	input.KnownRelationshipIDs = normalizeRecallUUIDList(input.KnownRelationshipIDs)
	input.ExpandFromEntityIDs = normalizeRecallUUIDList(input.ExpandFromEntityIDs)
	if input.Limit <= 0 {
		input.Limit = 5
	}
	if input.Limit > 20 {
		input.Limit = 20
	}
	return input
}

func ValidateCommunityDiscoveryInput(input CommunityDiscoveryInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	for _, value := range input.KnownRelationshipIDs {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("known_relationship_ids contains invalid UUID %q: %w", value, err)
		}
	}
	for _, value := range input.ExpandFromEntityIDs {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("expand_from_entity_ids contains invalid UUID %q: %w", value, err)
		}
	}
	return nil
}

func normalizeRecallUUIDList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
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

func communityStatusValid(status string) bool {
	switch status {
	case "current", "stale", "superseded":
		return true
	default:
		return false
	}
}

func TruncateCommunityDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 512 {
		runes = runes[:512]
	}
	return strings.TrimSpace(string(runes))
}
