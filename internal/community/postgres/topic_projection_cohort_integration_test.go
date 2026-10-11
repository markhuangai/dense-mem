//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	recallapp "github.com/markhuangai/dense-mem/internal/recall"
	recallpg "github.com/markhuangai/dense-mem/internal/recall/postgres"
	searchpg "github.com/markhuangai/dense-mem/internal/search/postgres"
	"github.com/stretchr/testify/require"
)

func (f *topicProjectionFixture) recallService(t *testing.T) recallapp.RecallService {
	t.Helper()
	insertSearchTestContract(t, f.adminDB, f.rls, "topic-recall", 3, "exact", "")
	search := searchpg.NewStore(f.appDB, f.rls)
	reader := recallpg.NewStore(f.appDB, f.rls, search, nil, nil).WithOntology(func() recallpg.OntologyReader { return f.ontology.NewRecallReader() })
	return recallapp.NewRecallService(recallapp.RecallDependencies{Search: reader, Communities: f.store, CommunityConfig: topicFixtureConfig{}, OntologyConfig: topicFixtureConfig{}})
}

func TestCommunityTopicFrozenOrganizationCohortThroughRecall(t *testing.T) {
	f := newTopicProjectionFixture(t)
	cohort := evalharness.OntologyOrganizationCohort()
	encoded, err := json.Marshal(cohort)
	require.NoError(t, err)
	digest := sha256.Sum256(encoded)
	var lock struct {
		CohortSHA256 string `json:"cohort_sha256"`
	}
	data, err := os.ReadFile("../../../tests/eval/baselines/ontology_organization_v1.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &lock))
	require.Equal(t, lock.CohortSHA256, "sha256:"+hex.EncodeToString(digest[:]))
	var sourceLock struct {
		GeneratorSHA256 string `json:"generator_sha256"`
	}
	sourceLockData, err := os.ReadFile("../../../tests/eval/source_locks/ontology_organization_v1.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(sourceLockData, &sourceLock))
	generator, err := os.ReadFile("../../evalharness/organization_cohort.go")
	require.NoError(t, err)
	generatorDigest := sha256.Sum256(generator)
	require.Equal(t, sourceLock.GeneratorSHA256, "sha256:"+hex.EncodeToString(generatorDigest[:]))
	semantic := knowledge.NewStore(f.appDB, f.rls, knowledge.ConflictRuntimeConfig{})
	expectedEvidence, expectedFacts := map[string]bool{}, map[string]string{}
	expectedTexts := map[string]string{}
	sourceCount := 0
	for _, testCase := range cohort {
		handles := []ontology.SourceHandle{}
		for i, source := range testCase.Sources {
			owner := f.ownerA
			if i%2 == 1 {
				owner = f.ownerB
			}
			ingest, err := semantic.CreateIngestForTest(context.Background(), knowledge.CreateIngestInput{
				TeamID: f.teamID, OwnerProfileID: owner, IdempotencyKey: uuid.NewString(), RequestHash: "sha256:" + hex.EncodeToString(digest[:]),
				Evidence: []knowledge.EvidenceInput{{FragmentID: uuid.NewString(), Content: source.Text, ForceInsert: true, Metadata: map[string]any{"actor": source.Actor, "polarity": source.Polarity, "time": source.Time, "qualification": source.Qualification}}},
			})
			require.NoError(t, err)
			fragment := ingest.Evidence[0].FragmentID
			expectedEvidence[fragment] = true
			expectedTexts[fragment] = source.Text
			handles = append(handles, ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: fragment, Version: 1})
			subject := createSemanticEntity(t, context.Background(), semantic, f.teamID, owner, "project", source.Actor)
			for _, fact := range source.FactIDs {
				object := createSemanticEntity(t, context.Background(), semantic, f.teamID, owner, "product", testCase.ID+" "+fact)
				decision := applySemanticDecision(t, context.Background(), semantic, ApplyRelationshipDecisionInput{
					TeamID: f.teamID, OwnerProfileID: owner, IngestID: ingest.IngestID, SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
					Polarity: source.Polarity, ScopeKey: source.ID,
					Support: &EvidenceSupportInput{FragmentID: fragment, SourceGroupKey: source.ID, SpanEnd: len(source.Text), Authority: "primary"},
				})
				expectedFacts[decision.Relationship.RelationshipID] = fragment
			}
			sourceCount++
		}
		f.topic(t, testCase.ID, handles)
	}
	require.Equal(t, 19, sourceCount)
	f.drain(t, 20)
	service := f.recallService(t)
	zero, ten, twenty := 0, 10, 20
	result, err := service.Recall(f.actor(f.reader), recallapp.RecallRequest{Query: "marker", Limit: 1, RelationshipLimit: &zero, CommunityLimit: &ten, CommunityRelationshipLimit: &twenty})
	require.NoError(t, err)
	require.Len(t, result.RelatedCommunities, 9)
	seenEvidence, seenFacts := map[string]bool{}, map[string]bool{}
	for _, evidence := range result.Results {
		require.True(t, expectedEvidence[evidence.EvidenceID], "Bad@K: unrelated evidence")
		require.Contains(t, evidence.Context, expectedTexts[evidence.EvidenceID])
		seenEvidence[evidence.EvidenceID] = true
		for relationship, fragment := range expectedFacts {
			if fragment == evidence.EvidenceID {
				seenFacts[relationship] = true
			}
		}
	}
	for _, record := range result.RelatedCommunities {
		for _, relationship := range record.CommunityRelationships {
			fragment, exists := expectedFacts[relationship.RelationshipID]
			require.True(t, exists, "Bad@K: unrelated fact")
			require.Contains(t, relationship.EvidenceIDs, fragment)
			seenFacts[relationship.RelationshipID] = true
			for _, evidence := range relationship.EvidenceIDs {
				require.True(t, expectedEvidence[evidence])
				seenEvidence[evidence] = true
			}
		}
	}
	require.Len(t, seenFacts, len(expectedFacts), "community consolidated distinct source facts")
	require.Equal(t, expectedEvidence, seenEvidence)
	t.Logf("cohort_sha256=%s cases=9 sources=19 fact_preservation=1 source_preservation=1 false_consolidation=0 bad_at_k=0 actual_recall=true", lock.CohortSHA256)
}
