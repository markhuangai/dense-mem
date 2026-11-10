//go:build integration

package postgres

import (
	"context"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	trace "github.com/markhuangai/dense-mem/internal/trace/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func assertDuplicateTraceOccurrences(t *testing.T, ctx context.Context, _ *gorm.DB, app *gorm.DB, rls storage.RLSHelper, team string, input SynchronousRememberCommitInput, occurrences []string) {
	t.Helper()
	root := input.DuplicateResolutions[0].CandidateFragmentID
	result, err := trace.New(app, rls).TraceEvidence(ctx, trace.TraceEvidenceInput{TeamID: team, EvidenceID: root})
	require.NoError(t, err)
	require.Equal(t, root, result.Evidence.FragmentID)
	byID := map[string]trace.TraceEvidenceFragment{}
	for _, occurrence := range result.Occurrences {
		byID[occurrence.OccurrenceID] = occurrence
	}
	for index, id := range occurrences {
		require.Contains(t, byID, id)
		require.Equal(t, input.Evidence[index].Content, byID[id].Content)
		require.Equal(t, input.OwnerProfileID, byID[id].OccurrenceOwnerProfileID)
		require.Equal(t, input.DuplicateResolutions[index].CandidateOwnerID, byID[id].OwnerProfileID)
	}
}
