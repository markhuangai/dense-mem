//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	recallservice "github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecallOntologyGroupingKnownIDsAndStaleSources(t *testing.T) {
	f := newRecallOntologyFixture(t)
	texts := []string{"Atlas stores data in PostgreSQL.", "PostgreSQL is Atlas's datastore.", "Atlas stores data in PostgreSQL and Redis."}
	handles := []ontology.SourceHandle{}
	meanings := map[string]string{}
	for i, text := range texts {
		handles = append(handles, f.evidence(t, i, text, nil))
		meanings[text] = "storage"
	}
	meanings[texts[2]] = "storage-and-cache"
	service, calls := f.organizer(t, meanings)
	_, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	beforeCalls := calls.Load()
	input := RecallEvidenceInput{TeamID: f.team, Query: "PostgreSQL", Limit: 3, SpaceID: f.space, OrganizationEnabled: true}
	grouped, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, grouped.Results, 2)
	var duplicateID string
	for _, hit := range grouped.Results {
		if len(hit.EquivalentEvidenceIDs) > 0 {
			require.Len(t, hit.EquivalentEvidenceIDs, 1)
			duplicateID = hit.EquivalentEvidenceIDs[0]
		}
	}
	require.NotEmpty(t, duplicateID)
	input.KnownEvidenceIDs = []string{duplicateID}
	known, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, known.Results, 1)
	require.Equal(t, handles[2].ID, known.Results[0].EvidenceID)
	input.KnownEvidenceIDs = []string{uuid.NewString()}
	unknown, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, unknown.Results, 2)
	_, err = f.knowledge.RetractEvidence(f.actor(2, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[2], EvidenceIDs: []string{handles[0].ID}, Reason: "wrong owner regression", IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex("wrong owner")})
	require.Error(t, err)
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handles[0].ID}, Reason: "withdraw source", IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex("withdraw source")})
	require.NoError(t, err)
	input.KnownEvidenceIDs = []string{handles[0].ID}
	stale, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, stale.Results, 2)
	for _, hit := range stale.Results {
		require.Empty(t, hit.EquivalentEvidenceIDs)
		require.NotEqual(t, handles[0].ID, hit.EvidenceID)
	}
	require.Contains(t, degradationCodes(stale.Degradations), "ontology_stale")
	require.Equal(t, beforeCalls, calls.Load(), "Recall cannot invoke organization assessment")
}

func TestRecallOntologyDiscoveryAliasesAndTemporalFallback(t *testing.T) {
	f := newRecallOntologyFixture(t)
	handles := []ontology.SourceHandle{f.evidence(t, 0, "Atlas stores data in PostgreSQL.", nil), f.evidence(t, 1, "PostgreSQL is Atlas's datastore.", nil)}
	service, _ := f.organizer(t, map[string]string{"Atlas stores data in PostgreSQL.": "same", "PostgreSQL is Atlas's datastore.": "same"})
	_, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	input := RecallEvidenceInput{TeamID: f.team, Query: "database", Limit: 3, SpaceID: f.space}
	ordinary, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Empty(t, ordinary.Results)
	input.OrganizationEnabled = true
	discovered, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, discovered.Results, 1)
	require.Len(t, discovered.Results[0].EquivalentEvidenceIDs, 1)
	f.publication(t, ontology.Record{ID: uuid.NewString(), Kind: ontology.EntityClass, Definition: &ontology.Definition{Key: "database-class", Label: "Database class", Aliases: []string{"database"}, BaseEntityKind: "project"}})
	ambiguous, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Empty(t, ambiguous.Results, "ambiguous aliases cannot discover arbitrary topics")
	input.Query = "PostgreSQL"
	for _, knownTime := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid_at", true: "known_at"}[knownTime], func(t *testing.T) {
			now := time.Now().UTC()
			temporal := input
			if knownTime {
				temporal.KnownAt = &now
			} else {
				temporal.ValidAt = &now
			}
			result, err := f.search.RecallEvidence(f.actor(2, "member"), temporal)
			require.NoError(t, err)
			require.Len(t, result.Results, 2)
			require.Contains(t, degradationCodes(result.Degradations), "ontology_temporal_not_supported")
			for _, hit := range result.Results {
				require.Empty(t, hit.EquivalentEvidenceIDs)
			}
		})
	}
	f.override(t, ontology.KeepSeparate, handles)
	separated, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, separated.Results, 2)
	for _, hit := range separated.Results {
		require.Empty(t, hit.EquivalentEvidenceIDs)
	}
}

func TestRecallOntologyOptionalFailureAndRequiredFailureRemainDistinct(t *testing.T) {
	f := newRecallOntologyFixture(t)
	f.evidence(t, 0, "Atlas uses PostgreSQL.", nil)
	input := RecallEvidenceInput{TeamID: f.team, Query: "PostgreSQL", Limit: 1, SpaceID: f.space, OrganizationEnabled: true}
	reader := f.search.recall.ontology
	f.search.recall.WithOntology(func() OntologyReader {
		return func(ctx context.Context, tx *gorm.DB, team string, input ontology.RecallReadInput) (ontology.RecallOrganization, error) {
			return ontology.RecallOrganization{}, tx.Exec("SELECT deliberately_missing_ontology_function()").Error
		}
	})
	result, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Contains(t, degradationCodes(result.Degradations), "ontology_unavailable")
	f.search.recall.WithOntology(reader)
	ctx, cancel := context.WithCancel(f.actor(2, "member"))
	cancel()
	result, err = f.search.RecallEvidence(ctx, input)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result)
	_, err = reader()(f.actor(2, "member"), nil, f.team, ontology.RecallReadInput{Limit: 1})
	require.Error(t, err)
	foreignTeam := createLedgerTeam(t, f.admin, f.rls, "foreign ontology recall")
	result, err = f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: foreignTeam, Query: "PostgreSQL", OrganizationEnabled: true})
	require.True(t, result == nil || len(result.Results) == 0)
	if err != nil {
		require.False(t, errors.Is(err, context.Canceled))
	}
}

func degradationCodes(values []recallcontract.RecallDegradationResult) []string {
	codes := []string{}
	for _, value := range values {
		codes = append(codes, value.Code)
	}
	return codes
}

func TestRecallOntologyPunctuatedKeysAndAliases(t *testing.T) {
	f := newRecallOntologyFixture(t)
	handle := f.evidence(t, 0, "Atlas retains durable records.", nil)
	for _, name := range []string{"C++", "Node.js", "C#"} {
		definition := ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: name, Label: name, Aliases: []string{name + " platform"}}}
		f.publication(t, definition, f.assignment(t, definition.ID, handle))
		for _, query := range []string{name, "What is " + name + "?", "Where is (" + name + " platform)?"} {
			input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: query, Limit: 3}
			ordinary, err := f.search.RecallEvidence(f.actor(2, "member"), input)
			require.NoError(t, err)
			require.Empty(t, ordinary.Results)
			input.OrganizationEnabled = true
			discovered, err := f.search.RecallEvidence(f.actor(2, "member"), input)
			require.NoError(t, err)
			require.Len(t, discovered.Results, 1)
			require.Equal(t, handle.ID, discovered.Results[0].EvidenceID)
		}
		definition.Version = 1
		definition.Definition.Key = name + " language"
		definition.Definition.Aliases = []string{name}
		f.publication(t, definition, f.assignment(t, definition.ID, handle))
		alias, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "What is " + name + "?", Limit: 3, OrganizationEnabled: true})
		require.NoError(t, err)
		require.Len(t, alias.Results, 1)
		require.Equal(t, handle.ID, alias.Results[0].EvidenceID)
	}
}

func TestRecallOntologyFeedbackAlternatesDoNotInflateResults(t *testing.T) {
	f := newRecallOntologyFixture(t)
	handles := []ontology.SourceHandle{f.evidence(t, 0, "Atlas stores data in PostgreSQL.", nil), f.evidence(t, 1, "PostgreSQL is Atlas's datastore.", nil)}
	producer, _ := f.organizer(t, map[string]string{"Atlas stores data in PostgreSQL.": "same", "PostgreSQL is Atlas's datastore.": "same"})
	_, err := producer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	result, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "PostgreSQL", Limit: 1, OrganizationEnabled: true})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	hit := result.Results[0]
	refs := recallservice.FeedbackResultRefs(&recallcontract.RecallResult{Results: []recallcontract.RecallResultItem{{EvidenceID: hit.EvidenceID, EquivalentEvidenceIDs: hit.EquivalentEvidenceIDs, Rank: hit.Rank}}})
	team, owner, space := uuid.MustParse(f.team), uuid.MustParse(f.owners[2]), uuid.MustParse(f.space)
	recallID := "rec_" + uuid.NewString()
	store := NewFeedbackStore(f.app, f.rls)
	require.NoError(t, store.RecordSnapshot(f.actor(2, "member"), domain.RecallFeedbackEvent{RecallID: recallID, TeamID: &team, ProfileID: &owner, SpaceID: &space, SpaceGeneration: f.generation, ResultRefs: refs}))
	saved, err := store.Get(f.actor(2, "member"), recallID)
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.Equal(t, 1, saved.ResultCount)
	require.Len(t, saved.ResultRefs, 2)
	require.Equal(t, hit.EvidenceID, saved.ResultRefs[1].EquivalentTo)
	feedback := recallservice.NewRecallFeedbackEventService(store, nil, nil)
	require.NoError(t, feedback.RecordRecallFeedback(f.actor(2, "member"), domain.RecallFeedbackSubmission{RecallID: recallID, Quality: "high", Used: true, AnswerSupported: true, IrrelevantRefs: []domain.RecallFeedbackJudgedResultRef{{Type: domain.RecallFeedbackResultTypeEvidence, ID: hit.EquivalentEvidenceIDs[0], Rank: 1}}}))
	saved, err = store.Get(f.actor(2, "member"), recallID)
	require.NoError(t, err)
	require.Equal(t, 1, saved.ResultCount)
	require.Equal(t, hit.EquivalentEvidenceIDs[0], saved.IrrelevantRefs[0].ID)
}

func TestRecallOntologyRelationshipDiscoveryPreservesSemanticGroups(t *testing.T) {
	f := newRecallOntologyFixture(t)
	subject := createSemanticEntity(t, context.Background(), f.knowledge, f.team, f.owners[0], "person", "Morgan")
	object := createSemanticEntity(t, context.Background(), f.knowledge, f.team, f.owners[0], "project", "Atlas")
	handles, evidence := []ontology.SourceHandle{}, []string{}
	groupKey := ""
	for owner := range 2 {
		ingest := createSemanticIngest(t, context.Background(), f.knowledge, f.team, f.owners[owner], uuid.NewString(), "Morgan works on Atlas.")
		decision := applySemanticDecision(t, f.actor(owner, "member"), f.knowledge, ApplyRelationshipDecisionInput{
			TeamID: f.team, OwnerProfileID: f.owners[owner], IngestID: ingest.IngestID,
			SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
			Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: uuid.NewString(), SpanStart: 0, SpanEnd: len("Morgan works on Atlas."), Authority: "primary"}})
		require.NotNil(t, decision.Relationship)
		if owner == 0 {
			groupKey = decision.Relationship.SemanticGroupKey
		} else {
			require.Equal(t, groupKey, decision.Relationship.SemanticGroupKey)
		}
		handle := ontology.SourceHandle{Kind: ontology.RelationshipSource, ID: decision.Relationship.RelationshipID, Version: int64(decision.Relationship.Version)}
		handles = append(handles, handle)
		evidence = append(evidence, ingest.Evidence[0].FragmentID)
		_, err := f.search.UpsertSearchDocument(context.Background(), knowledge.UpsertSearchDocumentInput{TeamID: f.team, OwnerProfileID: f.owners[owner], SourceKind: "relationship", SourceID: handle.ID, SourceVersion: handle.Version, DocumentText: "Morgan works on Atlas."})
		require.NoError(t, err)
	}
	producer, calls := f.organizer(t, nil)
	_, err := producer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	priorCalls := calls.Load()
	input := RecallRelationshipsInput{TeamID: f.team, SpaceID: f.space, Query: "database", Limit: 3}
	ordinary, err := f.search.RecallRelationships(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Empty(t, ordinary.Results)
	input.OrganizationEnabled = true
	discovered, err := f.search.RecallRelationships(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, discovered.Results, 1)
	hit := discovered.Results[0]
	require.Equal(t, 1, hit.Rank)
	require.Equal(t, groupKey, hit.SemanticGroupKey)
	require.ElementsMatch(t, []string{handles[0].ID, handles[1].ID}, append([]string{hit.RelationshipID}, hit.EquivalentRelationshipIDs...))
	input.KnownRelationshipIDs = []string{hit.RelationshipID}
	known, err := f.search.RecallRelationships(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Empty(t, known.Results)
	input.KnownRelationshipIDs = nil
	for owner := range 2 {
		_, err := f.knowledge.RetractEvidence(f.actor(owner, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[owner], EvidenceIDs: []string{evidence[owner]}, Reason: "withdraw Relationship discovery support", IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex(evidence[owner])})
		require.NoError(t, err)
	}
	stale, err := f.search.RecallRelationships(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Empty(t, stale.Results)
	require.Contains(t, degradationCodes(stale.Degradations), "ontology_stale")
	require.Equal(t, priorCalls, calls.Load(), "Relationship Recall must not assess organization")
}

func TestRecallOntologyBatchKeepsActivePredicateVersions(t *testing.T) {
	f := newRecallOntologyFixture(t)
	object := createSemanticEntity(t, context.Background(), f.knowledge, f.team, f.owners[0], "project", "Atlas")
	for index, subjectKind := range []string{"person", "place"} {
		predicate, err := f.knowledge.EnsureSemanticReviewPredicateCandidate(f.actor(index, "member"), knowledge.EnsureSemanticPredicateCandidateInput{TeamID: f.team, OwnerProfileID: f.owners[index], Predicate: "works_on", RelationshipKind: "state", SubjectKind: subjectKind, ObjectKind: "project"})
		require.NoError(t, err)
		subject := createSemanticEntity(t, context.Background(), f.knowledge, f.team, f.owners[index], subjectKind, "Contributor "+subjectKind)
		ingest := createSemanticIngest(t, context.Background(), f.knowledge, f.team, f.owners[index], uuid.NewString(), "Contributor works on Atlas.")
		applySemanticDecision(t, f.actor(index, "member"), f.knowledge, ApplyRelationshipDecisionInput{TeamID: f.team, OwnerProfileID: f.owners[index], IngestID: ingest.IngestID, SubjectEntityID: subject.EntityID, PredicateKey: predicate.PredicateKey, PredicateVersion: predicate.Version, ObjectEntityID: object.EntityID, Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: uuid.NewString(), SpanStart: 0, SpanEnd: len("Contributor works on Atlas."), Authority: "primary"}})
		definition := ontology.Record{ID: uuid.NewString(), Kind: ontology.PredicateConcept, Definition: &ontology.Definition{Key: "registry " + subjectKind, Label: "Registry " + subjectKind}}
		f.publication(t, definition, f.assignment(t, definition.ID, ontology.SourceHandle{Kind: ontology.PredicateSource, ID: predicate.PredicateKey, Version: int64(predicate.Version)}))
	}
	page, err := f.ontology.ListRecords(f.actor(2, "member"), f.team, ontology.AssignmentKind, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Records, 2)
	require.NotEqual(t, page.Records[0].Assignment.Source.Version, page.Records[1].Assignment.Source.Version)
	for _, record := range page.Records {
		require.True(t, record.Current, "another active version of the same predicate cannot stale this assignment")
	}
}
