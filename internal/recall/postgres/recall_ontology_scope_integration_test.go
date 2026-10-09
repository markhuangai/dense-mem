//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecallOntologyMigratedAliasRemainsIneligibleWithCurrentProjection(t *testing.T) {
	f := newRecallOntologyFixture(t)
	ctx := context.Background()
	text := "Atlas stores data in PostgreSQL."
	handles := []ontology.SourceHandle{}
	for owner := range 2 {
		ingest, err := f.knowledge.CreateIngestForTest(ctx, knowledge.CreateIngestInput{
			TeamID: f.team, OwnerProfileID: f.owners[owner],
			IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex(uuid.NewString()),
			Evidence: []knowledge.EvidenceInput{{Content: text}},
		})
		require.NoError(t, err)
		handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: ingest.Evidence[0].FragmentID, Version: 1}
		document, err := f.search.UpsertSearchDocument(ctx, knowledge.UpsertSearchDocumentInput{
			TeamID: f.team, OwnerProfileID: f.owners[owner], SourceKind: "evidence",
			SourceID: handle.ID, SourceVersion: 1, DocumentText: text,
		})
		require.NoError(t, err)
		completeSearchDocumentsForTest(t, f.search, f.team, map[string][]float32{document.SearchDocumentID: {1, 0, 0}})
		handles = append(handles, handle)
	}
	alternateText := "PostgreSQL is Atlas's datastore."
	handles = append(handles, f.evidence(t, 0, alternateText, nil))
	var beforeAlias time.Time
	require.NoError(t, f.admin.Raw(`SELECT clock_timestamp()`).Row().Scan(&beforeAlias))
	require.NoError(t, f.rls.WithSystemTx(ctx, f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO evidence_exact_aliases (
			team_id, alias_fragment_id, alias_owner_profile_id,
			canonical_fragment_id, canonical_owner_profile_id
		) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid)`,
			f.team, handles[1].ID, f.owners[1], handles[0].ID, f.owners[0]).Error
	}))
	var state string
	require.NoError(t, f.rls.WithTeamTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT search_state FROM search_documents WHERE team_id=?::uuid AND source_kind='evidence' AND source_id=?::uuid`, f.team, handles[1].ID).Row().Scan(&state)
	}))
	require.Equal(t, "current", state)
	producer, calls := f.organizer(t, map[string]string{text: "same", alternateText: "same"})
	_, err := producer.Organize(ctx, f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	beforeCalls := calls.Load()
	input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "database", Limit: 10, OrganizationEnabled: true}
	for _, known := range [][]string{nil, {handles[1].ID}} {
		input.KnownEvidenceIDs = known
		result, err := f.search.RecallEvidence(f.actor(2, "member"), input)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		hit := result.Results[0]
		require.NotEqual(t, handles[1].ID, hit.EvidenceID)
		require.NotContains(t, hit.EquivalentEvidenceIDs, handles[1].ID)
		require.ElementsMatch(t, []string{handles[0].ID, handles[2].ID}, append([]string{hit.EvidenceID}, hit.EquivalentEvidenceIDs...))
	}
	input.KnownEvidenceIDs = []string{handles[0].ID}
	known, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Empty(t, known.Results)
	input.KnownEvidenceIDs, input.Query, input.KnownAt = nil, "PostgreSQL", &beforeAlias
	historical, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, historical.Results, 3)
	ids := []string{}
	for _, hit := range historical.Results {
		ids = append(ids, hit.EvidenceID)
		require.Empty(t, hit.EquivalentEvidenceIDs)
	}
	require.Contains(t, ids, handles[1].ID)
	require.Contains(t, degradationCodes(historical.Degradations), "ontology_temporal_not_supported")
	require.Equal(t, beforeCalls, calls.Load())
}

func TestRecallOntologyPrefetchedGroupsReadNewEvidenceAndReauthorize(t *testing.T) {
	f := newRecallOntologyFixture(t)
	texts := []string{"Atlas uses PostgreSQL.", "PostgreSQL stores Atlas data.", "Boreal uses PostgreSQL.", "PostgreSQL stores Boreal data."}
	handles, ids := []ontology.SourceHandle{}, []string{}
	meanings := map[string]string{}
	for index, text := range texts {
		handle := f.evidence(t, index%2, text, nil)
		handles, ids = append(handles, handle), append(ids, handle.ID)
		meanings[text] = []string{"atlas", "boreal"}[index/2]
	}
	producer, _ := f.organizer(t, meanings)
	_, err := producer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	reader, ctx := f.ontology.NewRecallReader(), f.actor(2, "member")
	require.NoError(t, f.rls.WithTeamReadOnlyRepeatableTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		initial, err := reader(ctx, tx, f.team, ontology.RecallReadInput{SpaceID: f.space, Query: "unmatched", CandidateEvidenceIDs: ids[:2], Limit: 60})
		require.NoError(t, err)
		require.Empty(t, initial.Sources)
		result, err := reader(ctx, tx, f.team, ontology.RecallReadInput{SpaceID: f.space, EvidenceIDs: ids, Limit: 60})
		require.NoError(t, err)
		require.Len(t, result.Groups, 2)
		members := []string{}
		for _, group := range result.Groups {
			require.Len(t, group.Members, 2)
			members = append(members, group.Members...)
		}
		require.ElementsMatch(t, ids, members)
		actor, ok := requestctx.ActorFromContext(ctx)
		require.True(t, ok)
		actor.AllowedSpaces = nil
		restricted := requestctx.WithActor(context.Background(), actor)
		result, err = reader(restricted, tx, f.team, ontology.RecallReadInput{SpaceID: f.space, EvidenceIDs: ids, Limit: 60})
		require.ErrorIs(t, err, ontology.ErrUnauthorized)
		require.Empty(t, result.Groups)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = reader(canceled, tx, f.team, ontology.RecallReadInput{SpaceID: f.space, EvidenceIDs: ids, Limit: 60})
		require.ErrorIs(t, err, context.Canceled)
		return nil
	}))
}

func TestRecallOntologyPrivateForeignAndOldGenerationSources(t *testing.T) {
	f := newRecallOntologyFixture(t)
	texts := []string{"Atlas stores data in PostgreSQL.", "PostgreSQL stores Atlas's data."}
	handles := []ontology.SourceHandle{f.evidence(t, 0, texts[0], nil), f.evidence(t, 1, texts[1], nil)}
	producer, _ := f.organizer(t, map[string]string{texts[0]: "same", texts[1]: "same"})
	_, err := producer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	private := &domain.Credential{ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "private source", KeyHash: "synthetic-" + uuid.NewString(), KeyPrefix: uuid.NewString()[:24], KeySuffix: "test", Scopes: []string{"read", "write"}, MemoryBinding: domain.CredentialBindingCredentialPrivate}
	require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), private))
	privateCtx := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{{ID: private.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate, Generation: private.MemorySpaceGeneration}})
	privateIngest, err := f.knowledge.CreateIngestForTest(privateCtx, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: private.OwnerID.String(), SpaceID: private.MemorySpaceID.String(), SpaceGeneration: private.MemorySpaceGeneration, IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex("private"), Evidence: []knowledge.EvidenceInput{{Content: texts[0], ForceInsert: true}}})
	require.NoError(t, err)
	privateID := privateIngest.Evidence[0].FragmentID
	_, err = f.search.UpsertSearchDocument(privateCtx, knowledge.UpsertSearchDocumentInput{TeamID: f.team, OwnerProfileID: private.OwnerID.String(), SpaceID: private.MemorySpaceID.String(), SpaceGeneration: private.MemorySpaceGeneration, SourceKind: "evidence", SourceID: privateID, SourceVersion: 1, DocumentText: texts[0]})
	require.NoError(t, err)
	foreign := createLedgerTeam(t, f.admin, f.rls, "foreign source team")
	foreignOwner := createLedgerProfile(t, f.admin, f.rls, foreign, "foreign source owner")
	foreignIngest, err := f.knowledge.CreateIngestForTest(context.Background(), knowledge.CreateIngestInput{TeamID: foreign, OwnerProfileID: foreignOwner, IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex("foreign"), Evidence: []knowledge.EvidenceInput{{Content: texts[0], ForceInsert: true}}})
	require.NoError(t, err)
	for _, known := range []string{privateID, foreignIngest.Evidence[0].FragmentID} {
		result, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "PostgreSQL", Limit: 10, OrganizationEnabled: true, KnownEvidenceIDs: []string{known}})
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		require.NotEqual(t, known, result.Results[0].EvidenceID)
		require.NotContains(t, result.Results[0].EquivalentEvidenceIDs, known)
		require.Len(t, result.Results[0].EquivalentEvidenceIDs, 1)
	}
	privateResult, err := f.search.RecallEvidence(privateCtx, RecallEvidenceInput{TeamID: f.team, SpaceID: private.MemorySpaceID.String(), SpaceKind: "credential_private", Query: "PostgreSQL", Limit: 10, OrganizationEnabled: true})
	require.NoError(t, err)
	require.Len(t, privateResult.Results, 1)
	require.Equal(t, privateID, privateResult.Results[0].EvidenceID)
	require.Empty(t, privateResult.Results[0].EquivalentEvidenceIDs)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND id=?::uuid`, f.team, f.space).Error
	}))
	f.generation++
	fresh := f.evidence(t, 0, "Atlas retains PostgreSQL in its new generation.", nil)
	result, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "PostgreSQL", Limit: 10, OrganizationEnabled: true, KnownEvidenceIDs: []string{handles[0].ID}})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Equal(t, fresh.ID, result.Results[0].EvidenceID)
	require.Empty(t, result.Results[0].EquivalentEvidenceIDs)
}

func TestRecallOntologyHydrationUsesOneSnapshotAcrossRetraction(t *testing.T) {
	f := newRecallOntologyFixture(t)
	texts := []string{"Atlas stores data in PostgreSQL.", "PostgreSQL stores Atlas's data."}
	handles := []ontology.SourceHandle{f.evidence(t, 0, texts[0], nil), f.evidence(t, 1, texts[1], nil)}
	producer, _ := f.organizer(t, map[string]string{texts[0]: "same", texts[1]: "same"})
	_, err := producer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	reader := f.search.recall.ontology
	var once sync.Once
	f.search.recall.WithOntology(func() OntologyReader {
		read := reader()
		return func(ctx context.Context, tx *gorm.DB, team string, input ontology.RecallReadInput) (ontology.RecallOrganization, error) {
			result, err := read(ctx, tx, team, input)
			if err == nil && len(input.EvidenceIDs) > 0 {
				once.Do(func() {
					_, err := f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handles[0].ID}, Reason: "publication and hydration race", IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex("snapshot race")})
					require.NoError(t, err)
				})
			}
			return result, err
		}
	})
	input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "PostgreSQL", Limit: 10, OrganizationEnabled: true}
	before, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, before.Results, 1)
	require.Len(t, before.Results[0].EquivalentEvidenceIDs, 1, "in-flight grouping and hydration must agree on their snapshot")
	after, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, after.Results, 1)
	require.Equal(t, handles[1].ID, after.Results[0].EvidenceID)
	require.Empty(t, after.Results[0].EquivalentEvidenceIDs)
	require.Contains(t, degradationCodes(after.Degradations), "ontology_stale")
}

func TestRecallOntologySourceRevisionCorrectionAndDefinitionStaleness(t *testing.T) {
	f := newRecallOntologyFixture(t)
	key := "source-" + uuid.NewString()
	createRevision := func(token, previous, text string) ontology.SourceHandle {
		result, err := f.knowledge.CreateIngestForTest(context.Background(), knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: f.owners[0], IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex(token), Evidence: []knowledge.EvidenceInput{{Content: text, ForceInsert: true, SourceKey: key, SourceRevisionToken: token, ExpectedPreviousRevisionToken: previous}}})
		require.NoError(t, err)
		fragment := result.Evidence[0]
		_, err = f.search.UpsertSearchDocument(context.Background(), knowledge.UpsertSearchDocumentInput{TeamID: f.team, OwnerProfileID: f.owners[0], SourceKind: "evidence", SourceID: fragment.FragmentID, SourceVersion: 1, DocumentText: text})
		require.NoError(t, err)
		return ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: fragment.FragmentID, Version: 1}
	}
	old := createRevision("1", "", "Atlas stores data in PostgreSQL.")
	other := f.evidence(t, 1, "PostgreSQL stores Atlas's data.", nil)
	producer, _ := f.organizer(t, map[string]string{"Atlas stores data in PostgreSQL.": "same", "PostgreSQL stores Atlas's data.": "same"})
	_, err := producer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: []ontology.SourceHandle{old, other}})
	require.NoError(t, err)
	reader := f.search.recall.ontology
	var once sync.Once
	var current ontology.SourceHandle
	f.search.recall.WithOntology(func() OntologyReader {
		read := reader()
		return func(ctx context.Context, tx *gorm.DB, team string, input ontology.RecallReadInput) (ontology.RecallOrganization, error) {
			result, err := read(ctx, tx, team, input)
			if err == nil && len(input.EvidenceIDs) > 0 {
				once.Do(func() {
					current = createRevision("2", "1", "Atlas stores PostgreSQL data and encrypts backups.")
				})
			}
			return result, err
		}
	})
	before, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "PostgreSQL", Limit: 10, OrganizationEnabled: true})
	require.NoError(t, err)
	require.Len(t, before.Results, 1)
	require.ElementsMatch(t, []string{old.ID, other.ID}, append([]string{before.Results[0].EvidenceID}, before.Results[0].EquivalentEvidenceIDs...))
	require.NotEmpty(t, current.ID)
	result, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "PostgreSQL", Limit: 10, OrganizationEnabled: true, KnownEvidenceIDs: []string{old.ID}})
	require.NoError(t, err)
	require.Len(t, result.Results, 2)
	ids := []string{}
	for _, hit := range result.Results {
		ids = append(ids, hit.EvidenceID)
		if hit.EvidenceID == current.ID {
			require.Contains(t, hit.Context, "encrypts backups")
		}
		require.Empty(t, hit.EquivalentEvidenceIDs)
	}
	require.ElementsMatch(t, []string{current.ID, other.ID}, ids)
	require.Contains(t, degradationCodes(result.Degradations), "ontology_stale")
	page, err := f.ontology.ListRecords(context.Background(), f.team, ontology.Topic, "", 20)
	require.NoError(t, err)
	require.NotEmpty(t, page.Records)
	definition := page.Records[0].Record
	definition.Definition.Description = "Revised topic vocabulary"
	definition.Sources = nil
	f.publication(t, definition)
	alias, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "database", Limit: 10, OrganizationEnabled: true})
	require.NoError(t, err)
	require.Empty(t, alias.Results, "stale assignments cannot discover source IDs")
}

func TestRecallOntologyReaderResetsBetweenTransactions(t *testing.T) {
	f := newRecallOntologyFixture(t)
	texts := []string{"Atlas stores data in PostgreSQL.", "PostgreSQL stores Atlas's data."}
	handles := []ontology.SourceHandle{f.evidence(t, 0, texts[0], nil), f.evidence(t, 1, texts[1], nil)}
	producer, _ := f.organizer(t, map[string]string{texts[0]: "same", texts[1]: "same"})
	_, err := producer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	reader := f.ontology.NewRecallReader()
	read := func() ontology.RecallOrganization {
		t.Helper()
		var result ontology.RecallOrganization
		ctx := f.actor(2, "member")
		require.NoError(t, f.rls.WithTeamReadOnlyRepeatableTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
			_, err := reader(ctx, tx, f.team, ontology.RecallReadInput{SpaceID: f.space, Query: "database", Limit: 60})
			if err != nil {
				return err
			}
			result, err = reader(ctx, tx, f.team, ontology.RecallReadInput{SpaceID: f.space, EvidenceIDs: []string{handles[0].ID, handles[1].ID}, Limit: 60})
			return err
		}))
		return result
	}
	require.Len(t, read().Groups, 1)
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handles[0].ID}, Reason: "snapshot reader reuse regression", IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex("reader reuse")})
	require.NoError(t, err)
	after := read()
	require.Empty(t, after.Groups)
	require.Equal(t, "ontology_stale", after.Degradation)
	foreign := createLedgerTeam(t, f.admin, f.rls, "foreign reused reader")
	require.NoError(t, f.rls.WithTeamReadOnlyRepeatableTx(f.actor(2, "member"), f.app, f.team, func(tx *gorm.DB) error {
		result, err := reader(f.actor(2, "member"), tx, foreign, ontology.RecallReadInput{Query: "database", Limit: 60})
		require.ErrorIs(t, err, ontology.ErrUnauthorized)
		require.Empty(t, result.Sources)
		require.Empty(t, result.Groups)
		return nil
	}))
}

func TestRecallOntologyDiscoveryHydrationEnforcesEligibilityAndState(t *testing.T) {
	f := newRecallOntologyFixture(t)
	text := "Morgan works on Atlas."
	active := f.evidence(t, 0, text, nil)
	pending := f.evidence(t, 1, "Atlas has pending search state.", nil)
	failed := f.evidence(t, 1, "Atlas has failed search state.", nil)
	retracted := f.evidence(t, 0, "Withdrawn Atlas source.", nil)
	deletionOnly := f.evidence(t, 0, "Deletion-only Atlas source.", map[string]any{"conflict_resolution_deletion_only": true})

	missing, err := f.knowledge.CreateIngestForTest(context.Background(), knowledge.CreateIngestInput{
		TeamID: f.team, OwnerProfileID: f.owners[1], Evidence: []knowledge.EvidenceInput{{Content: "Atlas source without a search projection."}},
	})
	require.NoError(t, err)
	missingHandle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: missing.Evidence[0].FragmentID, Version: 1}
	definition := ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "database", Label: "Database"}}
	records := []ontology.Record{definition}
	for _, handle := range []ontology.SourceHandle{active, pending, failed, retracted, deletionOnly, missingHandle} {
		records = append(records, f.assignment(t, definition.ID, handle))
	}
	f.publication(t, records...)
	subject := createSemanticEntity(t, context.Background(), f.knowledge, f.team, f.owners[0], "person", "Morgan")
	object := createSemanticEntity(t, context.Background(), f.knowledge, f.team, f.owners[0], "project", "Atlas")
	var ingestID string
	require.NoError(t, f.admin.Raw(`SELECT ingest_id::text FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid`, f.team, active.ID).Row().Scan(&ingestID))
	decision := applySemanticDecision(t, f.actor(0, "member"), f.knowledge, ApplyRelationshipDecisionInput{
		TeamID: f.team, OwnerProfileID: f.owners[0], IngestID: ingestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{FragmentID: active.ID, SourceGroupKey: uuid.NewString(), SpanStart: 0, SpanEnd: len(text), Authority: "primary"},
	})
	require.NotNil(t, decision.Relationship)
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE search_documents SET search_state='pending' WHERE team_id=?::uuid AND source_kind='evidence' AND source_id=?::uuid`, f.team, pending.ID).Error
	}))
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE search_documents SET search_state='failed' WHERE team_id=?::uuid AND source_kind='evidence' AND source_id=?::uuid`, f.team, failed.ID).Error
	}))
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{
		TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{retracted.ID}, Reason: "source withdrawn",
		IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex(retracted.ID),
	})
	require.NoError(t, err)

	for _, known := range [][]string{nil, {decision.Relationship.RelationshipID}} {
		input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, SpaceKind: "team_shared", Query: "database", Limit: 10, OrganizationEnabled: true, KnownRelationshipIDs: known}
		result, err := f.search.RecallEvidence(f.actor(2, "member"), input)
		require.NoError(t, err)
		require.Len(t, result.Results, 3)
		require.Equal(t, "failed", result.SearchState)
		byID := map[string]RecallEvidenceHit{}
		for _, hit := range result.Results {
			byID[hit.EvidenceID] = hit
		}
		require.Contains(t, byID, active.ID)
		require.Contains(t, byID, pending.ID)
		require.Contains(t, byID, failed.ID)
		require.NotContains(t, byID, retracted.ID)
		require.NotContains(t, byID, deletionOnly.ID)
		require.NotContains(t, byID, missingHandle.ID)
		require.Equal(t, "pending", byID[pending.ID].SearchState)
		require.Equal(t, "failed", byID[failed.ID].SearchState)
		if len(known) == 0 {
			require.Contains(t, byID[active.ID].RelationshipIDs, decision.Relationship.RelationshipID)
		} else {
			require.NotContains(t, byID[active.ID].RelationshipIDs, decision.Relationship.RelationshipID)
		}
	}
}
