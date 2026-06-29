package keywordsearch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/markhuangai/dense-mem/internal/fulltextquery"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
	neo4jstore "github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

// neo4jFragmentSearcher implements FragmentSearcherInterface using Neo4j.
type neo4jFragmentSearcher struct {
	reader ScopedReaderInterface
}

// ScopedReaderInterface is the interface for scoped read operations.
// This matches neo4j.ProfileScopeEnforcer's ScopedRead method.
type ScopedReaderInterface interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error)
}

// Ensure neo4jFragmentSearcher implements FragmentSearcherInterface.
var _ FragmentSearcherInterface = (*neo4jFragmentSearcher)(nil)

// NewFragmentSearcher creates a new FragmentSearcherInterface using Neo4j.
func NewFragmentSearcher(reader ScopedReaderInterface) FragmentSearcherInterface {
	return &neo4jFragmentSearcher{reader: reader}
}

// SearchContent performs full-text search on SourceFragment content.
// Results are filtered by team_id and retract status in the Cypher query.
func (s *neo4jFragmentSearcher) SearchContent(ctx context.Context, profileID string, query string, labels []string, limit int) ([]FragmentSearchResult, error) {
	searchQuery := fulltextquery.PlainText(query)
	if searchQuery == "" {
		return []FragmentSearchResult{}, nil
	}

	// Adapt FragmentActiveFilter (which uses the sf. node alias) to the f. alias used here.
	// This excludes retracted SourceFragment nodes; legacy nodes without a status property
	// are treated as active per the coalesce default (AC-44).
	fragmentActive := strings.ReplaceAll(neo4jstore.FragmentActiveFilter, "sf.", "f.")

	// Build the base WHERE clause: profile isolation + retract filter.
	baseWhere := "f.team_id = $profileId AND " + fragmentActive

	// Optionally extend with label filter — appended as a separate AND condition so
	// the label values remain a Cypher parameter (prevents injection, AC-6).
	whereClause := baseWhere
	if len(labels) > 0 {
		whereClause += " AND ANY(label IN $labels WHERE label IN f.labels)"
	}

	// Build the Cypher query with full-text index search.
	// Uses db.index.fulltext.queryNodes for content search.
	cypherQuery := `CALL db.index.fulltext.queryNodes('fragment_content_idx', $searchQuery) YIELD node AS f, score
	WHERE ` + whereClause + `
	RETURN f.fragment_id AS fragment_id, f.content AS content, f.labels AS labels, f.metadata AS metadata,
	       f.metadata_json AS metadata_json, f.team_id AS team_id,
	       f.created_at AS created_at, f.updated_at AS updated_at, score
	LIMIT $limit`

	// Build params
	params := map[string]any{
		"searchQuery": searchQuery,
		"limit":       limit,
	}

	// Add label filter param if specified.
	if len(labels) > 0 {
		params["labels"] = labels
	}

	// Execute via ScopedRead
	_, results, err := s.reader.ScopedRead(ctx, profileID, cypherQuery, params)
	if err != nil {
		return nil, fmt.Errorf("failed to search fragments: %w", err)
	}

	// Convert results to FragmentSearchResult
	searchResults := make([]FragmentSearchResult, len(results))
	for i, row := range results {
		searchResults[i] = FragmentSearchResult{
			FragmentID: getString(row, "fragment_id"),
			Content:    getString(row, "content"),
			Score:      getFloat64Val(row, "score"),
			Labels:     getLabels(row, "labels"),
			Metadata:   getMetadata(row, "metadata", "metadata_json"),
			ProfileID:  getString(row, "team_id"),
			CreatedAt:  getTimeVal(row, "created_at"),
			UpdatedAt:  getTimeVal(row, "updated_at"),
		}
	}

	return searchResults, nil
}

// neo4jFactSearcher implements FactSearcherInterface using Neo4j.
type neo4jFactSearcher struct {
	reader ScopedReaderInterface
}

// Ensure neo4jFactSearcher implements FactSearcherInterface.
var _ FactSearcherInterface = (*neo4jFactSearcher)(nil)

// NewFactSearcher creates a new FactSearcherInterface using Neo4j.
func NewFactSearcher(reader ScopedReaderInterface) FactSearcherInterface {
	return &neo4jFactSearcher{reader: reader}
}

// SearchPredicate performs full-text search on Fact predicates.
// Results are filtered by team_id in the Cypher query.
func (s *neo4jFactSearcher) SearchPredicate(ctx context.Context, profileID string, query string, labels []string, limit int) ([]FactSearchResult, error) {
	searchQuery := fulltextquery.PlainText(query)
	if searchQuery == "" {
		return []FactSearchResult{}, nil
	}

	// Build the Cypher query with full-text index search
	// Uses db.index.fulltext.queryNodes for predicate search — fact_predicate_idx is a node index on Fact.predicate
	cypherQuery := `
		CALL db.index.fulltext.queryNodes('fact_predicate_idx', $searchQuery) YIELD node AS r, score
		WHERE r.team_id = $profileId
		RETURN r.fact_id AS fact_id, r.predicate AS predicate, r.labels AS labels, r.metadata AS metadata, r.team_id AS team_id,
		       r.valid_from AS valid_from, r.valid_to AS valid_to, r.recorded_at AS recorded_at, r.recorded_to AS recorded_to, score
		LIMIT $limit
	`

	// Build params
	params := map[string]any{
		"searchQuery": searchQuery,
		"limit":       limit,
	}

	// Add label filter if specified — values are passed as a parameter to prevent Cypher injection
	if len(labels) > 0 {
		cypherQuery = strings.Replace(cypherQuery, "WHERE r.team_id = $profileId",
			"WHERE r.team_id = $profileId AND ANY(label IN $labels WHERE label IN r.labels)", 1)
		params["labels"] = labels
	}

	// Execute via ScopedRead
	_, results, err := s.reader.ScopedRead(ctx, profileID, cypherQuery, params)
	if err != nil {
		return nil, fmt.Errorf("failed to search facts: %w", err)
	}

	// Convert results to FactSearchResult
	searchResults := make([]FactSearchResult, len(results))
	for i, row := range results {
		searchResults[i] = FactSearchResult{
			FactID:     getString(row, "fact_id"),
			Predicate:  getString(row, "predicate"),
			Score:      getFloat64Val(row, "score"),
			Labels:     getLabels(row, "labels"),
			Metadata:   getMetadata(row, "metadata"),
			ProfileID:  getString(row, "team_id"),
			ValidFrom:  getTimePtr(row, "valid_from"),
			ValidTo:    getTimePtr(row, "valid_to"),
			RecordedAt: getTimeVal(row, "recorded_at"),
			RecordedTo: getTimePtr(row, "recorded_to"),
		}
	}

	return searchResults, nil
}

// Helper functions for extracting values from Neo4j result maps
func getString(row map[string]any, key string) string {
	if val, ok := row[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
		return fmt.Sprintf("%v", val)
	}
	return ""
}

func getLabels(row map[string]any, key string) []string {
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

func getMetadata(row map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if value := fragmentcodec.DecodeOptionalMap(row[key]); value != nil {
			return value
		}
	}
	return nil
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

func getTimePtr(row map[string]any, key string) *time.Time {
	if val, ok := row[key].(time.Time); ok {
		return &val
	}
	return nil
}

func getTimeVal(row map[string]any, key string) time.Time {
	if val, ok := row[key].(time.Time); ok {
		return val
	}
	return time.Time{}
}
