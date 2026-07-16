package contextservice

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/stretchr/testify/require"
)

type stubSemanticTraceStore struct {
	teamID         string
	relationshipID string
	result         *domain.SemanticTraceResult
	err            error
}

func (s *stubSemanticTraceStore) TraceRelationship(_ context.Context, teamID string, relationshipID string) (*domain.SemanticTraceResult, error) {
	s.teamID = teamID
	s.relationshipID = relationshipID
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

type stubSemanticContextRecall struct {
	profileID string
	req       recallservice.RecallRequest
	hits      []recallservice.RecallHit
	err       error
}

func (s *stubSemanticContextRecall) Recall(_ context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	s.profileID = profileID
	s.req = req
	if s.err != nil {
		return nil, s.err
	}
	return s.hits, nil
}

func TestSemanticContextTraceReturnsRelationshipEvidenceAndSupports(t *testing.T) {
	includeContent := false
	store := &stubSemanticTraceStore{result: &domain.SemanticTraceResult{
		Relationship: &domain.SemanticRelationship{
			RelationshipID:  "rel-1",
			SubjectEntityID: "entity-subject",
			Predicate:       "uses",
			ObjectValue:     "Postgres",
			Tier:            domain.SemanticTierFact,
		},
		Evidence: []domain.SemanticEvidenceFragment{{
			FragmentID: "evidence-1",
			Content:    "Dense-Mem v2 uses Postgres.",
		}},
		Supports: []domain.SemanticRelationshipSupport{{
			RelationshipID: "rel-1",
			FragmentID:     "evidence-1",
			EvidenceIndex:  0,
		}},
	}}
	svc := NewSemantic(store, nil)

	got, err := svc.Trace(context.Background(), " team-1 ", TraceRequest{
		Type:                   "relationship",
		ID:                     " rel-1 ",
		IncludeEvidenceContent: &includeContent,
	})

	require.NoError(t, err)
	require.Equal(t, "team-1", store.teamID)
	require.Equal(t, "rel-1", store.relationshipID)
	require.Equal(t, "relationship", got.Anchor.Type)
	require.Equal(t, "rel-1", got.Anchor.Relationship.RelationshipID)
	require.Len(t, got.SemanticEvidence, 1)
	require.Empty(t, got.SemanticEvidence[0].Content)
	require.Len(t, got.SemanticSupports, 1)
}

func TestSemanticContextTraceValidatesAndMapsNotFound(t *testing.T) {
	_, err := NewSemantic(nil, nil).Trace(context.Background(), "team", TraceRequest{RelationshipID: "rel"})
	require.ErrorContains(t, err, "store is required")

	store := &stubSemanticTraceStore{}
	svc := NewSemantic(store, nil)

	_, err = svc.Trace(context.Background(), "", TraceRequest{RelationshipID: "rel"})
	require.ErrorContains(t, err, "team id is required")

	_, err = svc.Trace(context.Background(), "team", TraceRequest{})
	require.ErrorContains(t, err, "relationship_id is required")

	store.err = sql.ErrNoRows
	_, err = svc.Trace(context.Background(), "team", TraceRequest{RelationshipID: "rel-missing"})
	require.ErrorContains(t, err, "relationship not found")

	want := errors.New("postgres unavailable")
	store.err = want
	_, err = svc.Trace(context.Background(), "team", TraceRequest{RelationshipID: "rel-1"})
	require.ErrorIs(t, err, want)
}

func TestSemanticContextAssembleConvertsSemanticRecallHits(t *testing.T) {
	includeEvidence := true
	recall := &stubSemanticContextRecall{hits: []recallservice.RecallHit{
		{
			Relationship: &domain.SemanticRelationship{
				RelationshipID:  "rel-1",
				SubjectEntityID: "dense-mem",
				Predicate:       "uses",
				ObjectValue:     "Postgres",
				Tier:            domain.SemanticTierValidatedClaim,
			},
			Supports: []domain.SemanticRelationshipSupport{{
				RelationshipID: "rel-1",
				FragmentID:     "evidence-1",
				EvidenceIndex:  0,
			}},
			Score: 0.91,
		},
		{
			Evidence: &domain.SemanticEvidenceFragment{
				FragmentID: "evidence-2",
				Content:    "pgvector stores embeddings.",
			},
			Score: 0.52,
		},
		{Score: 0.1},
	}}
	svc := NewSemantic(nil, recall)

	got, err := svc.Assemble(context.Background(), " team-1 ", AssembleRequest{
		Query:           " v2 storage ",
		Limit:           2,
		MaxChars:        1200,
		IncludeEvidence: &includeEvidence,
	})

	require.NoError(t, err)
	require.Equal(t, "team-1", recall.profileID)
	require.Equal(t, "v2 storage", recall.req.Query)
	require.Equal(t, 2, recall.req.Limit)
	require.True(t, recall.req.IncludeEvidence)
	require.Equal(t, "v2 storage", got.Query)
	require.Len(t, got.Items, 2)
	require.Equal(t, "relationship", got.Items[0].Type)
	require.Equal(t, "rel-1", got.Items[0].ID)
	require.Len(t, got.Items[0].Supports, 1)
	require.Equal(t, "evidence", got.Items[1].Type)
	require.Equal(t, "evidence-2", got.Items[1].ID)
	require.Contains(t, got.ContextBlock, "Dense-Mem semantic context")
	require.Contains(t, got.ContextBlock, "[relationship:rel-1]")
	require.Contains(t, got.ContextBlock, "[evidence:evidence-2]")
}

func TestSemanticContextAssembleValidatesAndPropagatesRecallError(t *testing.T) {
	_, err := NewSemantic(nil, nil).Assemble(context.Background(), "team", AssembleRequest{Query: "q"})
	require.ErrorContains(t, err, "recall service is required")

	recall := &stubSemanticContextRecall{}
	svc := NewSemantic(nil, recall)

	_, err = svc.Assemble(context.Background(), "", AssembleRequest{Query: "q"})
	require.ErrorContains(t, err, "team id is required")

	_, err = svc.Assemble(context.Background(), "team", AssembleRequest{})
	require.ErrorContains(t, err, "query is required")

	want := errors.New("recall failed")
	recall.err = want
	_, err = svc.Assemble(context.Background(), "team", AssembleRequest{Query: "q"})
	require.ErrorIs(t, err, want)
}

func TestBuildSemanticContextBlockTruncatesAndSkipsNilItems(t *testing.T) {
	items := []ContextItem{
		{Type: "relationship"},
		{Type: "evidence"},
		{
			Type:  "relationship",
			Score: 0.5,
			Relationship: &domain.SemanticRelationship{
				RelationshipID:  "rel-1",
				SubjectEntityID: "subject",
				Predicate:       "has_long_value",
				ObjectValue:     stringsOf("x", 900),
				Tier:            domain.SemanticTierCandidate,
			},
		},
		{
			Type:  "evidence",
			Score: 0.4,
			Evidence: &domain.SemanticEvidenceFragment{
				FragmentID: "evidence-1",
				Content:    "later evidence should be truncated " + stringsOf("y", 800),
			},
		},
	}

	block, truncated := buildSemanticContextBlock("query\nwith newline", items, 500)

	require.True(t, truncated)
	require.Contains(t, block, "Query: query with newline")
	require.Contains(t, block, "[relationship:rel-1]")
	require.NotContains(t, block, "later evidence should be truncated")
	require.Equal(t, "fallback", firstNonEmpty(" ", "", "fallback"))
}

func stringsOf(value string, count int) string {
	out := ""
	for i := 0; i < count; i++ {
		out += value
	}
	return out
}
