package repository

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const maxEvidenceItems = 100

func normalizeCreateIngestInput(input CreateIngestInput) CreateIngestInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.PlacementRunID = strings.TrimSpace(input.PlacementRunID)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	input.CompatibleRequestHashes = normalizeRequestHashes(input.RequestHash, input.CompatibleRequestHashes)
	input.SourceSummary = strings.TrimSpace(input.SourceSummary)
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = string(domain.PlacementRunQueued)
	}
	for i := range input.Evidence {
		input.Evidence[i].ContentHash = strings.TrimSpace(input.Evidence[i].ContentHash)
		if input.Evidence[i].ContentHash == "" && input.Evidence[i].Content != "" {
			input.Evidence[i].ContentHash = sha256Hex(input.Evidence[i].Content)
		}
		input.Evidence[i].SourceType = strings.TrimSpace(input.Evidence[i].SourceType)
		if input.Evidence[i].SourceType == "" {
			input.Evidence[i].SourceType = "conversation"
		}
		input.Evidence[i].Authority = strings.TrimSpace(input.Evidence[i].Authority)
		if input.Evidence[i].Authority == "" {
			input.Evidence[i].Authority = "primary"
		}
		input.Evidence[i].SourceRef = strings.TrimSpace(input.Evidence[i].SourceRef)
		input.Evidence[i].SourceKey = strings.TrimSpace(input.Evidence[i].SourceKey)
		input.Evidence[i].SourceRevisionToken = strings.TrimSpace(input.Evidence[i].SourceRevisionToken)
		input.Evidence[i].ExpectedPreviousRevisionToken = strings.TrimSpace(input.Evidence[i].ExpectedPreviousRevisionToken)
		input.Evidence[i].SourceRevisionContentHash = strings.TrimSpace(input.Evidence[i].SourceRevisionContentHash)
		input.Evidence[i].IdempotencyKey = strings.TrimSpace(input.Evidence[i].IdempotencyKey)
		if input.Evidence[i].SourceRevisionContentHash == "" && input.Evidence[i].SourceKey != "" {
			input.Evidence[i].SourceRevisionContentHash = input.Evidence[i].ContentHash
		}
		input.Evidence[i].SupersedesEvidenceIDs = normalizeUUIDStrings(input.Evidence[i].SupersedesEvidenceIDs)
	}
	return input
}

func normalizeRequestHashes(primary string, alternatives []string) []string {
	seen := map[string]struct{}{strings.TrimSpace(primary): {}}
	result := make([]string, 0, len(alternatives))
	for _, value := range alternatives {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func requestHashMatches(input CreateIngestInput, existing string) bool {
	if strings.TrimSpace(existing) == input.RequestHash {
		return true
	}
	for _, candidate := range input.CompatibleRequestHashes {
		if strings.TrimSpace(existing) == candidate {
			return true
		}
	}
	return false
}

func validateCreateIngestInput(input CreateIngestInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.IngestID != "" {
		if _, err := uuid.Parse(input.IngestID); err != nil {
			return fmt.Errorf("ingest_id is invalid: %w", err)
		}
	}
	if input.PlacementRunID != "" {
		if _, err := uuid.Parse(input.PlacementRunID); err != nil {
			return fmt.Errorf("placement_run_id is invalid: %w", err)
		}
	}
	if input.SpaceID == "" && input.SpaceGeneration != 0 {
		return errors.New("space_id is required when space_generation is set")
	}
	if input.SpaceID != "" {
		if _, err := uuid.Parse(input.SpaceID); err != nil {
			return fmt.Errorf("space_id is invalid: %w", err)
		}
		if input.SpaceGeneration < 1 {
			return errors.New("space_generation is required when space_id is set")
		}
	}
	if input.Status != string(domain.PlacementRunQueued) &&
		input.Status != string(domain.PlacementRunGuarded) &&
		input.Status != string(domain.PlacementRunQuarantined) &&
		input.Status != string(domain.PlacementRunCompleted) {
		return fmt.Errorf("unsupported ingest status %q", input.Status)
	}
	if input.IdempotencyKey != "" && input.RequestHash == "" {
		return errors.New("request_hash is required when idempotency_key is set")
	}
	if len(input.Evidence) == 0 {
		return errors.New("evidence is required")
	}
	if len(input.Evidence) > maxEvidenceItems {
		return fmt.Errorf("evidence count %d exceeds maximum %d", len(input.Evidence), maxEvidenceItems)
	}
	directTargets := make(map[string]int)
	for i, item := range input.Evidence {
		if strings.TrimSpace(item.Content) == "" {
			return fmt.Errorf("evidence[%d].content is required", i)
		}
		if item.ContentHash == "" {
			return fmt.Errorf("evidence[%d].content_hash is required", i)
		}
		if want := sha256Hex(item.Content); item.ContentHash != want {
			return fmt.Errorf("evidence[%d].content_hash does not match content hash", i)
		}
		if item.SourceType != "conversation" && item.SourceType != "document" && item.SourceType != "observation" && item.SourceType != "manual" {
			return fmt.Errorf("evidence[%d].source_type is unsupported", i)
		}
		if !domain.Authority(item.Authority).IsValid() {
			return fmt.Errorf("evidence[%d].authority is unsupported", i)
		}
		if item.SourceKey == "" && item.SourceRevisionToken != "" {
			return fmt.Errorf("evidence[%d].source_revision requires source_key", i)
		}
		if item.SourceKey != "" && item.SourceRevisionToken == "" {
			return fmt.Errorf("evidence[%d].source_key requires source_revision", i)
		}
		if item.SourceKey != "" && item.SourceRevisionContentHash == "" {
			return fmt.Errorf("evidence[%d].source_revision_content_hash is required", i)
		}
		if item.ExpectedPreviousRevisionToken != "" && item.SourceKey == "" {
			return fmt.Errorf("evidence[%d].previous_source_revision requires source_key and source_revision", i)
		}
		if len(item.SupersedesEvidenceIDs) > 0 {
			if item.ExpectedPreviousRevisionToken != "" {
				return fmt.Errorf("evidence[%d].supersedes_evidence_ids cannot be combined with previous_source_revision", i)
			}
			if len(item.SupersedesEvidenceIDs) > 50 {
				return fmt.Errorf("evidence[%d].supersedes_evidence_ids exceeds maximum 50", i)
			}
			for _, evidenceID := range item.SupersedesEvidenceIDs {
				if _, err := uuid.Parse(evidenceID); err != nil {
					return fmt.Errorf("evidence[%d].supersedes_evidence_ids contains invalid UUID %q: %w", i, evidenceID, err)
				}
				if previous, exists := directTargets[evidenceID]; exists {
					return fmt.Errorf("evidence[%d].supersedes_evidence_ids duplicates evidence target from evidence[%d]", i, previous)
				}
				directTargets[evidenceID] = i
			}
		}
		if item.InitialEvent != nil {
			if err := validateSecurityEventDraft(*item.InitialEvent); err != nil {
				return fmt.Errorf("evidence[%d].security_event: %w", i, err)
			}
		}
	}
	if err := validateSourceRevisionBatch(input.Evidence); err != nil {
		return err
	}
	return nil
}
