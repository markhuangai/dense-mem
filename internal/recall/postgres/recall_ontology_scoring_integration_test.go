//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecallOntologyScoringDoesNotBoostExistingCandidates(t *testing.T) {
	f := newRecallOntologyFixture(t)
	var last ontology.SourceHandle
	for index := range 60 {
		last = f.evidence(t, index%2, fmt.Sprintf("Record %d describes an unrelated source.", index), nil)
	}
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE search_documents SET embedding='[0,1,0]'::vector WHERE team_id=?::uuid AND source_id=?::uuid`, f.team, last.ID).Error
	}))
	definition := ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "ranking catalog", Label: "Ranking catalog"}}
	f.publication(t, definition, f.assignment(t, definition.ID, last))
	input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "ranking catalog", QueryEmbedding: []float32{1, 0, 0}, Limit: 10}
	ordinary, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	input.OrganizationEnabled = true
	organized, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Empty(t, organized.Degradations)
	require.Equal(t, ordinary.Results, organized.Results)
	contract, err := f.search.GetActiveSearchContract(f.actor(2, "member"))
	require.NoError(t, err)
	batch, err := f.search.ReadEvidenceCandidates(f.actor(2, "member"), input, contract, 60)
	require.NoError(t, err)
	require.Empty(t, batch.TextHits)
	require.Empty(t, batch.ExpansionHits)
	require.Len(t, batch.OntologyHits, 1)
	require.Len(t, batch.VectorHits, 60)
	require.Equal(t, last.ID, batch.OntologyHits[0].SourceID)
	require.Equal(t, last.ID, batch.VectorHits[59].SourceID)
}

func TestRecallOntologyScoringUsesQueryAndBoundedSources(t *testing.T) {
	f := newRecallOntologyFixture(t)
	ctx := f.actor(2, "member")
	weak := f.evidence(t, 0, "alpha beta", nil)
	strong := f.evidence(t, 1, "alpha beta alpha beta", nil)
	f.evidence(t, 0, "alpha beta alpha beta alpha beta alpha beta", nil)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE search_documents SET embedding='[0,1,0]'::vector WHERE team_id=?::uuid AND source_id=?::uuid`, f.team, weak.ID).Error
	}))
	contract, err := f.search.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "alpha beta", QueryEmbedding: []float32{1, 0, 0}}
	require.NoError(t, f.rls.WithTeamReadOnlyRepeatableTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		score := func(ids []string) ([]SearchHit, error) {
			return searchRecallExactVectorSources(ctx, tx, input, contract, 1, ids)
		}
		ordinary, err := score([]string{weak.ID})
		require.NoError(t, err)
		require.Len(t, ordinary, 1)
		augmented, err := augmentRecallVectorChannel(ordinary, []SearchHit{{SourceID: strong.ID}, {SourceID: strong.ID}}, 1, score)
		require.NoError(t, err)
		require.Len(t, augmented, 1)
		require.Equal(t, strong.ID, augmented[0].SourceID)
		empty, err := score([]string{})
		require.NoError(t, err)
		require.Empty(t, empty, "an empty bounded pool must never become an unrestricted search")
		return nil
	}))
}

func TestRecallOntologyScoringKeepsSourceLookupsBounded(t *testing.T) {
	f := newRecallOntologyFixture(t)
	ctx := f.actor(2, "member")
	ids := []string{}
	allIDs := []string{}
	for index := range 96 {
		handle := f.evidence(t, index%2, fmt.Sprintf("Bounded scoring source %d", index), nil)
		allIDs = append(allIDs, handle.ID)
		if index < 60 {
			ids = append(ids, handle.ID)
		}
	}
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE search_documents SET embedding='[0,0,0]'::vector WHERE team_id=?::uuid AND source_id IN (?::uuid,?::uuid)`, f.team, allIDs[0], allIDs[95]).Error
	}))
	contract, err := f.search.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	capture := &recallQueryCapture{}
	db := newReadPerformanceCountedDB(f.app, &readPerformanceBenchmarkCounters{capture: capture})
	input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "bounded scoring", QueryEmbedding: []float32{1, 0, 0}}
	ordinary, err := f.search.ReadEvidenceCandidates(ctx, input, contract, 60)
	require.NoError(t, err)
	definition := ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: input.Query, Label: input.Query}}
	f.publication(t, definition, f.assignment(t, definition.ID, ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: allIDs[0], Version: 1}))
	input.OrganizationEnabled = true
	organized, err := f.search.ReadEvidenceCandidates(ctx, input, contract, 60)
	require.NoError(t, err)
	require.Empty(t, organized.OrganizationDegradation)
	require.Len(t, organized.OntologyHits, 1)
	require.Equal(t, ordinary.TextHits, organized.TextHits, "exhaustive text ranking already includes every eligible discovery in its scored pool")
	require.Equal(t, ordinary.VectorHits, organized.VectorHits, "exhaustive vector ranking already includes every eligible discovery in its scored pool")
	require.NoError(t, f.rls.WithTeamReadOnlyRepeatableTx(ctx, db, f.team, func(tx *gorm.DB) error {
		{
			hits, err := searchRecallExactVectorSources(ctx, tx, input, contract, 60, ids)
			require.NoError(t, err)
			require.Len(t, hits, len(ids))
			statement := capture.Statements[len(capture.Statements)-1]
			var encoded []byte
			require.NoError(t, tx.Statement.ConnPool.QueryRowContext(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+statement.rawSQL, statement.rawArgs...).Scan(&encoded))
			type node struct {
				Relation string  `json:"Relation Name"`
				Rows     float64 `json:"Actual Rows"`
				Loops    float64 `json:"Actual Loops"`
				Plans    []node  `json:"Plans"`
			}
			var reports []struct{ Plan node }
			require.NoError(t, json.Unmarshal(encoded, &reports))
			require.Len(t, reports, 1)
			var sourceRows float64
			var visit func(node)
			visit = func(current node) {
				if current.Relation == "evidence_fragments" {
					sourceRows += current.Rows * current.Loops
				}
				for _, child := range current.Plans {
					visit(child)
				}
			}
			visit(reports[0].Plan)
			require.Equal(t, float64(len(ids)), sourceRows, "bounded scoring must not scan the team's fragments once per candidate")
		}
		score := func(ids []string) ([]SearchHit, error) {
			return searchRecallExactVectorSources(ctx, tx, input, contract, len(allIDs), ids)
		}
		ordinary, err := score(ids)
		require.NoError(t, err)
		discoveries := make([]SearchHit, 0, len(allIDs))
		for _, id := range allIDs {
			discoveries = append(discoveries, SearchHit{SourceID: id})
		}
		merged, err := augmentRecallVectorChannel(ordinary, discoveries, len(allIDs), score)
		require.NoError(t, err)
		expected, err := score(allIDs)
		require.NoError(t, err)
		require.Len(t, merged, len(expected))
		for index := range expected {
			require.Equal(t, expected[index].SourceID, merged[index].SourceID, "reused scores must preserve PostgreSQL distance and document-ID ordering, including NaN distances")
		}
		return nil
	}))
}
