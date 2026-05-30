package communityservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

type stubCommunityGDSClient struct {
	readResults []any
	readErrs    []error
	writeErr    error
	reads       int
	writes      int
}

func (s *stubCommunityGDSClient) ExecuteRead(context.Context, neo4j.ManagedTransactionWork) (any, error) {
	index := s.reads
	s.reads++
	if index < len(s.readErrs) && s.readErrs[index] != nil {
		return nil, s.readErrs[index]
	}
	if index < len(s.readResults) {
		return s.readResults[index], nil
	}
	return []map[string]any{}, nil
}

func (s *stubCommunityGDSClient) ExecuteWrite(context.Context, neo4j.ManagedTransactionWork) (any, error) {
	s.writes++
	if s.writeErr != nil {
		return nil, s.writeErr
	}
	return nil, nil
}

var _ gdsClient = (*stubCommunityGDSClient)(nil)

func TestBuildCommunitySummaries_PrefersCurrentFacts(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)

	summaries := buildCommunitySummaries("profile-1", []communitySummaryInput{
		{
			CommunityID: "7",
			MemberCount: 4,
			FactTriples: []communityTriple{
				{Subject: "alice", Predicate: "knows", Object: "bob"},
				{Subject: "alice", Predicate: "knows", Object: "carol"},
				{Subject: "bob", Predicate: "works_with", Object: "alice"},
			},
			ClaimTriples: []communityTriple{
				{Subject: "draft", Predicate: "mentions", Object: "topic"},
			},
		},
	}, now)

	require.Len(t, summaries, 1)
	got := summaries[0]
	require.Equal(t, "7", got.CommunityID)
	require.Equal(t, "profile-1", got.ProfileID)
	require.Equal(t, communitySummaryVersion, got.SummaryVersion)
	require.Equal(t, 4, got.MemberCount)
	require.Equal(t, now, got.LastSummarizedAt)
	require.Equal(t, []string{"alice", "bob", "carol"}, got.TopEntities)
	require.Equal(t, []string{"knows", "works_with"}, got.TopPredicates)
	require.Contains(t, got.Summary, "current facts")
	require.NotContains(t, got.Summary, "no current facts yet")
	require.True(t, strings.Contains(got.Summary, "alice knows bob") || strings.Contains(got.Summary, "alice knows carol"))
}

func TestBuildCommunitySummaries_FallsBackToClaims(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)

	summaries := buildCommunitySummaries("profile-2", []communitySummaryInput{
		{
			CommunityID: "11",
			MemberCount: 3,
			ClaimTriples: []communityTriple{
				{Subject: "project", Predicate: "needs", Object: "review"},
				{Subject: "review", Predicate: "blocks", Object: "launch"},
			},
		},
	}, now)

	require.Len(t, summaries, 1)
	got := summaries[0]
	require.Equal(t, []string{"review", "launch", "project"}, got.TopEntities)
	require.Equal(t, []string{"blocks", "needs"}, got.TopPredicates)
	require.Contains(t, got.Summary, "no current facts yet")
	require.Contains(t, got.Summary, "Claim themes center on")
}

func TestBuildCommunitySummaries_SortsBySizeThenID(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)

	summaries := buildCommunitySummaries("profile-3", []communitySummaryInput{
		{CommunityID: "b", MemberCount: 2},
		{CommunityID: "a", MemberCount: 2},
		{CommunityID: "z", MemberCount: 5},
	}, now)

	require.Len(t, summaries, 3)
	require.Equal(t, "z", summaries[0].CommunityID)
	require.Equal(t, "a", summaries[1].CommunityID)
	require.Equal(t, "b", summaries[2].CommunityID)
}

func TestCommunitySummaryHelpersRankAndNormalize(t *testing.T) {
	triples := []communityTriple{
		{Subject: " alice ", Predicate: "likes", Object: " coffee "},
		{Subject: "alice", Predicate: "likes", Object: "tea"},
		{Subject: "bob", Predicate: "knows", Object: "alice"},
		{Subject: "", Predicate: "knows", Object: " "},
	}

	entities := topCommunityTerms(triples, 2, func(triple communityTriple) []string {
		return []string{triple.Subject, triple.Object}
	})
	require.Equal(t, []string{"alice", "bob"}, entities)

	predicates := topCommunityTerms(triples, 1, func(triple communityTriple) []string {
		return []string{triple.Predicate}
	})
	require.Equal(t, []string{"knows"}, predicates)

	statements := topCommunityTriples(triples, 3)
	require.Equal(t, []string{"alice likes coffee", "alice likes tea", "bob knows alice"}, statements)
	require.Equal(t, []string{"a", "b"}, rankCommunityCounts(map[string]int{"b": 1, "a": 1}, 0))
	require.Equal(t, "trimmed", normalizeCommunityTerm(" trimmed "))
}

func TestBuildCommunitySummaryHandlesSparseCommunities(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	summary := buildCommunitySummary("profile-4", communitySummaryInput{
		CommunityID: "empty",
		MemberCount: 9,
	}, now)

	require.Equal(t, "empty", summary.CommunityID)
	require.Equal(t, "profile-4", summary.ProfileID)
	require.Empty(t, summary.TopEntities)
	require.Empty(t, summary.TopPredicates)
	require.Contains(t, summary.Summary, "limited structured knowledge")
}

func TestCommunityRowConverters(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)

	require.Nil(t, rowToCommunity(map[string]any{"community_id": ""}))
	got := rowToCommunity(map[string]any{
		"community_id":        stringerValue("c-1"),
		"team_id":             "profile-1",
		"level":               int32(2),
		"summary":             "summary",
		"summary_version":     communitySummaryVersion,
		"member_count":        int64(7),
		"top_entities":        []any{"alice", "", "bob"},
		"top_predicates":      []string{"likes"},
		"last_summarized_at":  now,
		"ignored_extra_field": "ignored",
	})

	require.NotNil(t, got)
	require.Equal(t, "c-1", got.CommunityID)
	require.Equal(t, "profile-1", got.ProfileID)
	require.Equal(t, 2, got.Level)
	require.Equal(t, 7, got.MemberCount)
	require.Equal(t, []string{"alice", "bob"}, got.TopEntities)
	require.Equal(t, []string{"likes"}, got.TopPredicates)
	require.Equal(t, now, got.LastSummarizedAt)
	require.Equal(t, "", communityString(99))
	require.Nil(t, communityStrings(99))
	require.Equal(t, 0, communityInt("bad"))
	require.True(t, communityTime("bad").IsZero())
}

func TestEnsureCommunityInputReusesExistingValue(t *testing.T) {
	inputs := map[string]*communitySummaryInput{
		"a": {CommunityID: "a", MemberCount: 2},
	}

	existing := ensureCommunityInput(inputs, "a")
	created := ensureCommunityInput(inputs, "b")

	require.Equal(t, 2, existing.MemberCount)
	require.Equal(t, "b", created.CommunityID)
	require.Same(t, created, inputs["b"])
}

func TestNeo4jCommunityStoreLoadSummaryInputsAggregatesRows(t *testing.T) {
	client := &stubCommunityGDSClient{
		readResults: []any{
			[]map[string]any{
				{"community_id": "b", "member_count": int64(2)},
				{"community_id": "a", "member_count": int64(5)},
				{"community_id": "", "member_count": int64(99)},
			},
			[]map[string]any{
				{"community_id": "a", "subject": "alice", "predicate": "knows", "object": "bob"},
				{"community_id": "c", "subject": "carol", "predicate": "owns", "object": "db"},
				{"community_id": "", "subject": "ignored"},
			},
			[]map[string]any{
				{"community_id": "b", "subject": "draft", "predicate": "mentions", "object": "topic"},
			},
		},
	}
	store := newNeo4jCommunityStore(client)

	inputs, err := store.LoadSummaryInputs(context.Background(), "profile-1")

	require.NoError(t, err)
	require.Len(t, inputs, 3)
	require.Equal(t, "a", inputs[0].CommunityID)
	require.Equal(t, 5, inputs[0].MemberCount)
	require.Equal(t, []communityTriple{{Subject: "alice", Predicate: "knows", Object: "bob"}}, inputs[0].FactTriples)
	require.Equal(t, "b", inputs[1].CommunityID)
	require.Equal(t, []communityTriple{{Subject: "draft", Predicate: "mentions", Object: "topic"}}, inputs[1].ClaimTriples)
	require.Equal(t, "c", inputs[2].CommunityID)
	require.Equal(t, 0, inputs[2].MemberCount)
	require.Equal(t, 3, client.reads)
}

func TestNeo4jCommunityStoreLoadSummaryInputsWrapsReadErrors(t *testing.T) {
	store := newNeo4jCommunityStore(&stubCommunityGDSClient{
		readResults: []any{[]map[string]any{{"community_id": "a", "member_count": int64(1)}}},
		readErrs:    []error{nil, errors.New("facts failed")},
	})

	_, err := store.LoadSummaryInputs(context.Background(), "profile-1")

	require.ErrorContains(t, err, "load community facts")
}

func TestNeo4jCommunityStoreGetListReplace(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	client := &stubCommunityGDSClient{
		readResults: []any{
			[]map[string]any{{
				"community_id":       "community-1",
				"team_id":            "profile-1",
				"level":              int64(1),
				"summary":            "summary",
				"summary_version":    communitySummaryVersion,
				"member_count":       int64(3),
				"top_entities":       []string{"alice"},
				"top_predicates":     []string{"knows"},
				"last_summarized_at": now,
			}},
			[]map[string]any{
				{"community_id": "community-2", "team_id": "profile-1", "member_count": int64(2)},
				{"community_id": "", "team_id": "profile-1"},
			},
		},
	}
	store := newNeo4jCommunityStore(client)

	got, err := store.Get(context.Background(), "profile-1", "community-1")
	require.NoError(t, err)
	require.Equal(t, "community-1", got.CommunityID)
	require.Equal(t, []string{"alice"}, got.TopEntities)

	listed, err := store.List(context.Background(), "profile-1", 5)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "community-2", listed[0].CommunityID)

	err = store.Replace(context.Background(), "profile-1", []*domain.Community{
		nil,
		{CommunityID: ""},
		{
			CommunityID:      "community-1",
			Level:            1,
			Summary:          "summary",
			SummaryVersion:   communitySummaryVersion,
			MemberCount:      3,
			TopEntities:      []string{"alice"},
			TopPredicates:    []string{"knows"},
			LastSummarizedAt: now,
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, client.writes)
}

func TestNeo4jCommunityStoreReadErrors(t *testing.T) {
	store := newNeo4jCommunityStore(&stubCommunityGDSClient{})
	got, err := store.Get(context.Background(), "profile-1", "missing")
	require.ErrorIs(t, err, ErrCommunityNotFound)
	require.Nil(t, got)

	store = newNeo4jCommunityStore(&stubCommunityGDSClient{readErrs: []error{errors.New("read failed")}})
	_, err = store.List(context.Background(), "profile-1", 0)
	require.ErrorContains(t, err, "list communities")

	store = newNeo4jCommunityStore(&stubCommunityGDSClient{writeErr: errors.New("write failed")})
	err = store.Replace(context.Background(), "profile-1", nil)
	require.ErrorContains(t, err, "replace communities")
}

type stringerValue string

func (s stringerValue) String() string {
	return string(s)
}
