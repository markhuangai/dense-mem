package semanticsearch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
	neo4jstore "github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

// ScopedReaderInterface is the interface for scoped read operations.
// This matches neo4j.ProfileScopeEnforcer's ScopedRead method.
type ScopedReaderInterface interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error)
}

// neo4jEmbeddingSearcher implements EmbeddingSearcherInterface using Neo4j.
type neo4jEmbeddingSearcher struct {
	reader ScopedReaderInterface
}

const (
	vectorIndexTeamFilterOverfetchMultiplier = 20
	vectorIndexTeamFilterOverfetchMax        = 1000
)

// Ensure neo4jEmbeddingSearcher implements EmbeddingSearcherInterface.
var _ EmbeddingSearcherInterface = (*neo4jEmbeddingSearcher)(nil)

// NewEmbeddingSearcher creates a new EmbeddingSearcherInterface using Neo4j.
func NewEmbeddingSearcher(reader ScopedReaderInterface) EmbeddingSearcherInterface {
	return &neo4jEmbeddingSearcher{reader: reader}
}

// QueryVectorIndex performs vector similarity search on SourceFragment embeddings.
// Results are filtered by team_id and retract status in the Cypher query.
func (s *neo4jEmbeddingSearcher) QueryVectorIndex(ctx context.Context, profileID string, embedding []float32, limit int) ([]SearchHit, error) {
	queryLimit := vectorIndexQueryLimit(limit)

	// Adapt FragmentActiveFilter (which uses the sf. node alias) to the f. alias used here.
	// This excludes retracted SourceFragment nodes; legacy nodes without a status property
	// are treated as active per the coalesce default (AC-44).
	fragmentActive := strings.ReplaceAll(neo4jstore.FragmentActiveFilter, "sf.", "f.")

	// Build the Cypher query with vector index search.
	// Uses db.index.vector.queryNodes for vector similarity search.
	cypherQuery := `CALL db.index.vector.queryNodes('fragment_embedding_idx', $limit, $embedding) YIELD node AS f, score
WHERE f.team_id = $profileId AND ` + fragmentActive + `
	RETURN f.fragment_id AS id, f.content AS content, score, f.labels AS labels, f.metadata AS metadata,
	       f.metadata_json AS metadata_json, f.team_id AS team_id,
	       f.created_at AS created_at, f.updated_at AS updated_at`

	// Build params - convert float32 slice to any slice for Neo4j
	embeddingAny := make([]any, len(embedding))
	for i, v := range embedding {
		embeddingAny[i] = v
	}

	params := map[string]any{
		"embedding": embeddingAny,
		"limit":     queryLimit,
	}

	// Execute via ScopedRead
	_, results, err := s.reader.ScopedRead(ctx, profileID, cypherQuery, params)
	if err != nil {
		return nil, fmt.Errorf("failed to query vector index: %w", err)
	}
	if limit >= 0 && len(results) > limit {
		results = results[:limit]
	}

	// Convert results to SearchHit
	hits := make([]SearchHit, len(results))
	for i, row := range results {
		hits[i] = SearchHit{
			ID:        getStringVal(row, "id"),
			Type:      "fragment",
			Content:   getStringVal(row, "content"),
			Score:     getFloat64Val(row, "score"),
			Labels:    getLabelsVal(row, "labels"),
			Metadata:  getMetadataVal(row, "metadata", "metadata_json"),
			ProfileID: getStringVal(row, "team_id"),
			CreatedAt: timePtrIfNonZero(getTimeVal(row, "created_at")),
			UpdatedAt: timePtrIfNonZero(getTimeVal(row, "updated_at")),
		}
	}

	return hits, nil
}

func timePtrIfNonZero(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func vectorIndexQueryLimit(limit int) int {
	if limit <= 0 {
		return limit
	}
	queryLimit := limit * vectorIndexTeamFilterOverfetchMultiplier
	if queryLimit < limit || queryLimit > vectorIndexTeamFilterOverfetchMax {
		queryLimit = vectorIndexTeamFilterOverfetchMax
	}
	if queryLimit < limit {
		queryLimit = limit
	}
	return queryLimit
}

// Helper functions for extracting values from Neo4j result maps
func getStringVal(row map[string]any, key string) string {
	if val, ok := row[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
		return fmt.Sprintf("%v", val)
	}
	return ""
}

func getFloat64Val(row map[string]any, key string) float64 {
	if val, ok := row[key]; ok {
		if f, ok := val.(float64); ok {
			return f
		}
		if f, ok := val.(float32); ok {
			return float64(f)
		}
	}
	return 0.0
}

func getTimeVal(row map[string]any, key string) time.Time {
	if val, ok := row[key].(time.Time); ok {
		return val
	}
	return time.Time{}
}

func getLabelsVal(row map[string]any, key string) []string {
	if val, ok := row[key]; ok {
		if arr, ok := val.([]any); ok {
			labels := make([]string, len(arr))
			for i, v := range arr {
				labels[i] = fmt.Sprintf("%v", v)
			}
			return labels
		}
		if arr, ok := val.([]string); ok {
			return arr
		}
	}
	return nil
}

func getMetadataVal(row map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if value := fragmentcodec.DecodeOptionalMap(row[key]); value != nil {
			return value
		}
	}
	return nil
}
