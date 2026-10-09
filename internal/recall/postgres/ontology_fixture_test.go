//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypostgres "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type recallOntologyFixture struct {
	admin, app  *gorm.DB
	rls         *storage.RLS
	team, space string
	generation  int64
	owners      []string
	search      *searchFixtureStore
	knowledge   *knowledgepostgres.Store
	ontology    *ontologypostgres.Store
}

func newRecallOntologyFixture(t testing.TB) *recallOntologyFixture {
	t.Helper()
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	f := &recallOntologyFixture{admin: admin, app: app, rls: rls}
	f.team = createLedgerTeam(t, admin, rls, "ontology recall")
	for _, name := range []string{"A", "B", "C"} {
		f.owners = append(f.owners, createLedgerProfile(t, admin, rls, f.team, name))
	}
	require.NoError(t, rls.WithSystemTx(context.Background(), admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, f.team).Row().Scan(&f.space, &f.generation)
	}))
	insertSearchTestContract(t, admin, rls, "ontology recall", 3, "exact", "")
	f.search = newSearchFixtureStore(app, rls)
	f.knowledge = knowledgepostgres.NewStore(app, rls, knowledge.ConflictRuntimeConfig{})
	f.ontology = ontologypostgres.NewStore(app, rls)
	f.search.recall.WithOntology(func() OntologyReader { return f.ontology.NewRecallReader() })
	return f
}

func (f *recallOntologyFixture) actor(owner int, role string) context.Context {
	return requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: uuid.MustParse(f.team), OwnerID: uuid.MustParse(f.owners[owner]), Role: role,
		Grants: []string{"read", "write"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.MustParse(f.space), Kind: domain.MemorySpaceTeamShared, Generation: f.generation}},
	})
}

func (f *recallOntologyFixture) evidence(t testing.TB, owner int, text string, metadata map[string]any) ontology.SourceHandle {
	t.Helper()
	ctx := context.Background()
	ingest, err := f.knowledge.CreateIngestForTest(ctx, knowledge.CreateIngestInput{
		TeamID: f.team, OwnerProfileID: f.owners[owner], IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex(text + uuid.NewString()),
		Evidence: []knowledge.EvidenceInput{{FragmentID: uuid.NewString(), Content: text, ForceInsert: true, SourceType: "document", Metadata: metadata}},
	})
	require.NoError(t, err)
	fragment := ingest.Evidence[0]
	document, err := f.search.UpsertSearchDocument(ctx, knowledge.UpsertSearchDocumentInput{TeamID: f.team, OwnerProfileID: f.owners[owner], SourceKind: "evidence", SourceID: fragment.FragmentID, SourceVersion: 1, DocumentText: text})
	require.NoError(t, err)
	if document.SearchState == "pending" {
		completeSearchDocumentsForTest(t, f.search, f.team, map[string][]float32{document.SearchDocumentID: {1, 0, 0}})
	}
	return ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: fragment.FragmentID, Version: 1}
}

func (f *recallOntologyFixture) publication(t testing.TB, records ...ontology.Record) ontology.PublicationResult {
	t.Helper()
	page, err := f.ontology.ListRecords(context.Background(), f.team, "", "", 1)
	require.NoError(t, err)
	changes := []ontology.Change{}
	for _, record := range records {
		changes = append(changes, ontology.Change{ExpectedVersion: record.Version, Record: record})
	}
	result, err := f.ontology.PublishManager(f.actor(0, "manager"), f.team, ontology.Publication{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision, Reason: "recall regression fixture", Changes: changes})
	require.NoError(t, err)
	return result
}

func (f *recallOntologyFixture) override(t testing.TB, action ontology.OverrideAction, handles []ontology.SourceHandle) {
	t.Helper()
	snapshots, err := f.ontology.ReadSources(context.Background(), f.team, handles)
	require.NoError(t, err)
	record := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: action, Members: handles}}
	for _, snapshot := range snapshots {
		fingerprint, err := ontology.SourceFingerprint(snapshot)
		require.NoError(t, err)
		record.Sources = append(record.Sources, ontology.SourceDependency{SourceHandle: snapshot.SourceHandle, Fingerprint: fingerprint})
	}
	f.publication(t, record)
}

func (f *recallOntologyFixture) organizer(t testing.TB, meanings map[string]string) (*organization.Service, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var envelope struct{ Messages []struct{ Content string } }
		require.NoError(t, json.NewDecoder(r.Body).Decode(&envelope))
		var request assessment.Request
		require.GreaterOrEqual(t, len(envelope.Messages), 2)
		require.NoError(t, json.Unmarshal([]byte(envelope.Messages[1].Content), &request))
		response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{}, Items: []assessment.Decision{}, Equivalence: []assessment.Equivalence{}}
		definitionRef := ""
		for _, definition := range request.Definitions {
			if definition.Kind == ontology.Topic {
				definitionRef = definition.Ref
				break
			}
		}
		if definitionRef == "" {
			definitionRef = "postgresql-topic"
			response.Definitions = append(response.Definitions, assessment.Definition{Ref: definitionRef, Kind: ontology.Topic, Key: "postgresql", Label: "PostgreSQL", Description: "Synthetic PostgreSQL source organization", Aliases: []string{"database"}})
		}
		items := map[string]assessment.Item{}
		for _, item := range request.Items {
			items[item.Ref] = item
			ref := definitionRef
			if item.LockedDefinitionRef != "" {
				ref = item.LockedDefinitionRef
			}
			response.Items = append(response.Items, assessment.Decision{Ref: item.Ref, Status: "classified", DefinitionRef: ref})
		}
		for _, pair := range request.Pairs {
			relation := pair.RequiredRelation
			if relation == "" {
				relation = "distinct"
				left, right := meanings[items[pair.LeftRef].Text], meanings[items[pair.RightRef].Text]
				if left != "" && left == right {
					relation = "equivalent"
				}
			}
			response.Equivalence = append(response.Equivalence, assessment.Equivalence{Ref: pair.Ref, Relation: relation})
		}
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(encoded)}}}}))
	}))
	t.Cleanup(server.Close)
	limits := assessor.DefaultSemanticAssessmentLimits()
	cfg := &config.Config{AIVerifierAPIURL: server.URL, AIVerifierAPIKey: "synthetic-provider-key", AIVerifierModel: "fixture-model"}
	transport := assessorprovider.NewOpenAIAssessorWithAssessmentLimits(cfg, server.Client(), limits)
	return organization.NewService(f.ontology, assessment.NewProvider(transport, "fixture-model", limits)), calls
}

func (f *recallOntologyFixture) assignment(t testing.TB, definitionID string, handle ontology.SourceHandle) ontology.Record {
	t.Helper()
	snapshots, err := f.ontology.ReadSources(context.Background(), f.team, []ontology.SourceHandle{handle})
	require.NoError(t, err)
	fingerprint, err := ontology.SourceFingerprint(snapshots[0])
	require.NoError(t, err)
	return ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind,
		Assignment: &ontology.Assignment{Source: handle, DefinitionID: definitionID},
		Sources:    []ontology.SourceDependency{{SourceHandle: handle, Fingerprint: fingerprint}}}
}
