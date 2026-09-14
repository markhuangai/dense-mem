package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func applyDirectEvidenceSupersessions(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string, evidence []EvidenceFragment) error {
	if err := validateCreateIngestInput(normalizeCreateIngestInput(input)); err != nil {
		return err
	}
	return applyEvidenceSupersessions(ctx, tx, normalizeCreateIngestInput(input), ingestID, evidence)
}

func requireTestEvidenceFragment(t *testing.T, result *EvidenceIngestResult) EvidenceFragment {
	t.Helper()
	require.NotNil(t, result)
	require.Len(t, result.Evidence, 1)
	return result.Evidence[0]
}
