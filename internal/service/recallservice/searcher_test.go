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
	calls       int
}

func (r *unitRecallScopedReader) ScopedRead(_ context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	r.calls++
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
		"fact_id":         "fact-1",
		"team_id":         "profile-1",
		"score":           float32(0.75),
		"valid_from":      now,
		"valid_to":        later,
		"recorded_at":     now,
		"recorded_to":     later,
		"authority_state": "overlay",
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
	require.Equal(t, "overlay", facts[0].AuthorityState)
	require.Contains(t, reader.lastQuery, "OVERLAYS")
	require.Contains(t, reader.lastQuery, "incoming_overlay_count")
	require.Contains(t, reader.lastQuery, "outgoing_overlay_count")

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

func TestRecallSearchersNormalizeLuceneQueryText(t *testing.T) {
	t.Run("fact recall uses plain-text query", func(t *testing.T) {
		reader := &unitRecallScopedReader{}

		got, err := NewFactSearcher(reader).SearchActive(context.Background(), "profile-1", "GitVibe workflows / git-vibe command", 5)

		require.NoError(t, err)
		require.Empty(t, got)
		require.Equal(t, "GitVibe workflows git vibe command", reader.lastParams["searchQuery"])
	})

	t.Run("claim recall uses plain-text query", func(t *testing.T) {
		reader := &unitRecallScopedReader{}

		got, err := NewClaimSearcher(reader).SearchValidated(context.Background(), "profile-1", "github.com/markhuangai/git-vibe", 5)

		require.NoError(t, err)
		require.Empty(t, got)
		require.Equal(t, "github com markhuangai git vibe", reader.lastParams["searchQuery"])
	})

	t.Run("punctuation-only query returns empty result without calling neo4j", func(t *testing.T) {
		reader := &unitRecallScopedReader{}

		got, err := NewFactSearcher(reader).SearchActive(context.Background(), "profile-1", "/", 5)

		require.NoError(t, err)
		require.Empty(t, got)
		require.Equal(t, 0, reader.calls)
	})
}
