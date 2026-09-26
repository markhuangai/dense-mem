//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type recallRetrievalEquivalenceRecord struct {
	Case                   string    `json:"case"`
	InputEntityIDs         []string  `json:"input_entity_ids,omitempty"`
	SelectedIDs            []string  `json:"selected_ids"`
	Ranks                  []int     `json:"ranks"`
	Scores                 []float64 `json:"scores"`
	HitSearchStates        []string  `json:"hit_search_states"`
	SearchState            string    `json:"search_state"`
	SpaceKinds             []string  `json:"space_kinds"`
	Contexts               []string  `json:"contexts,omitempty"`
	CreatedAt              []string  `json:"created_at"`
	EvidenceCount          int       `json:"evidence_count,omitempty"`
	ConflictCount          int       `json:"conflict_count,omitempty"`
	Statements             int64     `json:"statements"`
	Transactions           int64     `json:"transactions"`
	TransactionCompletions int64     `json:"transaction_completions"`
}

func TestRecallRetrievalEquivalenceCorpus(t *testing.T) {
	fixture := newReadPerformanceEquivalenceFixture(t)
	require.Equal(t, readPerformanceFixedID("team:recall-read-performance"), fixture.teamID)
	require.Equal(t, readPerformanceFixedID("profile:recall-read-performance"), fixture.ownerID)
	require.Equal(t, readPerformanceFixedID("team:recall-read-performance-ann"), fixture.annTeamID)
	require.Equal(t, readPerformanceFixedID("profile:recall-read-performance-ann"), fixture.annOwnerID)
	require.Equal(t, readPerformanceFixedID("team:recall-read-performance-private"), fixture.privateTeam)
	require.Equal(t, readPerformanceFixedID("private-space"), fixture.privateID)
	require.Equal(t, readPerformanceFixedID("relationship"), fixture.relationshipID)
	require.Equal(t, readPerformanceFixedID("entity:subject"), fixture.entityID)
	require.Equal(t, readPerformanceFixedID("recall benchmark lexical marker:0001"), fixture.lexicalID)
	require.Equal(t, readPerformanceFixedID("private benchmark marker:0000"), fixture.privateEvidenceID)
	require.Equal(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), fixture.knownAt)
	ctx := context.Background()
	queries := []struct {
		name     string
		input    RecallEvidenceInput
		wantIDs  []string
		contexts []string
		actor    *requestctx.Actor
	}{
		{
			name: "lexical", wantIDs: []string{fixture.lexicalID},
			input:    RecallEvidenceInput{TeamID: fixture.teamID, Query: "recall benchmark lexical marker deterministic evidence record 0001", Limit: 1},
			contexts: []string{"recall benchmark lexical marker deterministic evidence record 0001"},
		},
		{
			name: "ann", wantIDs: fixture.annIDs,
			input: RecallEvidenceInput{TeamID: fixture.annTeamID, Query: "zzbenchmarkannlexicalmiss", QueryEmbedding: []float32{1, 0, 0}, Limit: 3},
			contexts: []string{
				"ann benchmark marker deterministic evidence record 0000",
				"ann benchmark marker deterministic evidence record 0001",
				"ann benchmark marker deterministic evidence record 0002",
			},
		},
		{
			name: "expansion", wantIDs: []string{fixture.supportID},
			input:    RecallEvidenceInput{TeamID: fixture.teamID, ExpandFromEntityIDs: []string{fixture.entityID}, Limit: 1},
			contexts: []string{"Benchmark Reader works on Dense Mem. Relationship support evidence record 0000"},
		},
		{
			name: "historical", wantIDs: []string{fixture.lexicalID},
			input:    RecallEvidenceInput{TeamID: fixture.teamID, Query: "recall benchmark lexical marker deterministic evidence record 0001", KnownAt: &fixture.knownAt, Limit: 1},
			contexts: []string{"recall benchmark lexical marker deterministic evidence record 0001"},
		},
		{
			name: "private_space", wantIDs: []string{fixture.privateEvidenceID},
			input:    RecallEvidenceInput{TeamID: fixture.privateTeam, Query: "private benchmark marker deterministic evidence record 0000", SpaceID: fixture.privateID, SpaceKind: string(domain.MemorySpaceCredentialPrivate), Limit: 1},
			contexts: []string{"private benchmark marker deterministic evidence record 0000"},
			actor:    &fixture.privateActor,
		},
	}
	report := make([]recallRetrievalEquivalenceRecord, 0, len(queries)+1)
	for _, query := range queries {
		t.Run(query.name, func(t *testing.T) {
			readCtx := ctx
			if query.actor != nil {
				readCtx = requestctx.WithActor(readCtx, *query.actor)
			}
			fixture.counters.reset()
			result, err := fixture.store.RecallEvidence(readCtx, query.input)
			require.NoError(t, err)
			require.Len(t, result.Results, len(query.wantIDs))
			record := recallRetrievalEquivalenceRecord{Case: query.name, InputEntityIDs: query.input.ExpandFromEntityIDs, SearchState: result.SearchState}
			for index, hit := range result.Results {
				require.Equal(t, query.wantIDs[index], hit.EvidenceID)
				require.Equal(t, query.contexts[index], hit.Context)
				require.Equal(t, "document", hit.SourceType)
				require.Equal(t, index+1, hit.Rank)
				require.Greater(t, hit.Score, 0.0)
				require.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), hit.CreatedAt.UTC())
				record.SelectedIDs = append(record.SelectedIDs, hit.EvidenceID)
				record.Ranks = append(record.Ranks, hit.Rank)
				record.Scores = append(record.Scores, hit.Score)
				record.HitSearchStates = append(record.HitSearchStates, hit.SearchState)
				record.SpaceKinds = append(record.SpaceKinds, hit.SpaceKind)
				record.Contexts = append(record.Contexts, hit.Context)
				record.CreatedAt = append(record.CreatedAt, hit.CreatedAt.UTC().Format(time.RFC3339Nano))
			}
			counts := fixture.counters.snapshot()
			require.Equal(t, counts.transactions, counts.commits+counts.rollbacks)
			require.Greater(t, counts.statements, int64(0))
			record.EvidenceCount = len(result.Results)
			record.ConflictCount = len(result.Conflicts) + len(result.EvidenceConflicts)
			record.Statements = counts.statements
			record.Transactions = counts.transactions
			record.TransactionCompletions = counts.commits + counts.rollbacks
			report = append(report, record)
		})
	}
	t.Run("relationship", func(t *testing.T) {
		fixture.counters.reset()
		result, err := fixture.store.RecallRelationships(ctx, RecallRelationshipsInput{
			TeamID: fixture.teamID, Query: "relationship benchmark", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		hit := result.Results[0]
		require.Equal(t, fixture.relationshipID, hit.RelationshipID)
		require.Equal(t, []string{fixture.supportID}, hit.EvidenceIDs)
		require.Equal(t, 1, hit.Rank)
		require.Greater(t, hit.Score, 0.0)
		require.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), hit.CreatedAt.UTC())
		counts := fixture.counters.snapshot()
		require.Equal(t, counts.transactions, counts.commits+counts.rollbacks)
		require.Greater(t, counts.statements, int64(0))
		report = append(report, recallRetrievalEquivalenceRecord{
			Case: "relationship", SelectedIDs: []string{hit.RelationshipID}, Ranks: []int{hit.Rank},
			Scores: []float64{hit.Score}, HitSearchStates: []string{hit.SearchState},
			SearchState: result.SearchState, SpaceKinds: []string{hit.SpaceKind},
			CreatedAt:     []string{hit.CreatedAt.UTC().Format(time.RFC3339Nano)},
			EvidenceCount: len(hit.EvidenceIDs), Statements: counts.statements,
			Transactions: counts.transactions, TransactionCompletions: counts.commits + counts.rollbacks,
		})
	})
	if output := os.Getenv("DENSE_MEM_RECALL_EQUIVALENCE_REPORT"); output != "" {
		path, err := filepath.Abs(output)
		require.NoError(t, err)
		runs, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "eval", "runs"))
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(path, runs+string(os.PathSeparator)), "report must stay under tests/eval/runs")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		payload, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(payload, '\n'), 0o644))
	}
}
