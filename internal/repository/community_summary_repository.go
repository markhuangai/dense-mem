package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func (r *SemanticRepositoryImpl) RecordCommunitySummaryAttempt(ctx context.Context, input CommunitySummaryAttemptInput) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.CommunityID = strings.TrimSpace(input.CommunityID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.RunID); err != nil {
		return fmt.Errorf("run_id is required: %w", err)
	}
	if input.CommunityID != "" {
		if _, err := uuid.Parse(input.CommunityID); err != nil {
			return fmt.Errorf("community_id is invalid: %w", err)
		}
	}
	if input.Attempt < 1 || input.Attempt > 3 {
		return fmt.Errorf("attempt must be between one and three")
	}
	quotes, err := json.Marshal(input.AdmittedSupportQuotes)
	if err != nil {
		return fmt.Errorf("community: marshal summary support quotes: %w", err)
	}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Exec(`
			INSERT INTO community_summary_attempts (
				team_id, run_id, community_id, attempt, provider_model, prompt_hash,
				response_hash, input_hash, admitted_relationship_ids, admitted_evidence_ids,
				admitted_support_quotes, response_summary, valid, error_code
			) VALUES (
				?::uuid, ?::uuid, NULLIF(?, '')::uuid, ?, ?, ?, ?, ?, ?::uuid[], ?::uuid[], ?::jsonb, ?, ?, ?
			)
		`, input.TeamID, input.RunID, input.CommunityID, input.Attempt, input.ProviderModel,
			input.PromptHash, input.ResponseHash, input.InputHash, pq.Array(input.AdmittedRelationshipIDs),
			pq.Array(input.AdmittedEvidenceIDs), string(quotes), truncateCommunityError(input.ResponseSummary), input.Valid,
			truncateCommunityError(input.ErrorCode)).Error
	})
	if err != nil {
		return fmt.Errorf("community: record summary attempt: %w", err)
	}
	return nil
}
