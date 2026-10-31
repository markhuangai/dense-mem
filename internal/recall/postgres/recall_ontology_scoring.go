package postgres

import (
	"fmt"
	"math"
	"sort"

	"github.com/lib/pq"
)

func recallSourceFilter(column string, sourceIDs []string, args []any) (string, []any) {
	if sourceIDs == nil {
		return "", args
	}
	return " AND " + column + " = ANY(?::uuid[])", append(args, pq.Array(sourceIDs))
}

func augmentRecallVectorChannel(existing, ontology []SearchHit, limit int, score func([]string) ([]SearchHit, error)) ([]SearchHit, error) {
	if len(ontology) == 0 {
		return existing, nil
	}
	pooled := make([]SearchHit, 0, len(existing)+len(ontology))
	seen := make(map[string]bool, cap(pooled))
	for _, hit := range existing {
		if !seen[hit.SourceID] {
			seen[hit.SourceID] = true
			pooled = append(pooled, hit)
		}
	}
	ids := make([]string, 0, len(ontology))
	for _, hit := range ontology {
		if !seen[hit.SourceID] {
			seen[hit.SourceID] = true
			ids = append(ids, hit.SourceID)
		}
	}
	if len(ids) == 0 {
		return existing, nil
	}
	if len(pooled)+len(ids) > 2*limit {
		return nil, fmt.Errorf("recall: ontology scoring pool exceeds twice the candidate limit")
	}
	// Ordinary scores remain valid in the same repeatable-read transaction used for ontology augmentation.
	hits, err := score(ids)
	if err != nil {
		return nil, err
	}
	pooled = append(pooled, hits...)
	sort.Slice(pooled, func(i, j int) bool {
		left, right := pooled[i], pooled[j]
		if math.IsNaN(left.Distance) != math.IsNaN(right.Distance) {
			return !math.IsNaN(left.Distance)
		}
		if left.Distance != right.Distance && !math.IsNaN(left.Distance) {
			return left.Distance < right.Distance
		}
		return left.SearchDocumentID < right.SearchDocumentID
	})
	return pooled[:min(len(pooled), limit)], nil
}

func recallBoundedVectorQuery(query string) string {
	// Materialization keeps HNSW from approximating the explicitly bounded cosine scoring pool.
	return "WITH scored AS MATERIALIZED (" + query + ") SELECT * FROM scored ORDER BY distance ASC, search_document_id ASC LIMIT ?"
}

func recallEvidenceSourceJoin(bounded bool) string {
	if !bounded {
		return `JOIN evidence_fragments AS source_fragment
		  ON source_fragment.team_id = search_documents.team_id
		 AND source_fragment.fragment_id = search_documents.source_id`
	}
	// Keep bounded scoring on keyed source lookups even when table statistics underestimate the candidate pool.
	return `JOIN LATERAL (
		SELECT team_id, fragment_id, metadata FROM evidence_fragments
		WHERE team_id = search_documents.team_id AND fragment_id = search_documents.source_id
		LIMIT 1
	) AS source_fragment ON TRUE`
}
