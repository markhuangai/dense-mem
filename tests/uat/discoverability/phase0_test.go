//go:build uat

package discoverability

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
)

// Phase-0 UATs validate the primitive fixes that unblock the later phases.
// They run under -tags uat like the rest of the UAT suite but do not require
// live containers — each AC is pinned to a static source artefact produced by
// the owning unit. Deeper end-to-end coverage lives in tests/uat/ (the full
// harness) and in the owning unit's unit tests.

// UAT-1: Neo4j schema canonicalization (Unit 2). AC-1, AC-2, AC-3, AC-8, AC-9, AC-10.
func TestUAT1_SchemaBootstrap(t *testing.T) {
	body, err := os.ReadFile(repoPath(t, "internal/storage/neo4j/schema.go"))
	require.NoError(t, err, "schema.go must exist — Unit 2 deliverable")
	content := string(body)

	// The canonical index names adopted by Unit 2 must be declared and must be
	// the same names that searcher.go queries against. Otherwise Phase-0
	// primitives are broken and every downstream phase fails invisibly.
	for _, name := range []string{"fragment_content_idx", "fact_predicate_idx"} {
		assert.Contains(t, content, name, "schema.go must declare %q", name)
	}
}

// UAT-2: BM25 score propagation (Unit 3). AC-4, AC-5.
func TestUAT2_BM25ScorePropagation(t *testing.T) {
	body, err := os.ReadFile(repoPath(t, "internal/tools/keywordsearch/searcher.go"))
	require.NoError(t, err, "searcher.go must exist — Unit 3 deliverable")
	content := string(body)

	// searcher.go must YIELD both the node binding and the score so the
	// service layer can map BM25 into the hit struct instead of hardcoding 1.0.
	assert.Contains(t, content, "YIELD node AS f, score",
		"fragment search YIELD must surface score, not a hardcoded 1.0")
	assert.Contains(t, content, `Score:      getFloat64Val(row, "score")`,
		"fragment result must map score into the Score field")
	assert.NotContains(t, content, "Score: 1.0",
		"hardcoded Score: 1.0 must be removed (Unit 3 fix)")

	// Sanity: the public result types carry a Score field so handlers can
	// propagate the BM25 score through the service layer.
	var fragHit keywordsearch.FragmentSearchResult
	fragHit.Score = 0.42
	assert.Equal(t, float64(0.42), fragHit.Score,
		"FragmentSearchResult must carry a Score field")

	var factHit keywordsearch.FactSearchResult
	factHit.Score = 0.73
	assert.Equal(t, float64(0.73), factHit.Score,
		"FactSearchResult must carry a Score field")
}

// UAT-3: Direct keyword search HTTP handlers are not part of the client surface.
func TestUAT3_KeywordSearchHTTPHandlerRemoved(t *testing.T) {
	_, err := os.Stat(repoPath(t, "internal/http/handler/keyword_search_handler.go"))
	require.ErrorIs(t, err, os.ErrNotExist,
		"keyword_search must remain an internal service, not a public HTTP handler")
}

// UAT-4: Direct graph query HTTP handlers are not part of the client surface.
func TestUAT4_GraphQueryHTTPHandlerRemoved(t *testing.T) {
	_, err := os.Stat(repoPath(t, "internal/http/handler/graph_query_handler.go"))
	require.ErrorIs(t, err, os.ErrNotExist,
		"graph_query must remain an internal/eval service, not a public HTTP handler")
}
