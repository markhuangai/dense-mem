package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *LedgerRepositoryImpl) PromoteSubmission(
	ctx context.Context,
	input PromoteSubmissionInput,
) (*SubmissionPromotionResult, error) {
	input = normalizePromoteSubmissionInput(input)
	if err := validatePromoteSubmissionInput(input); err != nil {
		return nil, err
	}
	result := &SubmissionPromotionResult{
		CanonicalIngestID:    input.Canonical.IngestID,
		PlacementRunID:       input.Canonical.PlacementRunID,
		RelationshipOutcomes: []SubmissionRelationshipOutcome{},
		EvidenceSearchState:  string(domain.SearchProjectionPending),
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := requireSubmissionLease(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, input.WorkerID, input.ExpectedAttempts); err != nil {
			return err
		}
		staged, err := loadSubmission(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, true)
		if err != nil {
			return err
		}
		if err := validatePromotionStaging(staged, input); err != nil {
			return err
		}
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}

		ingestID, created, err := insertKnowledgeIngest(ctx, tx, input.Canonical)
		if err != nil {
			return err
		}
		if !created || ingestID != input.Canonical.IngestID {
			return ErrSubmissionConflict
		}
		placementRunID, _, _, err := insertPlacementRun(ctx, tx, input.Canonical, ingestID)
		if err != nil {
			return err
		}
		if placementRunID != input.Canonical.PlacementRunID {
			return ErrSubmissionConflict
		}
		sources := make(map[string]SourceRevisionResult)
		evidence := make([]EvidenceFragment, 0, len(input.Canonical.Evidence))
		for index, item := range input.Canonical.Evidence {
			var source *SourceRevisionResult
			if item.SourceKey != "" {
				advanced, err := advanceSourceRevisionInTx(ctx, tx, AdvanceSourceRevisionInput{
					TeamID:                        input.TeamID,
					OwnerProfileID:                input.OwnerProfileID,
					SourceKey:                     item.SourceKey,
					SourceKind:                    sourceKindForEvidence(item.SourceType),
					Authority:                     item.Authority,
					RevisionToken:                 item.SourceRevisionToken,
					ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken,
					ContentHash:                   item.SourceRevisionContentHash,
					Envelope:                      item.SourceRevisionEnvelope,
				}, sources)
				if err != nil {
					return err
				}
				source = advanced
			}
			fragment, err := insertEvidenceFragment(ctx, tx, input.Canonical, ingestID, index, item, source)
			if err != nil {
				return err
			}
			evidence = append(evidence, fragment)
			if _, err := insertPlacementItem(ctx, tx, input.Canonical, ingestID, placementRunID, fragment, item); err != nil {
				return err
			}
		}
		if err := applyDirectEvidenceSupersessions(ctx, tx, input.Canonical, ingestID, evidence); err != nil {
			return err
		}
		if err := activatePromotedPlacementRun(ctx, tx, input); err != nil {
			return err
		}

		for _, commit := range input.Commits {
			committed, err := r.commitPlacementSemanticResultInTx(ctx, tx, commit)
			if err != nil {
				return err
			}
			if len(committed.ReviewTaskIDs) > 0 {
				return ErrSubmissionConflict
			}
			for _, relationship := range committed.RelationshipResults {
				outcome := SubmissionRelationshipOutcome{
					ProposalID: relationship.ProposalID,
					Status:     "accepted",
					ReasonCode: "assessed_and_promoted",
				}
				if relationship.Relationship != nil {
					outcome.RelationshipID = relationship.Relationship.RelationshipID
				}
				result.RelationshipOutcomes = append(result.RelationshipOutcomes, outcome)
			}
		}
		if err := markPromotedIngestCompleted(ctx, tx, input); err != nil {
			return err
		}
		if err := completePromotedSubmissionInTx(ctx, tx, input, result); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("submission promote: %w", err)
	}
	return result, nil
}

func normalizePromoteSubmissionInput(input PromoteSubmissionInput) PromoteSubmissionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.ReplacesQuarantinedSubmissionID = strings.TrimSpace(input.ReplacesQuarantinedSubmissionID)
	if input.Lease < time.Second {
		input.Lease = time.Minute
	}
	input.Canonical = normalizeCreateIngestInput(input.Canonical)
	for index := range input.Commits {
		input.Commits[index] = normalizeCommitPlacementSemanticInput(input.Commits[index])
		input.Commits[index].DeferRunFinalization = index < len(input.Commits)-1
	}
	return input
}

func validatePromoteSubmissionInput(input PromoteSubmissionInput) error {
	if err := validateSubmissionIdentity(input.TeamID, input.OwnerProfileID, input.SubmissionID); err != nil {
		return err
	}
	if input.WorkerID == "" || input.ExpectedAttempts < 1 || input.Lease < time.Second {
		return errors.New("submission promotion lease is required")
	}
	if input.Canonical.TeamID != input.TeamID || input.Canonical.OwnerProfileID != input.OwnerProfileID {
		return errors.New("canonical promotion ownership must match submission")
	}
	if input.Canonical.IngestID == "" || input.Canonical.PlacementRunID == "" || input.Canonical.Status != string(domain.PlacementRunProcessing) {
		return errors.New("canonical promotion must use explicit processing identifiers")
	}
	if err := validateCreateIngestInput(input.Canonical); err != nil {
		return fmt.Errorf("canonical promotion: %w", err)
	}
	if len(input.Commits) != len(input.Canonical.Evidence) {
		return errors.New("canonical promotion requires one commit per evidence item")
	}
	if len(input.EvidenceOutcomes) != len(input.Canonical.Evidence) {
		return errors.New("canonical promotion requires one evidence outcome per evidence item")
	}
	for index, commit := range input.Commits {
		if err := validateCommitPlacementSemanticInput(commit); err != nil {
			return fmt.Errorf("canonical promotion commit[%d]: %w", index, err)
		}
		if commit.TeamID != input.TeamID || commit.OwnerProfileID != input.OwnerProfileID ||
			commit.IngestID != input.Canonical.IngestID || commit.PlacementRunID != input.Canonical.PlacementRunID ||
			commit.WorkerID != input.WorkerID || commit.ExpectedAttempts != input.ExpectedAttempts ||
			commit.PlacementItemID != input.Canonical.Evidence[index].PlacementItemID {
			return fmt.Errorf("canonical promotion commit[%d] does not match submission scope", index)
		}
	}
	for index, outcome := range input.EvidenceOutcomes {
		if outcome.EvidenceIndex != index || strings.TrimSpace(outcome.Status) == "" || strings.TrimSpace(outcome.ReasonCode) == "" {
			return fmt.Errorf("canonical promotion evidence_outcomes[%d] is invalid", index)
		}
	}
	if input.ReplacesQuarantinedSubmissionID != "" {
		if _, err := uuid.Parse(input.ReplacesQuarantinedSubmissionID); err != nil {
			return fmt.Errorf("replaces_quarantined_submission_id is invalid: %w", err)
		}
	}
	return nil
}

func validatePromotionStaging(staged *Submission, input PromoteSubmissionInput) error {
	if staged == nil || staged.Status != string(domain.SubmissionProcessing) || staged.RequestHash != input.Canonical.RequestHash {
		return ErrSubmissionConflict
	}
	if staged.ReplacesQuarantinedSubmissionID != input.ReplacesQuarantinedSubmissionID {
		return ErrSubmissionConflict
	}
	if !samePromotionJSON(staged.Proposal, input.Canonical.Proposal) || len(staged.Evidence) != len(input.Canonical.Evidence) {
		return ErrSubmissionConflict
	}
	for index, stagedEvidence := range staged.Evidence {
		canonical := input.Canonical.Evidence[index]
		if stagedEvidence.Content != canonical.Content || stagedEvidence.ContentHash != canonical.ContentHash ||
			stagedEvidence.SourceType != canonical.SourceType || stagedEvidence.Source != canonical.SourceRef ||
			stagedEvidence.Authority != canonical.Authority || stagedEvidence.SourceKey != canonical.SourceKey ||
			stagedEvidence.SourceRevision != canonical.SourceRevisionToken ||
			stagedEvidence.PreviousSourceRevision != canonical.ExpectedPreviousRevisionToken ||
			stagedEvidence.IdempotencyKey != canonical.IdempotencyKey ||
			!samePromotionStrings(stagedEvidence.SupersedesEvidenceIDs, canonical.SupersedesEvidenceIDs) ||
			!samePromotionStrings(stagedEvidence.Labels, canonical.Labels) {
			return ErrSubmissionConflict
		}
	}
	return nil
}

func samePromotionJSON(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func samePromotionStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func activatePromotedPlacementRun(ctx context.Context, tx *gorm.DB, input PromoteSubmissionInput) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE placement_runs
		SET status = 'processing', attempts = ?, worker_id = ?,
			lease_until = clock_timestamp() + (? * interval '1 second'),
			started_at = clock_timestamp(), updated_at = clock_timestamp()
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid AND placement_run_id = ?::uuid
		  AND status = 'processing' AND attempts = 0
	`, input.ExpectedAttempts, input.WorkerID, int(input.Lease.Seconds()), input.TeamID, input.OwnerProfileID,
		input.Canonical.IngestID, input.Canonical.PlacementRunID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrSubmissionConflict
	}
	return nil
}

func markPromotedIngestCompleted(ctx context.Context, tx *gorm.DB, input PromoteSubmissionInput) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE knowledge_ingests
		SET status = 'completed', completed_at = clock_timestamp(), updated_at = clock_timestamp()
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND ingest_id = ?::uuid
		  AND status = 'processing'
	`, input.TeamID, input.OwnerProfileID, input.Canonical.IngestID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrSubmissionConflict
	}
	return nil
}

func completePromotedSubmissionInTx(
	ctx context.Context,
	tx *gorm.DB,
	input PromoteSubmissionInput,
	result *SubmissionPromotionResult,
) error {
	for _, outcome := range input.EvidenceOutcomes {
		index := outcome.EvidenceIndex
		if err := insertSubmissionOutcome(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, &index, "", outcome.Status, outcome.ReasonCode, map[string]any{"search_state": outcome.SearchState}); err != nil {
			return err
		}
	}
	for _, outcome := range result.RelationshipOutcomes {
		if err := insertSubmissionOutcome(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, nil, outcome.ProposalID, outcome.Status, outcome.ReasonCode, map[string]any{"relationship_id": outcome.RelationshipID}); err != nil {
			return err
		}
	}
	updated := tx.WithContext(ctx).Exec(`
		UPDATE submission_runs
		SET status = 'completed', error_code = '', canonical_ingest_id = ?::uuid,
			lease_until = NULL, worker_id = '', completed_at = clock_timestamp(), updated_at = clock_timestamp()
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
		  AND status = 'processing' AND worker_id = ? AND attempts = ? AND lease_until > clock_timestamp()
	`, input.Canonical.IngestID, input.TeamID, input.OwnerProfileID, input.SubmissionID, input.WorkerID, input.ExpectedAttempts)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrSubmissionLeaseConflict
	}
	if err := deleteSubmissionStage(ctx, tx, input.TeamID, input.SubmissionID); err != nil {
		return err
	}
	return nil
}
