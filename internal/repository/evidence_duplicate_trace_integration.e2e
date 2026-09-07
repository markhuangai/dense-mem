package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func assertDuplicateTraceOccurrences(
	t *testing.T,
	ctx context.Context,
	adminDB, appDB *gorm.DB,
	rls *postgres.RLS,
	teamID string,
	input SynchronousRememberCommitInput,
	occurrences []string,
) {
	t.Helper()
	var relationshipID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT relationship_id::text
			FROM relationship_evidence_supports
			WHERE team_id = ?::uuid AND source_group_key = 'support-lineage-group-0'
		`, teamID).Row().Scan(&relationshipID)
	}))
	trace, err := NewSemanticRepository(appDB, rls).TraceRelationship(ctx, TraceRelationshipInput{
		TeamID: teamID, RelationshipID: relationshipID,
	})
	require.NoError(t, err)
	require.Len(t, trace.EvidenceSupports, 2)
	require.Len(t, trace.EvidenceFragments, 2)
	expectedContent := map[string]string{
		occurrences[0]: input.Evidence[0].Content,
		occurrences[1]: input.Evidence[1].Content,
	}
	for _, support := range trace.EvidenceSupports {
		content, ok := expectedContent[support.OccurrenceID]
		require.True(t, ok, "trace support occurrence %q", support.OccurrenceID)
		var evidence *TraceEvidenceFragment
		for index := range trace.EvidenceFragments {
			if trace.EvidenceFragments[index].OccurrenceID == support.OccurrenceID {
				evidence = &trace.EvidenceFragments[index]
				break
			}
		}
		require.NotNil(t, evidence, "trace evidence occurrence %q", support.OccurrenceID)
		require.Equal(t, content, evidence.Content)
		require.Equal(t, support.Quote, evidence.Content)
	}
}
