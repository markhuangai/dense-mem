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

type sequenceRecallScopedReader struct {
	rowSets  [][]map[string]any
	errs     []error
	profiles []string
	queries  []string
	params   []map[string]any
}

func (r *sequenceRecallScopedReader) ScopedRead(_ context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	call := len(r.queries)
	r.profiles = append(r.profiles, profileID)
	r.queries = append(r.queries, query)
	r.params = append(r.params, params)
	if call < len(r.errs) && r.errs[call] != nil {
		return nil, nil, r.errs[call]
	}
	if call < len(r.rowSets) {
		return nil, r.rowSets[call], nil
	}
	return nil, nil, nil
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
	require.Contains(t, reader.lastQuery, "DECOMPOSED_INTO")
	require.Contains(t, reader.lastQuery, ":Assertion {team_id: $profileId, status: 'active'}")

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
	require.Contains(t, reader.lastQuery, "DECOMPOSED_INTO")

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

func TestCommunityExpanderSkipsEmptyAndAcceptsStaleCommunities(t *testing.T) {
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	reader := &sequenceRecallScopedReader{
		rowSets: [][]map[string]any{
			{
				{
					"community_id":       "c-empty",
					"team_id":            "profile-1",
					"summary":            "",
					"member_count":       int64(3),
					"last_summarized_at": now,
				},
				{
					"community_id":       "c-stale",
					"team_id":            "profile-1",
					"summary":            "Mars launch planning and orbital telemetry decisions.",
					"member_count":       int64(4),
					"top_entities":       []any{"Mars", "telemetry"},
					"top_predicates":     []any{"launch"},
					"last_summarized_at": old,
				},
			},
			{
				{
					"community_id": "c-stale",
					"member_type":  "fact",
					"fact_id":      "fact-1",
					"team_id":      "profile-1",
					"recorded_at":  now,
					"score":        0.8,
				},
			},
		},
	}

	expansion, err := NewCommunityExpander(reader).Expand(context.Background(), "profile-1", "mars telemetry", CommunityExpansionOptions{
		CommunityLimit:      2,
		MembersPerCommunity: 5,
		MaxCandidates:       10,
		ScanLimit:           10,
		MinScore:            0.1,
	})

	require.NoError(t, err)
	require.Equal(t, 1, expansion.SelectedCommunities)
	require.Len(t, expansion.Facts, 1)
	require.Equal(t, "fact-1", expansion.Facts[0].FactID)
	require.Equal(t, []string{"profile-1", "profile-1"}, reader.profiles)
	require.Contains(t, reader.queries[0], "MATCH (c:Community {team_id: $profileId})")
	require.Contains(t, reader.queries[1], "UNWIND $communityIds AS communityId")
	require.Contains(t, reader.queries[1], "coalesce(n.status, 'active') = 'active'", "community recall must exclude quarantined evidence")
	require.Contains(t, reader.queries[1], "DECOMPOSED_INTO", "community recall must suppress migrated legacy members")
	require.Equal(t, 10, reader.params[0]["limit"])
	require.Equal(t, []string{"c-stale"}, reader.params[1]["communityIds"])
	require.Equal(t, 5, reader.params[1]["membersPerCommunity"])
	require.Equal(t, 10, reader.params[1]["maxCandidates"])
}

func TestCommunityExpanderFiltersProfileAndNormalizesBounds(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	reader := &sequenceRecallScopedReader{
		rowSets: [][]map[string]any{
			{
				{
					"community_id":       "c-other",
					"team_id":            "profile-2",
					"summary":            "Mars launch planning.",
					"member_count":       5,
					"last_summarized_at": now,
				},
				{
					"community_id":       "c-own",
					"team_id":            "profile-1",
					"summary":            "Mars launch planning.",
					"member_count":       4,
					"last_summarized_at": now,
				},
			},
			{
				{
					"community_id": "c-own",
					"member_type":  "fragment",
					"fragment_id":  "frag-1",
					"team_id":      "profile-1",
					"score":        0.5,
				},
			},
		},
	}

	expansion, err := NewCommunityExpander(reader).Expand(context.Background(), "profile-1", "mars launch", CommunityExpansionOptions{
		CommunityLimit:      50,
		MembersPerCommunity: 50,
		MaxCandidates:       500,
		ScanLimit:           500,
	})

	require.NoError(t, err)
	require.Equal(t, 1, expansion.SelectedCommunities)
	require.Len(t, expansion.Fragments, 1)
	require.Equal(t, "frag-1", expansion.Fragments[0].FragmentID)
	require.Equal(t, 100, reader.params[0]["limit"])
	require.Equal(t, []string{"c-own"}, reader.params[1]["communityIds"])
	require.Equal(t, 25, reader.params[1]["membersPerCommunity"])
	require.Equal(t, 100, reader.params[1]["maxCandidates"])
}

func TestCommunityExpanderHandlesEmptyQueryAndReadErrors(t *testing.T) {
	reader := &sequenceRecallScopedReader{}
	expansion, err := NewCommunityExpander(reader).Expand(context.Background(), "profile-1", "///", CommunityExpansionOptions{})
	require.NoError(t, err)
	require.Zero(t, expansion.SelectedCommunities)
	require.Empty(t, reader.queries)

	listErr := errors.New("list failed")
	reader = &sequenceRecallScopedReader{errs: []error{listErr}}
	_, err = NewCommunityExpander(reader).Expand(context.Background(), "profile-1", "mars", CommunityExpansionOptions{})
	require.ErrorContains(t, err, "recall community list")

	memberErr := errors.New("members failed")
	reader = &sequenceRecallScopedReader{
		rowSets: [][]map[string]any{
			{
				{
					"community_id": "c-1",
					"team_id":      "profile-1",
					"summary":      "Mars launch",
					"member_count": 1,
				},
			},
		},
		errs: []error{nil, memberErr},
	}
	_, err = NewCommunityExpander(reader).Expand(context.Background(), "profile-1", "mars", CommunityExpansionOptions{MinScore: 0.1})
	require.ErrorContains(t, err, "recall community members")
}

func TestCommunityExpanderReturnsMixedMemberTypesAndScores(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	reader := &sequenceRecallScopedReader{
		rowSets: [][]map[string]any{
			{
				{
					"community_id": "c-1",
					"team_id":      "profile-1",
					"summary":      "Mars launch telemetry",
					"member_count": 3,
				},
			},
			{
				{
					"community_id": "c-1",
					"member_type":  "claim",
					"claim_id":     "claim-1",
					"team_id":      "profile-1",
					"recorded_at":  now,
					"score":        0.5,
				},
				{
					"community_id": "c-1",
					"member_type":  "fragment",
					"fragment_id":  "fragment-1",
					"team_id":      "profile-1",
				},
				{
					"community_id": "c-1",
					"member_type":  "unknown",
					"team_id":      "profile-1",
				},
			},
		},
	}

	expansion, err := NewCommunityExpander(reader).Expand(context.Background(), "profile-1", "mars launch", CommunityExpansionOptions{MinScore: 0.1})
	require.NoError(t, err)
	require.Equal(t, 1, expansion.SelectedCommunities)
	require.Len(t, expansion.Claims, 1)
	require.Equal(t, "claim-1", expansion.Claims[0].ClaimID)
	require.InDelta(t, 0.5, expansion.Claims[0].Score, 1e-9)
	require.Len(t, expansion.Fragments, 1)
	require.Equal(t, "fragment-1", expansion.Fragments[0].FragmentID)
	require.InDelta(t, 1.0, expansion.Fragments[0].Score, 1e-9)
}

func TestCommunityExpanderHelpersCoverFallbackBranches(t *testing.T) {
	require.Equal(t, []string{"mars", "launch"}, communityQueryTokens("the Mars / launch and the Mars"))
	require.Equal(t, 0.4, communityMemberScore(0, 0.4))
	require.Equal(t, 0.8, communityMemberScore(0.8, 0))
	require.Equal(t, 0.2, communityMemberScore(0.5, 0.4))

	row := map[string]any{
		"int":     int32(7),
		"float":   int64(8),
		"strings": []string{"a", "b"},
	}
	require.Equal(t, 7, recallInt(row, "int"))
	require.Equal(t, 8.0, recallFloat64(row, "float"))
	require.Equal(t, []string{"a", "b"}, recallStringSlice(row, "strings"))
	require.Nil(t, recallStringSlice(row, "missing"))
	require.Nil(t, recallRowToCommunity(map[string]any{}))
}
