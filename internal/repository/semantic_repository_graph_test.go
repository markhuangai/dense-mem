package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRecallHypothesesReadsPostgresRows(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewSemanticRepository(db, nil)
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	hypothesisID := uuid.NewString()
	now := time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM semantic_hypotheses.*status IN \('proposed', 'reinforced'\)`).
		WithArgs(teamID, "%postgres%", "%postgres%", "%postgres%", "%postgres%", "%postgres%", 5).
		WillReturnRows(sqlmock.NewRows([]string{
			"hypothesis_id", "owner_profile_id", "text", "status", "source_refs", "metadata", "created_at", "updated_at",
		}).AddRow(
			hypothesisID,
			ownerID,
			"Postgres recall may improve relationship grounding.",
			string(domain.SemanticHypothesisProposed),
			[]byte(`[{"type":"relationship","id":"rel-1"}]`),
			[]byte(`{"what_if":"What if related edges are traversed?","confidence":0.7}`),
			now,
			now,
		)).
		RowsWillBeClosed()
	mock.ExpectCommit()

	got, err := repo.RecallHypotheses(context.Background(), teamID, "postgres", 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, hypothesisID, got[0].DreamID)
	require.Equal(t, ownerID, got[0].ProfileID)
	require.Equal(t, domain.DreamStatusProposed, got[0].Status)
	require.Equal(t, "What if related edges are traversed?", got[0].WhatIf)
	require.Equal(t, 0.7, got[0].Confidence)
	require.Equal(t, []domain.DreamSourceRef{{Type: "relationship", ID: "rel-1"}}, got[0].SourceRefs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSemanticGraphReadsEntityValueEdges(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewSemanticRepository(db, nil)

	teamID := uuid.NewString()
	relationshipID := uuid.NewString()
	entityID := uuid.NewString()
	valueID := uuid.NewString()
	now := time.Date(2026, 7, 16, 20, 0, 0, 0, time.UTC)

	graphRows := sqlmock.NewRows([]string{
		"relationship_id", "predicate",
		"source_key", "source_id", "source_title", "source_body", "source_status", "source_recorded_at",
		"target_key", "target_id", "target_type", "target_title", "target_body", "target_status", "target_recorded_at",
	}).AddRow(
		relationshipID, "uses",
		"entity:"+entityID, entityID, "Dense-Mem", string(domain.SemanticEntityProject), "active", now,
		"value:"+valueID, valueID, "value", "PostgreSQL", string(domain.SemanticValueString), "active", now,
	)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM semantic_relationship_records r.*LEFT JOIN semantic_values value.*ORDER BY r\.updated_at DESC`).
		WithArgs(teamID, "postgres", "postgres", 10).
		WillReturnRows(graphRows).
		RowsWillBeClosed()
	mock.ExpectCommit()

	graph, err := repo.SemanticGraph(context.Background(), teamID, domain.SemanticGraphQuery{
		Scope: "overview",
		Query: "postgres",
		Types: []string{"entity", "value"},
		Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, graph.Nodes, 2)
	require.Len(t, graph.Edges, 1)
	require.Equal(t, "entity:"+entityID, graph.Edges[0].Source)
	require.Equal(t, "value:"+valueID, graph.Edges[0].Target)
	require.Contains(t, semanticGraphTestNodeTitles(graph.Nodes), "Dense-Mem")
	require.Contains(t, semanticGraphTestNodeTitles(graph.Nodes), "PostgreSQL")

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM semantic_values.*WHERE team_id = .*value_id = .*status = 'active'`).
		WithArgs(teamID, valueID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "id", "title", "body", "status", "recorded_at"}).
			AddRow("value:"+valueID, valueID, "PostgreSQL", string(domain.SemanticValueString), "active", now)).
		RowsWillBeClosed()
	mock.ExpectCommit()

	detail, err := repo.SemanticGraphNodeDetail(context.Background(), teamID, "value", valueID)
	require.NoError(t, err)
	require.Equal(t, "value:"+valueID, detail.Key)
	require.Equal(t, "PostgreSQL", detail.Title)
	require.NoError(t, mock.ExpectationsWereMet())
}

func semanticGraphTestNodeTitles(nodes []domain.SemanticGraphNode) []string {
	titles := make([]string, 0, len(nodes))
	for _, node := range nodes {
		titles = append(titles, node.Title)
	}
	return titles
}

func TestNormalizeSemanticValueClassifiesTypedValues(t *testing.T) {
	cases := []struct {
		input     string
		valueType domain.SemanticValueType
		canonical string
	}{
		{" TRUE ", domain.SemanticValueBoolean, "true"},
		{"42.50", domain.SemanticValueNumber, "42.5"},
		{"2026-07-16", domain.SemanticValueDate, "2026-07-16"},
		{"2026-07-16T20:00:00Z", domain.SemanticValueDateTime, "2026-07-16T20:00:00Z"},
		{"PostgreSQL", domain.SemanticValueString, "postgresql"},
	}
	for _, tc := range cases {
		got := normalizeSemanticValue(tc.input)
		require.Equal(t, tc.valueType, got.ValueType)
		require.Equal(t, tc.canonical, got.CanonicalValue)
	}
}

func TestSemanticVectorCandidateReadersScanRows(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	teamID := uuid.NewString()
	evidenceID := uuid.NewString()
	relationshipID := uuid.NewString()
	entityID := uuid.NewString()
	now := time.Date(2026, 7, 16, 20, 0, 0, 0, time.UTC)
	scope := domain.SemanticRecallSearchScope{
		TeamID:               teamID,
		EmbeddingContractID:  "contract",
		ValidAt:              now,
		KnownAt:              now,
		BranchLimit:          12,
		KnownEvidenceIDs:     []string{},
		KnownRelationshipIDs: []string{},
	}

	candidateRows := sqlmock.NewRows([]string{
		"evidence_id", "branch", "branch_rank", "raw_score",
		"exact_match", "precise_match", "phrase_match", "all_hard_anchors_matched",
		"fact_support", "source_group_count", "latest_valid_from", "latest_recorded_at",
		"relationship_ids", "matched_entity_ids",
	}).AddRow(
		evidenceID, string(domain.SemanticRecallBranchEvidenceVector), 1, 0.91,
		false, false, false, false,
		true, 2, now, now,
		pq.StringArray{relationshipID}, pq.StringArray{entityID},
	)
	mock.ExpectQuery(`(?s)WITH.*evidence_ann AS MATERIALIZED`).
		WithArgs("[1,0]", "contract", now, now, 12, 48, teamID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(candidateRows).
		RowsWillBeClosed()

	candidates, err := searchRecallVectorEvidenceCandidates(context.Background(), db, scope, "[1,0]")
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, evidenceID, candidates[0].EvidenceID)
	require.Equal(t, []string{relationshipID}, candidates[0].RelationshipIDs)

	seedRows := sqlmock.NewRows([]string{"entity_id", "branch_rank", "exact", "hard_anchor", "explicit", "score"}).
		AddRow(entityID, 1, false, false, false, 0.88)
	mock.ExpectQuery(`(?s)WITH.*entity_ann AS MATERIALIZED`).
		WithArgs("[1,0]", "contract", teamID).
		WillReturnRows(seedRows).
		RowsWillBeClosed()

	seeds, err := searchRecallVectorEntitySeeds(context.Background(), db, scope, "[1,0]")
	require.NoError(t, err)
	require.Len(t, seeds, 1)
	require.Equal(t, entityID, seeds[0].EntityID)
	require.NoError(t, mock.ExpectationsWereMet())
}
