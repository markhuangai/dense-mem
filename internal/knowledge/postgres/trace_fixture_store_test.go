//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	graphpostgres "github.com/markhuangai/dense-mem/internal/graph/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
)

type TraceRelationshipInput = tracepostgres.TraceRelationshipInput

type knowledgeTraceFixtureStore struct {
	trace *tracepostgres.Store
}

func newKnowledgeTraceFixtureStore(db *gorm.DB, rls storagepostgres.RLSHelper) *knowledgeTraceFixtureStore {
	trace := tracepostgres.New(db, rls, func(ctx context.Context, tx *gorm.DB, input graphcontract.Query, spaceID string) (*graphcontract.Snapshot, error) {
		rows, err := graphpostgres.LoadLocalRows(ctx, tx, input, spaceID)
		if err != nil {
			return nil, err
		}
		return graphpostgres.Snapshot(input, rows), nil
	}, nil)
	return &knowledgeTraceFixtureStore{trace: trace}
}

func (s *knowledgeTraceFixtureStore) TraceRelationship(ctx context.Context, input tracepostgres.TraceRelationshipInput) (*tracepostgres.RelationshipTraceResult, error) {
	return s.trace.TraceRelationship(ctx, input)
}

func assertDuplicateTraceOccurrences(
	t *testing.T,
	ctx context.Context,
	adminDB, appDB *gorm.DB,
	rls storagepostgres.RLSHelper,
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
	trace, err := newKnowledgeTraceFixtureStore(appDB, rls).TraceRelationship(ctx, TraceRelationshipInput{
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
		var evidence *tracepostgres.TraceEvidenceFragment
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
