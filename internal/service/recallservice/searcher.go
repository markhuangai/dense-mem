package recallservice

import (
	"context"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/fulltextquery"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// RecallScopedReader is the minimal profile-scoped read interface required by
// the query-based fact/claim recall searchers.
type RecallScopedReader interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error)
}

type neo4jFactSearcher struct {
	reader RecallScopedReader
}

type neo4jClaimSearcher struct {
	reader RecallScopedReader
}

// NewFactSearcher builds the tier-1 recall searcher over the dedicated
// full-text index on Fact subject/predicate/object.
func NewFactSearcher(reader RecallScopedReader) FactSearcher {
	return &neo4jFactSearcher{reader: reader}
}

// NewClaimSearcher builds the tier-1.5 recall searcher over the dedicated
// full-text index on Claim subject/predicate/object.
func NewClaimSearcher(reader RecallScopedReader) ClaimSearcher {
	return &neo4jClaimSearcher{reader: reader}
}

func (s *neo4jFactSearcher) SearchActive(ctx context.Context, profileID string, query string, limit int) ([]FactRecallResult, error) {
	searchQuery := fulltextquery.PlainText(query)
	if searchQuery == "" {
		return []FactRecallResult{}, nil
	}

	cypher := `
CALL db.index.fulltext.queryNodes('fact_recall_idx', $searchQuery) YIELD node AS f, score
WHERE f.team_id = $profileId AND f.status = 'active'
OPTIONAL MATCH (incomingOverlay:Fact {team_id: $profileId})-[:OVERLAYS {team_id: $profileId}]->(f)
WITH f, score, count(incomingOverlay) AS incoming_overlay_count
OPTIONAL MATCH (f)-[:OVERLAYS {team_id: $profileId}]->(outgoingOverlay:Fact {team_id: $profileId})
WITH f, score, incoming_overlay_count, count(outgoingOverlay) AS outgoing_overlay_count
RETURN
    f.fact_id AS fact_id,
    f.team_id AS team_id,
    f.valid_from AS valid_from,
    f.valid_to AS valid_to,
    f.recorded_at AS recorded_at,
    f.recorded_to AS recorded_to,
    CASE
      WHEN outgoing_overlay_count > 0 THEN 'overlay'
      WHEN incoming_overlay_count > 0 THEN 'conflicted'
      ELSE 'authoritative'
    END AS authority_state,
    score
LIMIT $limit`

	_, rows, err := s.reader.ScopedRead(ctx, profileID, cypher, map[string]any{
		"searchQuery": searchQuery,
		"limit":       limit,
	})
	if err != nil {
		return nil, fmt.Errorf("recall fact search: %w", err)
	}

	results := make([]FactRecallResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, FactRecallResult{
			FactID:         recallString(row, "fact_id"),
			ProfileID:      recallString(row, "team_id"),
			Score:          recallFloat64(row, "score"),
			ValidFrom:      recallTimePtr(row, "valid_from"),
			ValidTo:        recallTimePtr(row, "valid_to"),
			RecordedAt:     recallTime(row, "recorded_at"),
			RecordedTo:     recallTimePtr(row, "recorded_to"),
			AuthorityState: recallString(row, "authority_state"),
		})
	}

	return results, nil
}

func (s *neo4jClaimSearcher) SearchValidated(ctx context.Context, profileID string, query string, limit int) ([]ClaimRecallResult, error) {
	searchQuery := fulltextquery.PlainText(query)
	if searchQuery == "" {
		return []ClaimRecallResult{}, nil
	}

	cypher := `
CALL db.index.fulltext.queryNodes('claim_recall_idx', $searchQuery) YIELD node AS c, score
WHERE c.team_id = $profileId AND c.status = 'validated'
RETURN
    c.claim_id AS claim_id,
    c.team_id AS team_id,
    c.valid_from AS valid_from,
    c.valid_to AS valid_to,
    c.recorded_at AS recorded_at,
    c.recorded_to AS recorded_to,
    score
LIMIT $limit`

	_, rows, err := s.reader.ScopedRead(ctx, profileID, cypher, map[string]any{
		"searchQuery": searchQuery,
		"limit":       limit,
	})
	if err != nil {
		return nil, fmt.Errorf("recall claim search: %w", err)
	}

	results := make([]ClaimRecallResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, ClaimRecallResult{
			ClaimID:    recallString(row, "claim_id"),
			ProfileID:  recallString(row, "team_id"),
			Score:      recallFloat64(row, "score"),
			ValidFrom:  recallTimePtr(row, "valid_from"),
			ValidTo:    recallTimePtr(row, "valid_to"),
			RecordedAt: recallTime(row, "recorded_at"),
			RecordedTo: recallTimePtr(row, "recorded_to"),
		})
	}

	return results, nil
}

func recallString(row map[string]any, key string) string {
	if v, ok := row[key].(string); ok {
		return v
	}
	return ""
}

func recallFloat64(row map[string]any, key string) float64 {
	switch v := row[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	default:
		return 0
	}
}

func recallTimePtr(row map[string]any, key string) *time.Time {
	if v, ok := row[key].(time.Time); ok {
		return &v
	}
	return nil
}

func recallTime(row map[string]any, key string) time.Time {
	if v, ok := row[key].(time.Time); ok {
		return v
	}
	return time.Time{}
}
