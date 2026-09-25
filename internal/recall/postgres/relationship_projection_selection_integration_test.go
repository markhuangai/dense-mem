//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

func TestRelationshipProjectionANNAndAllTeamReadinessKeepDistinctGenerationPolicies(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "projection-ann-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "projection-ann-owner-a")
	teamB := createLedgerTeam(t, adminDB, rls, "projection-ann-team-b")
	ownerB := createLedgerProfile(t, adminDB, rls, teamB, "projection-ann-owner-b")
	insertSearchTestContractWithFallback(t, adminDB, rls, "projection-ann", 3, "vector_hnsw", "densemem_issue457_relationship_hnsw", true)
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	semantic := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	store := newSearchFixtureStore(appDB, rls)

	create := func(teamID, ownerID, label string) *knowledgepostgres.RelationshipDecisionResult {
		subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", label)
		object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense Mem")
		content := fmt.Sprintf("%s works on Dense Mem.", label)
		ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "projection-ann-"+label, content)
		decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
			SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
			Support: &EvidenceSupportInput{
				FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "projection-ann-" + label,
				SpanStart: 0, SpanEnd: len(content), Authority: "primary",
			},
		})
		require.NotNil(t, decision.Relationship)
		return decision
	}

	active := create(teamA, ownerA, "Active Reader")
	other := create(teamB, ownerB, "Other Reader")
	activeGeneration := uuid.NewString()
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamA, activeGeneration, 1, "current")
	activeDocument, err := store.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamA, OwnerProfileID: ownerA, SourceKind: "relationship",
		SourceID: active.Relationship.RelationshipID, SourceVersion: int64(active.Relationship.Version),
		ProjectionGenerationID: activeGeneration, DocumentText: "relationship activeprojectionmarker",
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, store, teamA, map[string][]float32{
		activeDocument.SearchDocumentID: {1, 0, 0},
	})
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamA, uuid.NewString(), 2, "failed")
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamA, uuid.NewString(), 3, "embedding")

	readiness, err := store.CheckSearchReadiness(ctx)
	require.NoError(t, err)
	require.Contains(t, searchReadinessReasonCodes(readiness), "relationship_projection_text_incomplete")

	otherDocument, err := store.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamB, OwnerProfileID: ownerB, SourceKind: "relationship",
		SourceID: other.Relationship.RelationshipID, SourceVersion: int64(other.Relationship.Version),
		DocumentText: "relationship otherprojectionmarker",
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, store, teamB, map[string][]float32{
		otherDocument.SearchDocumentID: {1, 0, 0},
	})
	type nonCurrentTeam struct {
		teamID, relationshipID, marker, state string
	}
	onlyNonCurrent := make([]nonCurrentTeam, 0, 2)
	for _, state := range []string{"embedding", "failed"} {
		label := "Only " + state
		teamID := createLedgerTeam(t, adminDB, rls, "projection-ann-only-"+state)
		ownerID := createLedgerProfile(t, adminDB, rls, teamID, "projection-ann-only-owner-"+state)
		decision := create(teamID, ownerID, label)
		generationID := uuid.NewString()
		insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, generationID, 1, state)
		marker := "only" + state + "marker"
		document, err := store.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship",
			SourceID: decision.Relationship.RelationshipID, SourceVersion: int64(decision.Relationship.Version),
			ProjectionGenerationID: generationID, DocumentText: "relationship " + marker,
		})
		require.NoError(t, err)
		require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Exec(`UPDATE search_documents
				SET search_state = 'current', embedding = '[1,0,0]'::vector,
				    embedding_updated_at = now()
				WHERE team_id = ?::uuid AND search_document_id = ?::uuid`, teamID, document.SearchDocumentID).Error
		}))
		onlyNonCurrent = append(onlyNonCurrent, nonCurrentTeam{
			teamID: teamID, relationshipID: decision.Relationship.RelationshipID, marker: marker, state: state,
		})
	}
	t.Cleanup(func() {
		if err := rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
			return tx.Exec("DROP INDEX IF EXISTS densemem_issue457_relationship_hnsw").Error
		}); err != nil {
			t.Errorf("drop test HNSW index: %v", err)
		}
	})
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec("CREATE INDEX IF NOT EXISTS densemem_issue457_relationship_hnsw ON search_documents USING hnsw ((embedding::vector(3)) vector_cosine_ops)").Error; err != nil {
			return err
		}
		return tx.Exec("ANALYZE search_documents").Error
	}))

	readiness, err = store.CheckSearchReadiness(ctx)
	require.NoError(t, err)
	require.NotContains(t, searchReadinessReasonCodes(readiness), "relationship_projection_text_incomplete")
	capture := &projectionQueryCapture{}
	counters := &readPerformanceBenchmarkCounters{capture: capture}
	reads := newSearchFixtureStore(newReadPerformanceCountedDB(appDB, counters), rls)
	contract, err := reads.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	for _, tc := range onlyNonCurrent {
		textHits, err := reads.SearchFullText(ctx, FullTextSearchInput{
			TeamID: tc.teamID, Query: tc.marker, SourceKind: "relationship", Limit: 5,
		})
		require.NoError(t, err)
		require.Len(t, textHits, 1)
		require.Equal(t, tc.relationshipID, textHits[0].SourceID)
		recalled, err := reads.RecallRelationships(ctx, RecallRelationshipsInput{
			TeamID: tc.teamID, Query: tc.marker, QueryEmbedding: []float32{1, 0, 0}, Limit: 5,
		})
		require.NoError(t, err)
		require.True(t, recalled.VectorOmitted)
		require.Len(t, recalled.Results, 1)
		require.Equal(t, tc.relationshipID, recalled.Results[0].RelationshipID)
		if tc.state == "failed" {
			require.Equal(t, "failed", recalled.SearchState)
		} else {
			require.Equal(t, "pending", recalled.SearchState)
		}
		input := RecallRelationshipsInput{TeamID: tc.teamID, QueryEmbedding: []float32{1, 0, 0}, Limit: 5}
		var exactHits, annHits []SearchHit
		require.NoError(t, rls.WithTeamTx(ctx, appDB, tc.teamID, func(tx *gorm.DB) error {
			var err error
			exactHits, err = searchRecallRelationshipExactVector(ctx, tx, input, contract, 5)
			if err != nil {
				return err
			}
			annHits, err = searchRecallRelationshipANNVector(ctx, tx, input, contract, 5)
			return err
		}))
		require.Empty(t, exactHits)
		require.Empty(t, annHits)
	}

	textHits, err := reads.SearchFullText(ctx, FullTextSearchInput{
		TeamID: teamA, Query: "activeprojectionmarker", SourceKind: "relationship", Limit: 5,
	})
	require.NoError(t, err)
	require.Len(t, textHits, 1)
	require.Equal(t, active.Relationship.RelationshipID, textHits[0].SourceID)

	for _, tc := range []struct {
		teamID, wantID string
	}{
		{teamA, active.Relationship.RelationshipID},
		{teamB, other.Relationship.RelationshipID},
	} {
		result, err := reads.RecallRelationships(ctx, RecallRelationshipsInput{
			TeamID: tc.teamID, Query: "nomatchissue457", QueryEmbedding: []float32{1, 0, 0}, Limit: 5,
		})
		require.NoError(t, err)
		require.False(t, result.VectorOmitted)
		require.Equal(t, "current", result.SearchState)
		require.Len(t, result.Results, 1)
		require.Equal(t, tc.wantID, result.Results[0].RelationshipID)
		require.Equal(t, 1, result.Results[0].Rank)
	}
	if output := os.Getenv("DENSE_MEM_PROJECTION_PLAN_REPORT"); output != "" {
		var statement *projectionQueryStatement
		for index := range capture.Statements {
			item := &capture.Statements[index]
			if strings.Contains(item.SQL, "ann_candidates AS MATERIALIZED") &&
				strings.Contains(strings.Join(item.Args, " "), teamA) {
				statement = item
				break
			}
		}
		require.NotNil(t, statement)
		plan := []string{}
		require.NoError(t, rls.WithTeamTx(ctx, appDB, teamA, func(tx *gorm.DB) error {
			rows, err := tx.Raw("EXPLAIN (ANALYZE, BUFFERS) "+statement.rawSQL, statement.rawArgs...).Rows()
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					return err
				}
				plan = append(plan, line)
			}
			return rows.Err()
		}))
		require.Contains(t, strings.Join(plan, "\n"), "Buffers:")
		path, err := filepath.Abs(output)
		require.NoError(t, err)
		require.Contains(t, filepath.ToSlash(path), "/tests/eval/runs/issue-457/")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		payload, err := json.MarshalIndent(map[string]any{"case": "relationship_ann", "plan": plan}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(payload, '\n'), 0o644))
	}
}
