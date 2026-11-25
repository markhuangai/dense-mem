package postgres

import (
	"context"

	"gorm.io/gorm"
)

func applyDirectEvidenceSupersessions(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string, evidence []EvidenceFragment) error {
	if err := validateCreateIngestInput(normalizeCreateIngestInput(input)); err != nil {
		return err
	}
	return applyEvidenceSupersessions(ctx, tx, normalizeCreateIngestInput(input), ingestID, evidence)
}
