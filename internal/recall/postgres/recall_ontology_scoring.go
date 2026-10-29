package postgres

import (
	"fmt"

	"github.com/lib/pq"
)

func recallSourceFilter(column string, sourceIDs []string, args []any) (string, []any) {
	if sourceIDs == nil {
		return "", args
	}
	return " AND " + column + " = ANY(?::uuid[])", append(args, pq.Array(sourceIDs))
}

func augmentRecallChannel(existing, ontology []SearchHit, limit int, score func([]string) ([]SearchHit, error)) ([]SearchHit, error) {
	if len(ontology) == 0 {
		return existing, nil
	}
	ids := make([]string, 0, len(existing)+len(ontology))
	seen := make(map[string]bool, cap(ids))
	for _, hit := range existing {
		if !seen[hit.SourceID] {
			seen[hit.SourceID] = true
			ids = append(ids, hit.SourceID)
		}
	}
	ordinaryCount := len(ids)
	for _, hit := range ontology {
		if !seen[hit.SourceID] {
			seen[hit.SourceID] = true
			ids = append(ids, hit.SourceID)
		}
	}
	if len(ids) == ordinaryCount {
		return existing, nil
	}
	if len(ids) > 2*limit {
		return nil, fmt.Errorf("recall: ontology scoring pool exceeds twice the candidate limit")
	}
	return score(ids)
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
