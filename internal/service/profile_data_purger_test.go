package service

import (
	"context"
	"strings"
	"testing"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

type recordingScopedWriter struct {
	profileIDs []string
	queries    []string
}

func (w *recordingScopedWriter) ScopedWrite(_ context.Context, profileID string, query string, _ map[string]any) (neo4jdriver.ResultSummary, error) {
	w.profileIDs = append(w.profileIDs, profileID)
	w.queries = append(w.queries, query)
	return nil, nil
}

func TestNeo4jProfileDataPurgerPurgesRelationshipsAndNodesByProfile(t *testing.T) {
	t.Parallel()

	writer := &recordingScopedWriter{}
	purger := NewNeo4jProfileDataPurger(writer)

	err := purger.PurgeProfileData(context.Background(), "profile-123")
	require.NoError(t, err)
	require.Len(t, writer.queries, 2)
	require.Equal(t, []string{"profile-123", "profile-123"}, writer.profileIDs)
	require.Contains(t, strings.Join(writer.queries, "\n"), "r.team_id = $profileId")
	require.Contains(t, strings.Join(writer.queries, "\n"), "n.team_id = $profileId")
	require.Contains(t, strings.Join(writer.queries, "\n"), "DETACH DELETE n")
}
