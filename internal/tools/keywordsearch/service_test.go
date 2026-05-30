package keywordsearch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeScopedReader implements ScopedReaderInterface for testing the neo4j searchers directly.
type fakeScopedReader struct {
	rows []map[string]any
}

func (f *fakeScopedReader) ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	return nil, f.rows, nil
}

// mockFragmentSearcher implements FragmentSearcherInterface for testing.
type mockFragmentSearcher struct {
	searchContentFunc func(ctx context.Context, profileID string, query string, labels []string, limit int) ([]FragmentSearchResult, error)
}

func (m *mockFragmentSearcher) SearchContent(ctx context.Context, profileID string, query string, labels []string, limit int) ([]FragmentSearchResult, error) {
	if m.searchContentFunc != nil {
		return m.searchContentFunc(ctx, profileID, query, labels, limit)
	}
	return []FragmentSearchResult{}, nil
}

// mockFactSearcher implements FactSearcherInterface for testing.
type mockFactSearcher struct {
	searchPredicateFunc func(ctx context.Context, profileID string, query string, labels []string, limit int) ([]FactSearchResult, error)
}

func (m *mockFactSearcher) SearchPredicate(ctx context.Context, profileID string, query string, labels []string, limit int) ([]FactSearchResult, error) {
	if m.searchPredicateFunc != nil {
		return m.searchPredicateFunc(ctx, profileID, query, labels, limit)
	}
	return []FactSearchResult{}, nil
}

// TestKeywordSearchProfileFiltering tests that profile B results contain zero profile A data.
// This verifies defense-in-depth profile filtering works at the Go post-filter level.
func TestKeywordSearchProfileFiltering(t *testing.T) {
	profileA := "profile-a-id"
	profileB := "profile-b-id"

	tests := []struct {
		name               string
		requestingProfile  string
		fragmentResults    []FragmentSearchResult
		factResults        []FactSearchResult
		expectedProfileIDs []string // All should match requesting profile
	}{
		{
			name:              "profile B sees only profile B fragments and facts",
			requestingProfile: profileB,
			fragmentResults: []FragmentSearchResult{
				{FragmentID: "frag-1", Content: "content from profile A", Score: 0.9, ProfileID: profileA},
				{FragmentID: "frag-2", Content: "content from profile B", Score: 0.8, ProfileID: profileB},
				{FragmentID: "frag-3", Content: "more from profile A", Score: 0.95, ProfileID: profileA},
			},
			factResults: []FactSearchResult{
				{FactID: "fact-1", Predicate: "fact from profile A", Score: 0.7, ProfileID: profileA},
				{FactID: "fact-2", Predicate: "fact from profile B", Score: 0.6, ProfileID: profileB},
			},
			expectedProfileIDs: []string{profileB, profileB}, // Only profile B results
		},
		{
			name:              "profile A sees only profile A fragments and facts",
			requestingProfile: profileA,
			fragmentResults: []FragmentSearchResult{
				{FragmentID: "frag-1", Content: "content from profile A", Score: 0.9, ProfileID: profileA},
				{FragmentID: "frag-2", Content: "content from profile B", Score: 0.95, ProfileID: profileB},
			},
			factResults: []FactSearchResult{
				{FactID: "fact-1", Predicate: "fact from profile A", Score: 0.7, ProfileID: profileA},
				{FactID: "fact-2", Predicate: "fact from profile B", Score: 0.8, ProfileID: profileB},
				{FactID: "fact-3", Predicate: "more from profile A", Score: 0.6, ProfileID: profileA},
			},
			expectedProfileIDs: []string{profileA, profileA, profileA}, // Only profile A results
		},
		{
			name:              "all results from other profile - empty result",
			requestingProfile: profileB,
			fragmentResults: []FragmentSearchResult{
				{FragmentID: "frag-1", Content: "content from profile A", Score: 0.9, ProfileID: profileA},
				{FragmentID: "frag-2", Content: "more from profile A", Score: 0.95, ProfileID: profileA},
			},
			factResults: []FactSearchResult{
				{FactID: "fact-1", Predicate: "fact from profile A", Score: 0.7, ProfileID: profileA},
			},
			expectedProfileIDs: []string{}, // No results for profile B
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			mockFragment := &mockFragmentSearcher{
				searchContentFunc: func(ctx context.Context, pid string, query string, labels []string, limit int) ([]FragmentSearchResult, error) {
					// Return all results regardless of profile (simulating Cypher filter bypass attempt)
					return tt.fragmentResults, nil
				},
			}

			mockFact := &mockFactSearcher{
				searchPredicateFunc: func(ctx context.Context, pid string, query string, labels []string, limit int) ([]FactSearchResult, error) {
					// Return all results regardless of profile (simulating Cypher filter bypass attempt)
					return tt.factResults, nil
				},
			}

			svc := NewKeywordSearchService(mockFragment, mockFact)

			req := &KeywordSearchRequest{
				Query: "test query",
				Limit: 10,
			}

			result, err := svc.Search(ctx, tt.requestingProfile, req)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Verify all results belong to the requesting profile (defense-in-depth post-filter)
			for _, hit := range result.Data {
				assert.Equal(t, tt.requestingProfile, hit.ProfileID, "result should belong to requesting profile")
			}

			// Verify expected count
			assert.Len(t, result.Data, len(tt.expectedProfileIDs), "result count should match expected")
		})
	}
}

// TestKeywordSearchLimitCap tests that limit capping applies at 100 with meta.limit_applied set.
func TestKeywordSearchLimitCap(t *testing.T) {
	profileID := "test-profile-id"

	tests := []struct {
		name             string
		requestLimit     int
		expectedLimitCap int
		expect422        bool
	}{
		{
			name:         "limit 0 returns 422 validation error",
			requestLimit: 0,
			expect422:    true,
		},
		{
			name:             "limit 50 is not capped",
			requestLimit:     50,
			expectedLimitCap: 50,
		},
		{
			name:             "limit 100 is not capped (at max)",
			requestLimit:     100,
			expectedLimitCap: 100,
		},
		{
			name:             "limit 150 capped to 100",
			requestLimit:     150,
			expectedLimitCap: 100,
		},
		{
			name:             "limit 1000 capped to 100",
			requestLimit:     1000,
			expectedLimitCap: 100,
		},
		{
			name:             "default limit when negative",
			requestLimit:     -1,
			expectedLimitCap: DefaultLimit, // 20
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			mockFragment := &mockFragmentSearcher{
				searchContentFunc: func(ctx context.Context, pid string, query string, labels []string, limit int) ([]FragmentSearchResult, error) {
					// Generate more results than limit to test capping
					results := make([]FragmentSearchResult, 200)
					for i := 0; i < 200; i++ {
						results[i] = FragmentSearchResult{
							FragmentID: "frag-" + string(rune(i)),
							Content:    "content",
							Score:      float64(200-i) / 200.0, // Descending scores
							ProfileID:  pid,
						}
					}
					return results, nil
				},
			}

			mockFact := &mockFactSearcher{
				searchPredicateFunc: func(ctx context.Context, pid string, query string, labels []string, limit int) ([]FactSearchResult, error) {
					return []FactSearchResult{}, nil
				},
			}

			svc := NewKeywordSearchService(mockFragment, mockFact)

			req := &KeywordSearchRequest{
				Query: "test query",
				Limit: tt.requestLimit,
			}

			result, err := svc.Search(ctx, profileID, req)

			if tt.expect422 {
				require.Error(t, err)
				assert.True(t, IsValidationError(err), "expected validation error for limit 0")
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)

			// Verify limit_applied in meta
			assert.Equal(t, tt.expectedLimitCap, result.Meta.LimitApplied, "limit_applied should match expected cap")

			// Verify results are capped
			assert.LessOrEqual(t, len(result.Data), tt.expectedLimitCap, "result count should not exceed limit cap")
		})
	}
}

// TestKeywordSearchEmptyResult tests that empty result set returns 200 with {"data":[]}.
func TestKeywordSearchEmptyResult(t *testing.T) {
	profileID := "test-profile-id"

	tests := []struct {
		name            string
		fragmentResults []FragmentSearchResult
		factResults     []FactSearchResult
	}{
		{
			name:            "no results from both indexes",
			fragmentResults: []FragmentSearchResult{},
			factResults:     []FactSearchResult{},
		},
		{
			name: "results filtered out by profile mismatch",
			fragmentResults: []FragmentSearchResult{
				{FragmentID: "frag-1", Content: "content from other profile", Score: 0.9, ProfileID: "other-profile"},
			},
			factResults: []FactSearchResult{
				{FactID: "fact-1", Predicate: "fact from other profile", Score: 0.7, ProfileID: "other-profile"},
			},
		},
		{
			name: "results filtered out by labels mismatch",
			fragmentResults: []FragmentSearchResult{
				{FragmentID: "frag-1", Content: "content without matching labels", Score: 0.9, ProfileID: profileID, Labels: []string{"label-a"}},
			},
			factResults: []FactSearchResult{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			mockFragment := &mockFragmentSearcher{
				searchContentFunc: func(ctx context.Context, pid string, query string, labels []string, limit int) ([]FragmentSearchResult, error) {
					return tt.fragmentResults, nil
				},
			}

			mockFact := &mockFactSearcher{
				searchPredicateFunc: func(ctx context.Context, pid string, query string, labels []string, limit int) ([]FactSearchResult, error) {
					return tt.factResults, nil
				},
			}

			svc := NewKeywordSearchService(mockFragment, mockFact)

			req := &KeywordSearchRequest{
				Query:  "test query",
				Limit:  20,
				Labels: []string{"required-label"}, // For the labels mismatch test
			}

			result, err := svc.Search(ctx, profileID, req)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Verify empty data array
			assert.Empty(t, result.Data, "data should be empty array")
			assert.Equal(t, []SearchHit{}, result.Data, "data should be empty array, not nil")

			// Verify meta is set correctly
			assert.Equal(t, 20, result.Meta.LimitApplied, "limit_applied should be set")
		})
	}
}

// profileFilteringScopedReader is a test double for ScopedReaderInterface that returns
// rows keyed by profileID — simulating Neo4j's per-profile data isolation.
type profileFilteringScopedReader struct {
	rowsByProfile map[string][]map[string]any
}

func (r *profileFilteringScopedReader) ScopedRead(_ context.Context, profileID string, _ string, _ map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	return nil, r.rowsByProfile[profileID], nil
}

// TestSearchContent verifies that SearchContent returns the correct fragment_id
// (from f.fragment_id, not f.id) and enforces cross-profile isolation.
func TestSearchContent(t *testing.T) {
	t.Run("maps fragment_id from f.fragment_id property", func(t *testing.T) {
		reader := &fakeScopedReader{rows: []map[string]any{
			{"fragment_id": "frag-abc-123", "content": "hello world", "score": 0.91, "team_id": "p1"},
			{"fragment_id": "frag-def-456", "content": "second result", "score": 0.55, "team_id": "p1"},
		}}
		s := NewFragmentSearcher(reader)
		got, err := s.SearchContent(context.Background(), "p1", "hello", nil, 10)
		require.NoError(t, err)
		require.Len(t, got, 2)
		// AC-1: fragment_id must come from f.fragment_id, not f.id
		assert.Equal(t, "frag-abc-123", got[0].FragmentID)
		assert.Equal(t, "frag-def-456", got[1].FragmentID)
	})

	t.Run("cross profile isolation — profile B does not receive profile A data", func(t *testing.T) {
		profileA := "profile-a"
		profileB := "profile-b"

		reader := &profileFilteringScopedReader{
			rowsByProfile: map[string][]map[string]any{
				profileA: {
					{"fragment_id": "frag-a1", "content": "A content", "score": 0.9, "team_id": profileA},
					{"fragment_id": "frag-a2", "content": "A content 2", "score": 0.8, "team_id": profileA},
				},
				profileB: {
					{"fragment_id": "frag-b1", "content": "B content", "score": 0.7, "team_id": profileB},
				},
			},
		}

		s := NewFragmentSearcher(reader)

		// Profile B query must not return profile A fragment IDs
		bResults, err := s.SearchContent(context.Background(), profileB, "content", nil, 10)
		require.NoError(t, err)
		bIDs := make([]string, len(bResults))
		for i, r := range bResults {
			bIDs[i] = r.FragmentID
		}
		require.NotContains(t, bIDs, "frag-a1", "profile A frag-a1 must not appear in profile B results")
		require.NotContains(t, bIDs, "frag-a2", "profile A frag-a2 must not appear in profile B results")
		require.Contains(t, bIDs, "frag-b1", "profile B frag-b1 must be present in profile B results")

		// Profile A query must not return profile B fragment IDs
		aResults, err := s.SearchContent(context.Background(), profileA, "content", nil, 10)
		require.NoError(t, err)
		aIDs := make([]string, len(aResults))
		for i, r := range aResults {
			aIDs[i] = r.FragmentID
		}
		require.NotContains(t, aIDs, "frag-b1", "profile B frag-b1 must not appear in profile A results")
		require.Contains(t, aIDs, "frag-a1", "profile A frag-a1 must be present in profile A results")
	})
}

// TestSearchPredicate verifies that SearchPredicate reads fact_id from r.fact_id (node property)
// and enforces cross-profile isolation — covering AC-2.
func TestSearchPredicate(t *testing.T) {
	t.Run("maps fact_id from r.fact_id node property", func(t *testing.T) {
		reader := &fakeScopedReader{rows: []map[string]any{
			{"fact_id": "fact-abc-123", "predicate": "knows", "score": 0.91, "team_id": "p1"},
			{"fact_id": "fact-def-456", "predicate": "likes", "score": 0.55, "team_id": "p1"},
		}}
		s := NewFactSearcher(reader)
		got, err := s.SearchPredicate(context.Background(), "p1", "knows", nil, 10)
		require.NoError(t, err)
		require.Len(t, got, 2)
		// AC-2: fact_id must come from r.fact_id (Fact node property), not r.id
		assert.Equal(t, "fact-abc-123", got[0].FactID)
		assert.Equal(t, "fact-def-456", got[1].FactID)
	})

	t.Run("cross profile isolation — profile B does not receive profile A data", func(t *testing.T) {
		profileA := "profile-a"
		profileB := "profile-b"

		reader := &profileFilteringScopedReader{
			rowsByProfile: map[string][]map[string]any{
				profileA: {
					{"fact_id": "fact-a1", "predicate": "A knows B", "score": 0.9, "team_id": profileA},
					{"fact_id": "fact-a2", "predicate": "A likes C", "score": 0.8, "team_id": profileA},
				},
				profileB: {
					{"fact_id": "fact-b1", "predicate": "B knows A", "score": 0.7, "team_id": profileB},
				},
			},
		}

		s := NewFactSearcher(reader)

		// Profile B query must not return profile A fact IDs
		bResults, err := s.SearchPredicate(context.Background(), profileB, "knows", nil, 10)
		require.NoError(t, err)
		bIDs := make([]string, len(bResults))
		for i, r := range bResults {
			bIDs[i] = r.FactID
		}
		require.NotContains(t, bIDs, "fact-a1", "profile A fact-a1 must not appear in profile B results")
		require.NotContains(t, bIDs, "fact-a2", "profile A fact-a2 must not appear in profile B results")
		require.Contains(t, bIDs, "fact-b1", "profile B fact-b1 must be present in profile B results")

		// Profile A query must not return profile B fact IDs
		aResults, err := s.SearchPredicate(context.Background(), profileA, "knows", nil, 10)
		require.NoError(t, err)
		aIDs := make([]string, len(aResults))
		for i, r := range aResults {
			aIDs[i] = r.FactID
		}
		require.NotContains(t, aIDs, "fact-b1", "profile B fact-b1 must not appear in profile A results")
		require.Contains(t, aIDs, "fact-a1", "profile A fact-a1 must be present in profile A results")
	})
}

// capturingReader implements ScopedReaderInterface and records every call for inspection.
type capturingReader struct {
	capturedQuery  string
	capturedParams map[string]any
	rows           []map[string]any
}

func (c *capturingReader) ScopedRead(_ context.Context, _ string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	c.capturedQuery = query
	c.capturedParams = params
	return nil, c.rows, nil
}

// TestLabelFiltering verifies that label filter values are passed as Cypher parameters
// (not string-concatenated into the query), preventing Cypher injection (AC-6).
func TestLabelFiltering(t *testing.T) {
	t.Run("fragment searcher passes labels as parameter, not concatenated in query", func(t *testing.T) {
		labels := []string{"science", "'; DROP DATABASE neo4j; //"}

		reader := &capturingReader{rows: []map[string]any{
			{"fragment_id": "frag-1", "content": "hello", "score": 0.9, "team_id": "p1", "labels": []any{"science"}},
		}}
		s := NewFragmentSearcher(reader)

		got, err := s.SearchContent(context.Background(), "p1", "hello", labels, 10)
		require.NoError(t, err)
		require.NotEmpty(t, got)

		// The raw label values must NOT appear in the query string
		for _, label := range labels {
			assert.NotContains(t, reader.capturedQuery, label,
				"label value %q must not be string-interpolated into the Cypher query", label)
		}

		// The labels must be present as a parameter
		require.Contains(t, reader.capturedParams, "labels", "labels param must be set")
		passedLabels, ok := reader.capturedParams["labels"].([]string)
		require.True(t, ok, "labels param must be []string")
		assert.Equal(t, labels, passedLabels)

		// The parameterized predicate must appear in the query
		assert.Contains(t, reader.capturedQuery, "ANY(label IN $labels WHERE label IN f.labels)",
			"fragment query must use parameterized ANY() predicate")
	})

	t.Run("fact searcher passes labels as parameter, not concatenated in query", func(t *testing.T) {
		labels := []string{"history", "'; MATCH (n) DETACH DELETE n; //"}

		reader := &capturingReader{rows: []map[string]any{
			{"fact_id": "fact-1", "predicate": "knows", "score": 0.85, "team_id": "p1", "labels": []any{"history"}},
		}}
		s := NewFactSearcher(reader)

		got, err := s.SearchPredicate(context.Background(), "p1", "knows", labels, 10)
		require.NoError(t, err)
		require.NotEmpty(t, got)

		// The raw label values must NOT appear in the query string
		for _, label := range labels {
			assert.NotContains(t, reader.capturedQuery, label,
				"label value %q must not be string-interpolated into the Cypher query", label)
		}

		// The labels must be present as a parameter
		require.Contains(t, reader.capturedParams, "labels", "labels param must be set")
		passedLabels, ok := reader.capturedParams["labels"].([]string)
		require.True(t, ok, "labels param must be []string")
		assert.Equal(t, labels, passedLabels)

		// The parameterized predicate must appear in the query
		assert.Contains(t, reader.capturedQuery, "ANY(label IN $labels WHERE label IN r.labels)",
			"fact query must use parameterized ANY() predicate")
	})

	t.Run("no labels param set when labels filter is empty", func(t *testing.T) {
		reader := &capturingReader{rows: []map[string]any{}}
		s := NewFragmentSearcher(reader)

		_, err := s.SearchContent(context.Background(), "p1", "hello", nil, 10)
		require.NoError(t, err)

		// No labels param should be present
		assert.NotContains(t, reader.capturedParams, "labels",
			"labels param must not be set when no labels filter is requested")

		// No ANY() predicate should appear
		assert.NotContains(t, reader.capturedQuery, "ANY(label IN $labels",
			"query must not contain ANY() predicate when no labels filter is requested")
	})

	t.Run("cross-profile isolation still enforced with label filter", func(t *testing.T) {
		labels := []string{"science"}
		profileA := "profile-a"
		profileB := "profile-b"

		mockFragment := &mockFragmentSearcher{
			searchContentFunc: func(_ context.Context, pid string, _ string, _ []string, _ int) ([]FragmentSearchResult, error) {
				// Simulate Cypher returning mixed-profile rows (defense-in-depth scenario)
				return []FragmentSearchResult{
					{FragmentID: "frag-a1", Content: "A content", Score: 0.9, ProfileID: profileA, Labels: []string{"science"}},
					{FragmentID: "frag-b1", Content: "B content", Score: 0.8, ProfileID: profileB, Labels: []string{"science"}},
				}, nil
			},
		}
		mockFact := &mockFactSearcher{}

		svc := NewKeywordSearchService(mockFragment, mockFact)
		result, err := svc.Search(context.Background(), profileB, &KeywordSearchRequest{
			Query:  "content",
			Limit:  10,
			Labels: labels,
		})
		require.NoError(t, err)

		for _, hit := range result.Data {
			assert.Equal(t, profileB, hit.ProfileID,
				"label-filtered results must still be profile-isolated")
		}
		bIDs := make([]string, len(result.Data))
		for i, h := range result.Data {
			bIDs[i] = h.ID
		}
		assert.NotContains(t, bIDs, "frag-a1", "profile A data must not appear in profile B results")
	})
}

// TestFragmentSearcher_ScorePropagated tests that BM25 scores are propagated from the fulltext search.
func TestFragmentSearcher_ScorePropagated(t *testing.T) {
	reader := &fakeScopedReader{rows: []map[string]any{
		{"fragment_id": "f1", "content": "hello", "score": 0.87, "team_id": "p"},
		{"fragment_id": "f2", "content": "world", "score": 0.42, "team_id": "p"},
	}}
	s := NewFragmentSearcher(reader)
	got, err := s.SearchContent(context.Background(), "p", "hello", nil, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.InDelta(t, 0.87, got[0].Score, 1e-6)
	assert.InDelta(t, 0.42, got[1].Score, 1e-6)
	assert.NotEqual(t, 1.0, got[0].Score, "score must not be hardcoded")
}

// TestFactSearcher_ScorePropagated tests that BM25 scores are propagated from the fulltext search.
func TestFactSearcher_ScorePropagated(t *testing.T) {
	reader := &fakeScopedReader{rows: []map[string]any{
		{"fact_id": "fact-1", "predicate": "knows", "score": 0.33, "team_id": "p"},
		{"fact_id": "fact-2", "predicate": "likes", "score": 0.55, "team_id": "p"},
	}}
	s := NewFactSearcher(reader)
	got, err := s.SearchPredicate(context.Background(), "p", "knows", nil, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.InDelta(t, 0.33, got[0].Score, 1e-6)
	assert.InDelta(t, 0.55, got[1].Score, 1e-6)
	assert.NotEqual(t, 1.0, got[0].Score, "score must not be hardcoded")
}

func TestKeywordSearchDTOGettersAndValidationError(t *testing.T) {
	validAt := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	req := &KeywordSearchRequest{
		Query:   "memory",
		Limit:   7,
		Labels:  []string{"work"},
		ValidAt: &validAt,
	}
	require.Equal(t, "memory", req.GetQuery())
	require.Equal(t, 7, req.GetLimit())
	require.Equal(t, []string{"work"}, req.GetLabels())

	hit := SearchHit{
		ID:        "fact-1",
		Type:      "fact",
		Content:   "likes",
		Score:     0.9,
		Labels:    []string{"work"},
		Metadata:  map[string]any{"source": "test"},
		ProfileID: "profile-1",
	}
	require.Equal(t, "fact-1", hit.GetID())
	require.Equal(t, "fact", hit.GetType())
	require.Equal(t, "likes", hit.GetContent())
	require.Equal(t, 0.9, hit.GetScore())
	require.Equal(t, []string{"work"}, hit.GetLabels())
	require.Equal(t, map[string]any{"source": "test"}, hit.GetMetadata())
	require.Equal(t, "profile-1", hit.GetProfileID())

	result := &KeywordSearchResult{
		Data: []SearchHit{hit},
		Meta: KeywordSearchMeta{LimitApplied: 7},
	}
	require.Equal(t, []SearchHit{hit}, result.GetData())
	require.Equal(t, KeywordSearchMeta{LimitApplied: 7}, result.GetMeta())
	meta := result.GetMeta()
	require.Equal(t, 7, meta.GetLimitApplied())
	require.Equal(t, "bad request", NewValidationError("bad request").Error())
}

func TestKeywordSearchValidationAndDependencyErrors(t *testing.T) {
	t.Run("query is required", func(t *testing.T) {
		svc := NewKeywordSearchService(&mockFragmentSearcher{}, &mockFactSearcher{})

		_, err := svc.Search(context.Background(), "profile-1", &KeywordSearchRequest{Limit: 10})

		require.Error(t, err)
		require.True(t, IsValidationError(err))
	})

	t.Run("profile id is required", func(t *testing.T) {
		svc := NewKeywordSearchService(&mockFragmentSearcher{}, &mockFactSearcher{})

		_, err := svc.Search(context.Background(), "", &KeywordSearchRequest{Query: "memory", Limit: 10})

		require.Error(t, err)
		require.True(t, IsValidationError(err))
	})

	t.Run("fragment search error returns error", func(t *testing.T) {
		svc := NewKeywordSearchService(
			&mockFragmentSearcher{searchContentFunc: func(context.Context, string, string, []string, int) ([]FragmentSearchResult, error) {
				return nil, errors.New("fragment search failed")
			}},
			&mockFactSearcher{},
		)

		_, err := svc.Search(context.Background(), "profile-1", &KeywordSearchRequest{Query: "memory", Limit: 10})

		require.ErrorContains(t, err, "fragment search failed")
	})

	t.Run("fact search error returns error", func(t *testing.T) {
		svc := NewKeywordSearchService(
			&mockFragmentSearcher{},
			&mockFactSearcher{searchPredicateFunc: func(context.Context, string, string, []string, int) ([]FactSearchResult, error) {
				return nil, errors.New("fact search failed")
			}},
		)

		_, err := svc.Search(context.Background(), "profile-1", &KeywordSearchRequest{Query: "memory", Limit: 10})

		require.ErrorContains(t, err, "fact search failed")
	})
}

func TestKeywordFactResultMatchesWindow(t *testing.T) {
	base := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	before := base.Add(-time.Hour)
	after := base.Add(time.Hour)

	require.True(t, factResultMatchesWindow(FactSearchResult{RecordedAt: base}, nil, nil))
	require.False(t, factResultMatchesWindow(FactSearchResult{ValidFrom: &after, RecordedAt: base}, &base, nil))
	require.False(t, factResultMatchesWindow(FactSearchResult{ValidTo: &base, RecordedAt: base}, &base, nil))
	require.False(t, factResultMatchesWindow(FactSearchResult{RecordedAt: after}, nil, &base))
	require.False(t, factResultMatchesWindow(FactSearchResult{RecordedAt: before, RecordedTo: &base}, nil, &base))
	require.True(t, factResultMatchesWindow(FactSearchResult{ValidFrom: &before, ValidTo: &after, RecordedAt: before, RecordedTo: &after}, &base, &base))
}

func TestKeywordSearcherValueCoercionHelpers(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	row := map[string]any{
		"string":       "value",
		"number":       123,
		"labels_any":   []any{"a", 2},
		"labels_str":   []string{"x", "y"},
		"metadata":     map[string]any{"k": "v"},
		"score64":      float64(0.7),
		"score32":      float32(0.5),
		"recorded_at":  now,
		"recorded_nil": "not-time",
	}

	require.Equal(t, "value", getString(row, "string"))
	require.Equal(t, "123", getString(row, "number"))
	require.Empty(t, getString(row, "missing"))
	require.Equal(t, []string{"a", "2"}, getLabels(row, "labels_any"))
	require.Equal(t, []string{"x", "y"}, getLabels(row, "labels_str"))
	require.Nil(t, getLabels(row, "missing"))
	require.Equal(t, map[string]any{"k": "v"}, getMetadata(row, "metadata"))
	require.Nil(t, getMetadata(row, "string"))
	require.Equal(t, 0.7, getFloat64Val(row, "score64"))
	require.Equal(t, 0.5, getFloat64Val(row, "score32"))
	require.Zero(t, getFloat64Val(row, "string"))
	require.Equal(t, &now, getTimePtr(row, "recorded_at"))
	require.Nil(t, getTimePtr(row, "recorded_nil"))
	require.Equal(t, now, getTimeVal(row, "recorded_at"))
	require.True(t, getTimeVal(row, "recorded_nil").IsZero())
}
