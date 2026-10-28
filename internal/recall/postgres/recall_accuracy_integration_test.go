//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func accuracyFrozenLock(t *testing.T) evalharness.RecallAccuracyLock {
	t.Helper()
	actual, err := evalharness.GenerateRecallAccuracy(filepath.Join(t.TempDir(), "cohort"))
	require.NoError(t, err)
	data, err := os.ReadFile("../../../tests/eval/source_locks/recall_accuracy_v1.json")
	require.NoError(t, err)
	var frozen evalharness.RecallAccuracyLock
	require.NoError(t, json.Unmarshal(data, &frozen))
	frozen.GeneratorHashes = nil
	require.Equal(t, frozen, actual)
	t.Logf("RECALL_ACCURACY_LOCK %s", data)
	return frozen
}

func TestRecallAccuracyCohort(t *testing.T) {
	lock := accuracyFrozenLock(t)
	var repetitions [2][]recallAccuracyObservation
	for repetition := range repetitions {
		t.Run([]string{"first_fixture", "fresh_fixture"}[repetition], func(t *testing.T) {
			f := newRecallAccuracyFixture(t, false, true)
			for _, enabled := range []bool{false, true} {
				scores := []evalharness.RetrievalScore{}
				cases, suite := []evalharness.Case{}, []evalharness.SuiteCase{}
				for _, c := range f.cohort {
					t.Run(c.Case.CaseID+map[bool]string{false: "/ordinary", true: "/ontology"}[enabled], func(t *testing.T) {
						observation := f.observe(t, c, enabled, "constant_vectors", false)
						repetitions[repetition] = append(repetitions[repetition], observation)
						scores = append(scores, observation.Score)
					})
					cases = append(cases, c.Case)
					suite = append(suite, evalharness.SuiteCase{CaseID: c.Case.CaseID, Weight: 1, Slices: c.Case.Slices})
				}
				require.Len(t, scores, 24)
				summary := evalharness.SummarizeScores("recall-accuracy", "baseline", lock.SeedID, lock.SeedHash, "suite.jsonl", suite, evalharness.IndexCases(cases), scores)
				encoded, err := json.Marshal(map[string]any{"repetition": repetition, "ontology_enabled": enabled, "embedding_fixture": "constant unit vectors [1,0,0], dimensions 3; retrieval logic only", "summary": summary})
				require.NoError(t, err)
				t.Logf("RECALL_ACCURACY_SUMMARY %s", encoded)
			}
			require.Equal(t, f.preserved, f.canonicalSnapshot(t))
		})
	}
	require.Len(t, repetitions[0], 48)
	require.Equal(t, repetitions[0], repetitions[1], "fresh fixtures must reproduce source rankings and every metric")
	t.Log(`RECALL_ACCURACY_REPEATABILITY {"fresh_runs":2,"normalized_observations_equal":true}`)
}

func TestRecallAccuracyVariations(t *testing.T) {
	accuracyFrozenLock(t)
	for _, variant := range []struct {
		name             string
		reverse, vectors bool
	}{{"reversed_uuid_order", true, true}, {"missing_embeddings", false, false}} {
		t.Run(variant.name, func(t *testing.T) {
			f := newRecallAccuracyFixture(t, variant.reverse, variant.vectors)
			for _, c := range f.cohort {
				for _, enabled := range []bool{false, true} {
					f.observe(t, c, enabled, variant.name, false)
				}
			}
			if !variant.vectors {
				for _, c := range f.cohort {
					if c.Case.Slices[0] == "multihop" {
						observation := f.observe(t, c, true, "explicit_entity_expansion", true)
						require.Contains(t, flattenedAccuracySources(observation), c.Sources[0].ID, "explicit expansion must retain directly supported evidence")
					}
					if c.Case.Slices[0] == "synonyms" {
						ordinary := f.observe(t, c, false, "alias_only_control", false)
						require.Zero(t, ordinary.Score.RelevantAtK)
						organized := f.observe(t, c, true, "alias_only_control", false)
						require.Equal(t, 1.0, organized.Score.RecallAtK)
						f.groupAccuracySources(t, c.Sources[:2])
						grouped := f.observe(t, c, true, "grouped_alternates", false)
						require.Equal(t, 1.0, grouped.Score.RecallAtK)
						require.Len(t, grouped.RankedSources, 1)
						require.ElementsMatch(t, []string{c.Sources[0].ID, c.Sources[1].ID}, grouped.RankedSources[0])
					}
					if c.Case.CaseID == evalharness.RecallAccuracySeedID+":predicate_meaningful_predicate" || c.Case.CaseID == evalharness.RecallAccuracySeedID+":name_common_word" {
						observation := f.observe(t, c, true, "meaningful_query_control", false)
						require.Contains(t, flattenedAccuracySources(observation), c.Sources[0].ID)
					}
				}
			}
		})
	}
}

func flattenedAccuracySources(observation recallAccuracyObservation) []string {
	var ids []string
	for _, slot := range observation.RankedSources {
		ids = append(ids, slot...)
	}
	return ids
}

func (f *recallAccuracyFixture) groupAccuracySources(t *testing.T, sources []evalharness.RecallAccuracySource) {
	t.Helper()
	meanings := map[string]string{}
	var handles []ontology.SourceHandle
	for _, source := range sources {
		meanings[source.Text] = "paraphrase"
		handles = append(handles, f.evidence[source.ID])
	}
	organizer, calls := f.organizer(t, meanings)
	result, err := organizer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	require.Empty(t, result.FailureCode)
	require.NotNil(t, result.Publication)
	require.Positive(t, calls.Load())
}

func TestRecallAccuracyIsolation(t *testing.T) {
	accuracyFrozenLock(t)
	f := newRecallOntologyFixture(t)
	source := evalharness.RecallAccuracyCohort()[0].Sources[0]
	shared := f.evidence(t, 0, source.Text, nil)
	definition := ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "accuracy isolation", Label: "Accuracy isolation"}}
	f.publication(t, definition, f.assignment(t, definition.ID, shared))
	privateIDs := []string{}
	privateContexts := []context.Context{}
	for owner, credential := range f.credentials {
		ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
			TeamID: credential.TeamID, OwnerID: credential.OwnerID, IdentityID: credential.ActorIdentityID, MembershipID: credential.MembershipID,
			CredentialID: &credential.ID, AuthMethod: "api_key", Role: "member", Grants: []string{"read", "write"},
			AllowedSpaces: []domain.MemorySpaceAccess{{ID: credential.MemorySpaceID, Kind: domain.MemorySpacePrivate, Generation: credential.MemorySpaceGeneration}},
		})
		ingest, err := f.knowledge.CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: f.owners[owner],
			SpaceID: credential.MemorySpaceID.String(), SpaceGeneration: credential.MemorySpaceGeneration,
			IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex(source.Text), Evidence: []knowledge.EvidenceInput{{Content: source.Text, ForceInsert: true}}})
		require.NoError(t, err)
		id := ingest.Evidence[0].FragmentID
		privateIDs = append(privateIDs, id)
		privateContexts = append(privateContexts, ctx)
		_, err = f.search.UpsertSearchDocument(ctx, knowledge.UpsertSearchDocumentInput{TeamID: f.team, OwnerProfileID: f.owners[owner],
			SpaceID: credential.MemorySpaceID.String(), SpaceGeneration: credential.MemorySpaceGeneration,
			SourceKind: "evidence", SourceID: id, SourceVersion: 1, DocumentText: source.Text})
		require.NoError(t, err)
	}
	for owner, credential := range f.credentials {
		result, err := f.search.RecallEvidence(privateContexts[owner], RecallEvidenceInput{TeamID: f.team, SpaceID: credential.MemorySpaceID.String(), SpaceKind: "private", Query: "Atlas PostgreSQL", Limit: 10, OrganizationEnabled: true})
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		require.Equal(t, privateIDs[owner], result.Results[0].EvidenceID)
	}
	foreign := createLedgerTeam(t, f.admin, f.rls, "recall accuracy foreign")
	foreignOwner := createLedgerProfile(t, f.admin, f.rls, foreign, "foreign owner")
	foreignIngest, err := f.knowledge.CreateIngestForTest(context.Background(), knowledge.CreateIngestInput{TeamID: foreign, OwnerProfileID: foreignOwner,
		IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex(source.Text), Evidence: []knowledge.EvidenceInput{{Content: source.Text, ForceInsert: true}}})
	require.NoError(t, err)
	foreignID := foreignIngest.Evidence[0].FragmentID
	document, err := f.search.UpsertSearchDocument(context.Background(), knowledge.UpsertSearchDocumentInput{TeamID: foreign, OwnerProfileID: foreignOwner,
		SourceKind: "evidence", SourceID: foreignID, SourceVersion: 1, DocumentText: source.Text})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, f.search, foreign, map[string][]float32{document.SearchDocumentID: {1, 0, 0}})
	for owner := range f.owners {
		for _, known := range append(privateIDs, foreignID) {
			result, err := f.search.RecallEvidence(f.actor(owner, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space,
				Query: "accuracy isolation", QueryEmbedding: []float32{1, 0, 0}, Limit: 10, OrganizationEnabled: true, KnownEvidenceIDs: []string{known}})
			require.NoError(t, err)
			require.Len(t, result.Results, 1)
			require.Equal(t, shared.ID, result.Results[0].EvidenceID)
			require.Empty(t, result.Results[0].EquivalentEvidenceIDs)
		}
	}
	_, err = f.knowledge.RetractEvidence(f.actor(1, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[1],
		EvidenceIDs: []string{shared.ID}, IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex("wrong-owner"), Reason: "wrong-owner preservation control"})
	require.Error(t, err)
	result, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "Atlas PostgreSQL", Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Equal(t, shared.ID, result.Results[0].EvidenceID)
	t.Log(`RECALL_ACCURACY_ISOLATION {"actors":3,"private_owner_reads":3,"shared_foreign_hint_checks":12,"wrong_owner_retraction_rejected":true,"source_preserved":true}`)
}
