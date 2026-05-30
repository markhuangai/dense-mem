package recallservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

type unitRecallScopedReader struct {
	rows        []map[string]any
	err         error
	lastProfile string
	lastQuery   string
	lastParams  map[string]any
}

func (r *unitRecallScopedReader) ScopedRead(_ context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	r.lastProfile = profileID
	r.lastQuery = query
	r.lastParams = params
	if r.err != nil {
		return nil, nil, r.err
	}
	return nil, r.rows, nil
}

func TestRecallSearchersConvertRowsAndWrapErrors(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	reader := &unitRecallScopedReader{rows: []map[string]any{{
		"fact_id":     "fact-1",
		"team_id":     "profile-1",
		"score":       float32(0.75),
		"valid_from":  now,
		"valid_to":    later,
		"recorded_at": now,
		"recorded_to": later,
	}}}

	facts, err := NewFactSearcher(reader).SearchActive(context.Background(), "profile-1", "alice", 5)
	require.NoError(t, err)
	require.Len(t, facts, 1)
	require.Equal(t, "fact-1", facts[0].FactID)
	require.Equal(t, "profile-1", reader.lastProfile)
	require.Equal(t, "alice", reader.lastParams["searchQuery"])
	require.Equal(t, 5, reader.lastParams["limit"])
	require.InDelta(t, 0.75, facts[0].Score, 1e-9)
	require.NotNil(t, facts[0].ValidFrom)
	require.NotNil(t, facts[0].RecordedTo)

	reader = &unitRecallScopedReader{rows: []map[string]any{{
		"claim_id":    "claim-1",
		"team_id":     "profile-1",
		"score":       float64(0.5),
		"recorded_at": now,
	}}}
	claims, err := NewClaimSearcher(reader).SearchValidated(context.Background(), "profile-1", "alice", 3)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, "claim-1", claims[0].ClaimID)
	require.InDelta(t, 0.5, claims[0].Score, 1e-9)

	reader = &unitRecallScopedReader{err: errors.New("read failed")}
	_, err = NewFactSearcher(reader).SearchActive(context.Background(), "profile-1", "alice", 5)
	require.ErrorContains(t, err, "recall fact search")
	_, err = NewClaimSearcher(reader).SearchValidated(context.Background(), "profile-1", "alice", 5)
	require.ErrorContains(t, err, "recall claim search")
}

func TestRecallSearcherHelpersReturnZeroValuesForWrongTypes(t *testing.T) {
	row := map[string]any{"string": 1, "float": "bad", "time": "bad"}
	require.Equal(t, "", recallString(row, "string"))
	require.Equal(t, 0.0, recallFloat64(row, "float"))
	require.Nil(t, recallTimePtr(row, "time"))
	require.True(t, recallTime(row, "time").IsZero())
}
