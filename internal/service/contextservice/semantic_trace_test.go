package contextservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/assertionservice"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

type contextAssertionStore struct {
	assertion *domain.Assertion
	err       error
}

func (s *contextAssertionStore) WriteBundle(context.Context, string, assertionservice.Bundle) (assertionservice.WriteResult, error) {
	return assertionservice.WriteResult{}, s.err
}

func (s *contextAssertionStore) GetAssertion(context.Context, string, string) (*domain.Assertion, error) {
	return s.assertion, s.err
}

type semanticTraceReader struct {
	rowSets  [][]map[string]any
	errs     []error
	queries  []string
	params   []map[string]any
	profiles []string
}

func (r *semanticTraceReader) ScopedRead(_ context.Context, profileID, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
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

func TestTraceAssertionUsesTypedEdgesBudgetsAndVisitedSets(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	assertion := semanticTraceAssertion(domain.AssertionStatusActive, now)
	reader := &semanticTraceReader{rowSets: [][]map[string]any{
		{
			semanticTraceTestRow("assertion-2", "entity:mark", "mark", "person", "Mark", "entity:neo4j", "neo4j", "technology", "Neo4j", "USES", now),
			semanticTraceTestRow("assertion-1", "entity:mark", "mark", "person", "Mark", "entity:dense-mem", "dense-mem", "project", "Dense-Mem", "WORKS_ON", now),
			{"assertion_id": ""},
		},
		{
			semanticTraceTestRow("assertion-3", "entity:neo4j", "neo4j", "technology", "Neo4j", "value:version", "version", "value:string", "5.0", "HAS_VERSION", now),
		},
	}}
	include := true
	svc := New(Dependencies{
		Reader:     reader,
		Assertions: assertionservice.New(&contextAssertionStore{assertion: assertion}),
		FragmentGet: &fakeFragmentGet{fragments: map[string]*domain.Fragment{
			"fragment-1": fragmentFixture("fragment-1", "Mark works on Dense-Mem."),
		}},
	})

	got, err := svc.Trace(context.Background(), "team-a", TraceRequest{
		Type: AnchorAssertion, ID: " assertion-1 ", IncludeFragments: &include, MaxDepth: 1, MaxEdges: 4,
		RelationshipTypes: []string{"uses", "USES"}, Topic: " Neo4j ", MinRelevance: 0,
	})

	require.NoError(t, err)
	require.Equal(t, assertion, got.Anchor.Assertion)
	require.Len(t, got.SupportingFragments, 1)
	require.Len(t, got.SemanticEdges, 1)
	require.Equal(t, "USES", got.SemanticEdges[0].Relationship)
	require.Len(t, got.SemanticNodes, 2)
	require.Equal(t, []string{"dense-mem", "mark", "neo4j"}, got.VisitedEntityIDs)
	require.Equal(t, "depth_budget", got.StoppedReason)
	require.Len(t, got.Frontier, 1)
	require.Equal(t, "assertion-3", got.Frontier[0].AssertionID)
	require.Equal(t, "outgoing", got.Frontier[0].Direction)
	require.Equal(t, "team-a", reader.profiles[0])
	require.Equal(t, []string{"USES"}, reader.params[0]["relationshipTypes"])
	require.Equal(t, "neo4j", reader.params[0]["topic"])
	require.Equal(t, 0.15, reader.params[0]["minRelevance"])
	require.Contains(t, reader.queries[0], "relationship.semantic_projection = true")
}

func TestTraceAssertionStopsAtEdgeBudgetAndCanSkipFragments(t *testing.T) {
	assertion := semanticTraceAssertion(domain.AssertionStatusActive, time.Now().UTC())
	reader := &semanticTraceReader{rowSets: [][]map[string]any{{
		semanticTraceTestRow("assertion-2", "entity:mark", "mark", "person", "Mark", "entity:neo4j", "neo4j", "technology", "Neo4j", "USES", time.Now().UTC()),
	}, {}}}
	include := false
	svc := New(Dependencies{Reader: reader, Assertions: assertionservice.New(&contextAssertionStore{assertion: assertion})})

	got, err := svc.Trace(context.Background(), "team-a", TraceRequest{Type: AnchorAssertion, ID: "assertion-1", IncludeFragments: &include, MaxDepth: 4, MaxEdges: 1, MinRelevance: 0.5})

	require.NoError(t, err)
	require.Equal(t, "edge_budget", got.StoppedReason)
	require.Len(t, got.SemanticEdges, 1)
	require.Empty(t, got.SupportingFragments)
	require.Equal(t, 0.5, reader.params[0]["minRelevance"])
}

func TestTraceAssertionValidationAndDependencyErrors(t *testing.T) {
	ctx := context.Background()
	_, err := New(Dependencies{}).Trace(ctx, "team-a", TraceRequest{Type: AnchorAssertion, ID: "a"})
	require.ErrorContains(t, err, "assertion service is required")
	_, err = New(Dependencies{Assertions: assertionservice.New(&contextAssertionStore{})}).Trace(ctx, "team-a", TraceRequest{Type: AnchorAssertion, ID: "a"})
	require.ErrorContains(t, err, "graph reader is required")

	reader := &semanticTraceReader{}
	_, err = New(Dependencies{Reader: reader, Assertions: assertionservice.New(&contextAssertionStore{err: errors.New("assertion failed")})}).Trace(ctx, "team-a", TraceRequest{Type: AnchorAssertion, ID: "a"})
	require.ErrorContains(t, err, "assertion failed")
	_, err = New(Dependencies{Reader: reader, Assertions: assertionservice.New(&contextAssertionStore{})}).Trace(ctx, "team-a", TraceRequest{Type: AnchorAssertion, ID: "a"})
	require.ErrorContains(t, err, "assertion not found")

	quarantined := semanticTraceAssertion(domain.AssertionStatusQuarantined, time.Now().UTC())
	_, err = New(Dependencies{Reader: reader, Assertions: assertionservice.New(&contextAssertionStore{assertion: quarantined})}).Trace(ctx, "team-a", TraceRequest{Type: AnchorAssertion, ID: "a"})
	require.ErrorContains(t, err, "quarantined assertions")

	active := semanticTraceAssertion(domain.AssertionStatusActive, time.Now().UTC())
	svc := New(Dependencies{Reader: reader, Assertions: assertionservice.New(&contextAssertionStore{assertion: active})})
	skipFragments := false
	_, err = svc.Trace(ctx, "team-a", TraceRequest{Type: AnchorAssertion, ID: "a", IncludeFragments: &skipFragments, RelationshipTypes: []string{"---"}})
	require.ErrorContains(t, err, "invalid relationship type")
	_, err = svc.Trace(ctx, "team-a", TraceRequest{Type: AnchorAssertion, ID: "a", IncludeFragments: &skipFragments, MinRelevance: -0.1})
	require.ErrorContains(t, err, "min_relevance")
	_, err = svc.Trace(ctx, "team-a", TraceRequest{Type: AnchorAssertion, ID: "a", IncludeFragments: &skipFragments, MinRelevance: 1.1})
	require.ErrorContains(t, err, "min_relevance")

	reader = &semanticTraceReader{errs: []error{errors.New("trace failed")}}
	_, err = New(Dependencies{Reader: reader, Assertions: assertionservice.New(&contextAssertionStore{assertion: active})}).Trace(ctx, "team-a", TraceRequest{Type: AnchorAssertion, ID: "a", IncludeFragments: &skipFragments})
	require.ErrorContains(t, err, "trace failed")

	reader = &semanticTraceReader{rowSets: [][]map[string]any{{semanticTraceTestRow("assertion-2", "entity:mark", "mark", "person", "Mark", "entity:neo4j", "neo4j", "technology", "Neo4j", "USES", time.Now().UTC())}}, errs: []error{nil, errors.New("frontier failed")}}
	_, err = New(Dependencies{Reader: reader, Assertions: assertionservice.New(&contextAssertionStore{assertion: active})}).Trace(ctx, "team-a", TraceRequest{Type: AnchorAssertion, ID: "a", IncludeFragments: &skipFragments, MaxDepth: 1})
	require.ErrorContains(t, err, "frontier failed")
}

func semanticTraceAssertion(status domain.AssertionStatus, now time.Time) *domain.Assertion {
	return &domain.Assertion{
		AssertionID: "assertion-1", ProfileID: "team-a", SubjectEntityID: "mark", PredicateKey: "works_on", RelationshipType: "WORKS_ON",
		ObjectEntityID: "dense-mem", Tier: domain.AssertionTierValidatedClaim, Status: status, PolicyFamily: domain.AssertionPolicyVersioned,
		Polarity: domain.PolarityPlus, Modality: domain.ModalityAssertion, RecordedAt: now, SupportCount: 1, SourceGroupCount: 1,
		Evidence: []domain.EvidenceSpan{{FragmentID: "fragment-1", Start: 0, End: 4, SourceGroup: "source-1"}},
	}
}

func semanticTraceTestRow(assertionID, sourceKey, sourceID, sourceType, sourceName, targetKey, targetID, targetType, targetName, relationship string, now time.Time) map[string]any {
	return map[string]any{
		"assertion_id": assertionID, "source_key": sourceKey, "source_id": sourceID, "source_type": sourceType, "source_name": sourceName,
		"target_key": targetKey, "target_id": targetID, "target_type": targetType, "target_name": targetName,
		"relationship_type": relationship, "predicate": "uses", "tier": "validated_claim", "status": "active", "polarity": "+",
		"valid_from": now, "evidence_ids": []string{"fragment-1"},
	}
}
