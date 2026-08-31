package repository

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// validateRememberSubmissionSupersessionTargets locks and validates the
// lifecycle targets in the same transaction that will apply the accepted
// synchronous Remember commit. No staging intent rows are used.
func validateRememberSubmissionSupersessionTargets(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string) error {
	for _, item := range input.Evidence {
		if len(item.SupersedesEvidenceIDs) == 0 {
			continue
		}
		if _, err := planEvidenceLifecycle(ctx, tx, evidenceLifecycleOperationInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID,
			EvidenceIDs: item.SupersedesEvidenceIDs, ReplacementIngestID: ingestID,
		}); err != nil {
			return fmt.Errorf("remember supersession target preflight: %w", err)
		}
	}
	return nil
}
