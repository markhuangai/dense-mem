package semanticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockScopedReader implements ScopedReaderInterface for unit testing the searcher.
type mockScopedReader struct {
	scopedReadFunc func(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error)
}

func (m *mockScopedReader) ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	if m.scopedReadFunc != nil {
		return m.scopedReadFunc(ctx, profileID, query, params)
	}
	return nil, nil, nil
}

// TestQueryVectorIndex tests that QueryVectorIndex returns f.fragment_id as the hit ID.
// This is the red-test gate for Unit 11: the searcher must use fragment_id, not f.id.
func TestQueryVectorIndex(t *testing.T) {
	ctx := context.Background()
	profileID := "test-profile-id"
	fragID := "frag-uuid-1234"

	embedding := make([]float32, 3)
	for i := range embedding {
		embedding[i] = 0.1
	}

	mockReader := &mockScopedReader{
		scopedReadFunc: func(ctx context.Context, pid string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
			// Verify the profileID is passed through to enforce the team_id WHERE filter.
			require.Equal(t, profileID, pid, "ScopedRead must receive the requesting profileID")
			rows := []map[string]any{
				{
					"id":       fragID, // searcher aliases f.fragment_id AS id
					"content":  "test content",
					"score":    float64(0.9),
					"labels":   []any{},
					"metadata": map[string]any{},
					"team_id":  profileID,
				},
			}
			return nil, rows, nil
		},
	}

	searcher := NewEmbeddingSearcher(mockReader)
	hits, err := searcher.QueryVectorIndex(ctx, profileID, embedding, 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	// The hit ID must come from f.fragment_id, not f.id.
	require.Equal(t, fragID, hits[0].ID, "SearchHit.ID must equal the fragment_id property")
	require.Equal(t, profileID, hits[0].ProfileID)
}

// TestQueryVectorIndex_CrossProfileIsolation verifies that the searcher passes
// the requesting profileID to ScopedRead so the Cypher WHERE clause scopes
// results to the correct profile (profile A must not see profile B's data).
func TestQueryVectorIndex_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()
	profileA := "profile-a-id"
	profileB := "profile-b-id"

	embedding := make([]float32, 3)
	for i := range embedding {
		embedding[i] = 0.1
	}

	var capturedProfileID string
	mockReader := &mockScopedReader{
		scopedReadFunc: func(ctx context.Context, pid string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
			capturedProfileID = pid
			return nil, nil, nil
		},
	}

	searcher := NewEmbeddingSearcher(mockReader)

	_, err := searcher.QueryVectorIndex(ctx, profileA, embedding, 10)
	require.NoError(t, err)
	require.Equal(t, profileA, capturedProfileID, "ScopedRead must receive profile A's ID")

	_, err = searcher.QueryVectorIndex(ctx, profileB, embedding, 10)
	require.NoError(t, err)
	require.Equal(t, profileB, capturedProfileID, "ScopedRead must receive profile B's ID — not profile A's")
	require.NotEqual(t, profileA, capturedProfileID, "profile B query must not pass profile A's ID to ScopedRead")
}

func TestQueryVectorIndex_UsesNeo4jParameterOrder(t *testing.T) {
	ctx := context.Background()
	profileID := "profile-a-id"

	mockReader := &mockScopedReader{
		scopedReadFunc: func(ctx context.Context, pid string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
			assert.Contains(t, query, "queryNodes('fragment_embedding_idx', $limit, $embedding)")
			return nil, nil, nil
		},
	}

	searcher := NewEmbeddingSearcher(mockReader)
	_, err := searcher.QueryVectorIndex(ctx, profileID, []float32{0.1, 0.2, 0.3}, 10)
	require.NoError(t, err)
}

func TestQueryVectorIndex_OverfetchesBeforeTeamFilterAndCapsReturnedHits(t *testing.T) {
	ctx := context.Background()
	profileID := "profile-a-id"
	requestedLimit := 10
	rows := make([]map[string]any, 0, requestedLimit+2)
	for i := 0; i < requestedLimit+2; i++ {
		rows = append(rows, map[string]any{
			"id":       "frag-" + strconv.Itoa(i),
			"content":  "test content",
			"score":    float64(1),
			"labels":   []any{},
			"metadata": map[string]any{},
			"team_id":  profileID,
		})
	}

	var capturedLimit any
	mockReader := &mockScopedReader{
		scopedReadFunc: func(ctx context.Context, pid string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
			capturedLimit = params["limit"]
			return nil, rows, nil
		},
	}

	searcher := NewEmbeddingSearcher(mockReader)
	hits, err := searcher.QueryVectorIndex(ctx, profileID, []float32{0.1, 0.2, 0.3}, requestedLimit)

	require.NoError(t, err)
	require.Equal(t, requestedLimit*vectorIndexTeamFilterOverfetchMultiplier, capturedLimit)
	require.Len(t, hits, requestedLimit)
	require.Equal(t, "frag-0", hits[0].ID)
	require.Equal(t, "frag-9", hits[9].ID)
}

func TestVectorIndexQueryLimitCapsOverfetchButNotRequestedLimit(t *testing.T) {
	require.Equal(t, 0, vectorIndexQueryLimit(0))
	require.Equal(t, 200, vectorIndexQueryLimit(10))
	require.Equal(t, vectorIndexTeamFilterOverfetchMax, vectorIndexQueryLimit(100))
	require.Equal(t, 2000, vectorIndexQueryLimit(2000))
}

// mockEmbeddingSearcher implements EmbeddingSearcherInterface for testing.
type mockEmbeddingSearcher struct {
	queryVectorIndexFunc func(ctx context.Context, profileID string, embedding []float32, limit int) ([]SearchHit, error)
}

func (m *mockEmbeddingSearcher) QueryVectorIndex(ctx context.Context, profileID string, embedding []float32, limit int) ([]SearchHit, error) {
	if m.queryVectorIndexFunc != nil {
		return m.queryVectorIndexFunc(ctx, profileID, embedding, limit)
	}
	return []SearchHit{}, nil
}

// TestSemanticSearchProfileFiltering tests that profile B vector search excludes profile A vectors
// even when they are nearest globally.
// This verifies defense-in-depth profile filtering works at the Go post-filter level.
func TestSemanticSearchProfileFiltering(t *testing.T) {
	profileA := "profile-a-id"
	profileB := "profile-b-id"
	embeddingDimensions := 1536

	// Create a valid embedding for testing
	validEmbedding := make([]float32, embeddingDimensions)
	for i := range validEmbedding {
		validEmbedding[i] = 0.1
	}

	tests := []struct {
		name               string
		requestingProfile  string
		vectorResults      []SearchHit
		expectedProfileIDs []string // All should match requesting profile
	}{
		{
			name:              "profile B sees only profile B fragments even when profile A has nearest vector",
			requestingProfile: profileB,
			vectorResults: []SearchHit{
				{ID: "frag-1", Type: "fragment", Content: "nearest globally from profile A", Score: 0.99, ProfileID: profileA},
				{ID: "frag-2", Type: "fragment", Content: "content from profile B", Score: 0.80, ProfileID: profileB},
				{ID: "frag-3", Type: "fragment", Content: "second nearest from profile A", Score: 0.95, ProfileID: profileA},
			},
			expectedProfileIDs: []string{profileB}, // Only profile B results, even though profile A has higher scores
		},
		{
			name:              "profile A sees only profile A fragments even when profile B has nearest vector",
			requestingProfile: profileA,
			vectorResults: []SearchHit{
				{ID: "frag-1", Type: "fragment", Content: "nearest globally from profile B", Score: 0.99, ProfileID: profileB},
				{ID: "frag-2", Type: "fragment", Content: "content from profile A", Score: 0.80, ProfileID: profileA},
				{ID: "frag-3", Type: "fragment", Content: "second from profile A", Score: 0.75, ProfileID: profileA},
			},
			expectedProfileIDs: []string{profileA, profileA}, // Only profile A results
		},
		{
			name:              "all results from other profile - empty result",
			requestingProfile: profileB,
			vectorResults: []SearchHit{
				{ID: "frag-1", Type: "fragment", Content: "nearest from profile A", Score: 0.99, ProfileID: profileA},
				{ID: "frag-2", Type: "fragment", Content: "second from profile A", Score: 0.95, ProfileID: profileA},
			},
			expectedProfileIDs: []string{}, // No results for profile B
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			mockSearcher := &mockEmbeddingSearcher{
				queryVectorIndexFunc: func(ctx context.Context, pid string, emb []float32, limit int) ([]SearchHit, error) {
					// Return all results regardless of profile (simulating Cypher filter bypass attempt)
					return tt.vectorResults, nil
				},
			}

			svc := NewSemanticSearchService(mockSearcher, embeddingDimensions)

			req := &SemanticSearchRequest{
				Embedding: validEmbedding,
				Limit:     10,
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

// TestSemanticSearchBadDimensions tests that wrong embedding dimension returns validation error.
func TestSemanticSearchBadDimensions(t *testing.T) {
	profileID := "test-profile-id"
	embeddingDimensions := 1536

	mockSearcher := &mockEmbeddingSearcher{}
	svc := NewSemanticSearchService(mockSearcher, embeddingDimensions)

	tests := []struct {
		name         string
		embeddingLen int
		expectError  bool
	}{
		{
			name:         "correct dimensions",
			embeddingLen: 1536,
			expectError:  false,
		},
		{
			name:         "too few dimensions",
			embeddingLen: 512,
			expectError:  true,
		},
		{
			name:         "too many dimensions",
			embeddingLen: 2048,
			expectError:  true,
		},
		{
			name:         "empty embedding",
			embeddingLen: 0,
			expectError:  true, // This is the 501 case, not dimension mismatch
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			embedding := make([]float32, tt.embeddingLen)
			for i := range embedding {
				embedding[i] = 0.1
			}

			req := &SemanticSearchRequest{
				Embedding: embedding,
				Limit:     10,
			}

			result, err := svc.Search(ctx, profileID, req)

			if tt.expectError {
				require.Error(t, err)
				if tt.embeddingLen == 0 {
					// Empty embedding returns 501 error
					assert.True(t, IsEmbeddingGenerationNotConfiguredError(err), "expected embedding generation not configured error for empty embedding")
				} else {
					// Wrong dimensions returns dimension mismatch error
					assert.True(t, IsDimensionMismatchError(err), "expected dimension mismatch error")
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
			}
		})
	}
}

// TestSemanticSearchMissingEmbedding501 tests that absent embedding returns exactly 501.
func TestSemanticSearchMissingEmbedding501(t *testing.T) {
	profileID := "test-profile-id"
	embeddingDimensions := 1536

	mockSearcher := &mockEmbeddingSearcher{}
	svc := NewSemanticSearchService(mockSearcher, embeddingDimensions)

	ctx := context.Background()

	// Empty embedding (nil or zero-length)
	req := &SemanticSearchRequest{
		Embedding: []float32{}, // Empty embedding
		Limit:     10,
	}

	result, err := svc.Search(ctx, profileID, req)

	require.Error(t, err)
	require.Nil(t, result)

	// Verify it's exactly EmbeddingGenerationNotConfiguredError (501 case)
	assert.True(t, IsEmbeddingGenerationNotConfiguredError(err), "expected EmbeddingGenerationNotConfiguredError")
	assert.Contains(t, err.Error(), "embedding generation not configured")
}

// TestSemanticSearchLimitValidation tests limit validation and capping.
func TestSemanticSearchLimitValidation(t *testing.T) {
	profileID := "test-profile-id"
	embeddingDimensions := 1536

	validEmbedding := make([]float32, embeddingDimensions)
	for i := range validEmbedding {
		validEmbedding[i] = 0.1
	}

	tests := []struct {
		name             string
		requestLimit     int
		expectedLimitCap int
		expectError      bool
	}{
		{
			name:         "limit 0 returns 422 validation error",
			requestLimit: 0,
			expectError:  true,
		},
		{
			name:             "limit 10 is not capped",
			requestLimit:     10,
			expectedLimitCap: 10,
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
			name:             "default limit when not specified",
			requestLimit:     -1,           // Negative means use default
			expectedLimitCap: DefaultLimit, // 10
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			mockSearcher := &mockEmbeddingSearcher{
				queryVectorIndexFunc: func(ctx context.Context, pid string, emb []float32, limit int) ([]SearchHit, error) {
					// Return results with the requesting profile to pass post-filter
					results := make([]SearchHit, 200)
					for i := 0; i < 200; i++ {
						results[i] = SearchHit{
							ID:        "frag-" + string(rune(i)),
							Type:      "fragment",
							Content:   "content",
							Score:     float64(200-i) / 200.0, // Descending scores
							ProfileID: pid,
						}
					}
					return results, nil
				},
			}

			svc := NewSemanticSearchService(mockSearcher, embeddingDimensions)

			req := &SemanticSearchRequest{
				Embedding: validEmbedding,
				Limit:     tt.requestLimit,
			}

			result, err := svc.Search(ctx, profileID, req)

			if tt.expectError {
				require.Error(t, err)
				assert.True(t, IsValidationError(err), "expected validation error")
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

// TestSemanticSearchEmptyResult tests that empty result set returns 200 with {"data":[]}.
func TestSemanticSearchEmptyResult(t *testing.T) {
	profileID := "test-profile-id"
	embeddingDimensions := 1536

	validEmbedding := make([]float32, embeddingDimensions)
	for i := range validEmbedding {
		validEmbedding[i] = 0.1
	}

	tests := []struct {
		name          string
		vectorResults []SearchHit
	}{
		{
			name:          "no results from vector index",
			vectorResults: []SearchHit{},
		},
		{
			name: "results filtered out by profile mismatch",
			vectorResults: []SearchHit{
				{ID: "frag-1", Type: "fragment", Content: "content from other profile", Score: 0.9, ProfileID: "other-profile"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			mockSearcher := &mockEmbeddingSearcher{
				queryVectorIndexFunc: func(ctx context.Context, pid string, emb []float32, limit int) ([]SearchHit, error) {
					return tt.vectorResults, nil
				},
			}

			svc := NewSemanticSearchService(mockSearcher, embeddingDimensions)

			req := &SemanticSearchRequest{
				Embedding: validEmbedding,
				Limit:     20,
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

func TestSemanticSearchThresholdFiltersHits(t *testing.T) {
	profileID := "test-profile-id"
	embeddingDimensions := 3

	mockSearcher := &mockEmbeddingSearcher{
		queryVectorIndexFunc: func(ctx context.Context, pid string, embedding []float32, limit int) ([]SearchHit, error) {
			return []SearchHit{
				{ID: "frag-1", Type: "fragment", Content: "kept", Score: 0.91, ProfileID: pid},
				{ID: "frag-2", Type: "fragment", Content: "dropped", Score: 0.74, ProfileID: pid},
			}, nil
		},
	}

	svc := NewSemanticSearchService(mockSearcher, embeddingDimensions)

	result, err := svc.Search(context.Background(), profileID, &SemanticSearchRequest{
		Embedding: []float32{0.1, 0.2, 0.3},
		Limit:     10,
		Threshold: 0.8,
	})
	require.NoError(t, err)
	require.Len(t, result.Data, 1)
	assert.Equal(t, "frag-1", result.Data[0].ID)
}

func TestSemanticSearchDTOGettersAndErrors(t *testing.T) {
	req := &SemanticSearchRequest{
		Embedding: []float32{0.1, 0.2, 0.3},
		Query:     "memory",
		Limit:     7,
		Threshold: 0.8,
	}
	require.Equal(t, []float32{0.1, 0.2, 0.3}, req.GetEmbedding())
	require.Equal(t, "memory", req.GetQuery())
	require.Equal(t, 7, req.GetLimit())
	require.Equal(t, 0.8, req.GetThreshold())

	hit := SearchHit{
		ID:        "fragment-1",
		Type:      "fragment",
		Content:   "content",
		Score:     0.9,
		Labels:    []string{"work"},
		Metadata:  map[string]any{"source": "test"},
		ProfileID: "profile-1",
	}
	require.Equal(t, "fragment-1", hit.GetID())
	require.Equal(t, "fragment", hit.GetType())
	require.Equal(t, "content", hit.GetContent())
	require.Equal(t, 0.9, hit.GetScore())
	require.Equal(t, []string{"work"}, hit.GetLabels())
	require.Equal(t, map[string]any{"source": "test"}, hit.GetMetadata())
	require.Equal(t, "profile-1", hit.GetProfileID())

	result := &SemanticSearchResult{
		Data: []SearchHit{hit},
		Meta: SemanticSearchMeta{LimitApplied: 7},
	}
	require.Equal(t, []SearchHit{hit}, result.GetData())
	require.Equal(t, SemanticSearchMeta{LimitApplied: 7}, result.GetMeta())
	meta := result.GetMeta()
	require.Equal(t, 7, meta.GetLimitApplied())
	require.Equal(t, "bad request", NewValidationError("bad request").Error())
	require.Equal(t, "embedding dimension mismatch: expected 3, got 2", NewDimensionMismatchError(3, 2).Error())
}

func TestSearchHitOmitsEmptyTimestamps(t *testing.T) {
	raw, err := json.Marshal(SearchHit{ID: "fragment-1", Type: "fragment"})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "created_at")
	require.NotContains(t, string(raw), "updated_at")

	createdAt := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	raw, err = json.Marshal(SearchHit{ID: "fragment-1", Type: "fragment", CreatedAt: &createdAt})
	require.NoError(t, err)
	require.Contains(t, string(raw), "created_at")
}

func TestSemanticSearchValidationThresholdAndDependencyErrors(t *testing.T) {
	validEmbedding := []float32{0.1, 0.2, 0.3}

	t.Run("profile id is required", func(t *testing.T) {
		svc := NewSemanticSearchService(&mockEmbeddingSearcher{}, 3)

		_, err := svc.Search(context.Background(), "", &SemanticSearchRequest{Embedding: validEmbedding, Limit: 10})

		require.Error(t, err)
		require.True(t, IsValidationError(err))
	})

	t.Run("threshold below range", func(t *testing.T) {
		svc := NewSemanticSearchService(&mockEmbeddingSearcher{}, 3)

		_, err := svc.Search(context.Background(), "profile-1", &SemanticSearchRequest{Embedding: validEmbedding, Limit: 10, Threshold: -0.1})

		require.Error(t, err)
		require.True(t, IsValidationError(err))
	})

	t.Run("threshold above range", func(t *testing.T) {
		svc := NewSemanticSearchService(&mockEmbeddingSearcher{}, 3)

		_, err := svc.Search(context.Background(), "profile-1", &SemanticSearchRequest{Embedding: validEmbedding, Limit: 10, Threshold: 1.1})

		require.Error(t, err)
		require.True(t, IsValidationError(err))
	})

	t.Run("searcher error returns error", func(t *testing.T) {
		svc := NewSemanticSearchService(
			&mockEmbeddingSearcher{queryVectorIndexFunc: func(context.Context, string, []float32, int) ([]SearchHit, error) {
				return nil, errors.New("vector search failed")
			}},
			3,
		)

		_, err := svc.Search(context.Background(), "profile-1", &SemanticSearchRequest{Embedding: validEmbedding, Limit: 10})

		require.ErrorContains(t, err, "vector search failed")
	})
}

func TestSemanticSearcherValueCoercionHelpers(t *testing.T) {
	row := map[string]any{
		"string":        "value",
		"number":        123,
		"score64":       float64(0.7),
		"score32":       float32(0.5),
		"labels_any":    []any{"a", 2},
		"labels_str":    []string{"x", "y"},
		"metadata":      map[string]any{"k": "v"},
		"metadata_json": `{"j":"w"}`,
	}

	require.Equal(t, "value", getStringVal(row, "string"))
	require.Equal(t, "123", getStringVal(row, "number"))
	require.Empty(t, getStringVal(row, "missing"))
	require.Equal(t, 0.7, getFloat64Val(row, "score64"))
	require.Equal(t, 0.5, getFloat64Val(row, "score32"))
	require.Zero(t, getFloat64Val(row, "string"))
	require.Equal(t, []string{"a", "2"}, getLabelsVal(row, "labels_any"))
	require.Equal(t, []string{"x", "y"}, getLabelsVal(row, "labels_str"))
	require.Nil(t, getLabelsVal(row, "missing"))
	require.Equal(t, map[string]any{"k": "v"}, getMetadataVal(row, "metadata"))
	require.Equal(t, map[string]any{"j": "w"}, getMetadataVal(row, "missing", "metadata_json"))
	require.Nil(t, getMetadataVal(row, "string"))
}
