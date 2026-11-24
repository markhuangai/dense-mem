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
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/provider/structured"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	organization "github.com/markhuangai/dense-mem/internal/topics"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	topicspostgres "github.com/markhuangai/dense-mem/internal/topics/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type recallOntologyFixture struct {
	admin, app  *gorm.DB
	rls         *storage.RLS
	team, space string
	generation  int64
	owners      []string
	credentials []*domain.Credential
	search      *searchFixtureStore
	knowledge   *knowledgepostgres.Store
	topicStore  *topicspostgres.Store
}

func newRecallOntologyFixture(t testing.TB) *recallOntologyFixture {
	t.Helper()
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	f := &recallOntologyFixture{admin: admin, app: app, rls: rls}
	f.team = createLedgerTeam(t, admin, rls, "ontology recall")
	for _, name := range []string{"A", "B", "C"} {
		owner := createLedgerProfile(t, admin, rls, f.team, name)
		credential, err := access.NewCredentialRepository(admin, rls, nil).GetByIDForTeam(context.Background(), uuid.MustParse(f.team), uuid.MustParse(owner))
		require.NoError(t, err)
		f.owners = append(f.owners, owner)
		f.credentials = append(f.credentials, credential)
	}
	require.NoError(t, rls.WithSystemTx(context.Background(), admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, f.team).Row().Scan(&f.space, &f.generation)
	}))
	insertSearchTestContract(t, admin, rls, "ontology recall", 3, "exact", "")
	f.search = newSearchFixtureStore(app, rls)
	f.knowledge = knowledgepostgres.NewStore(app, rls)
	f.topicStore = topicspostgres.NewStore(app, rls)
	f.search.recall.WithOrganizationReader(func() OrganizationReader { return f.topicStore.NewRecallReader() }).WithTopics(f.topicStore.NewTopicReader())
	return f
}

func (f *recallOntologyFixture) actor(owner int, role string) context.Context {
	credential := f.credentials[owner]
	return requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: credential.TeamID, OwnerID: credential.OwnerID, Role: role,
		IdentityID: credential.ActorIdentityID, MembershipID: credential.MembershipID,
		CredentialID: &credential.ID, AuthMethod: "api_key",
		Grants: []string{"read", "write"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.MustParse(f.space), Kind: domain.MemorySpaceTeamShared, Generation: f.generation}},
	})
}

func (f *recallOntologyFixture) evidence(t testing.TB, owner int, text string, metadata map[string]any) topic.SourceHandle {
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
	return topic.SourceHandle{Kind: topic.EvidenceSource, ID: fragment.FragmentID, Version: 1}
}

func (f *recallOntologyFixture) publication(t testing.TB, records ...topic.Record) topic.PublicationResult {
	t.Helper()
	page, err := f.topicStore.ListRecords(context.Background(), f.team, "", "", 1)
	require.NoError(t, err)
	changes := []topic.Change{}
	for _, record := range records {
		changes = append(changes, topic.Change{ExpectedVersion: record.Version, Record: record})
	}
	result, err := f.topicStore.PublishManager(f.actor(0, "manager"), f.team, topic.Publication{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision, Reason: "recall regression fixture", Changes: changes})
	require.NoError(t, err)
	return result
}

func (f *recallOntologyFixture) rejectHistoricalOverride(t testing.TB, action topic.OverrideAction, handles []topic.SourceHandle) {
	t.Helper()
	snapshots, err := f.topicStore.ReadSources(context.Background(), f.team, handles)
	require.NoError(t, err)
	record := topic.Record{ID: uuid.NewString(), Kind: topic.OverrideKind, Override: &topic.Override{Action: action, Members: handles}}
	for _, snapshot := range snapshots {
		fingerprint, err := topic.SourceFingerprint(snapshot)
		require.NoError(t, err)
		record.Sources = append(record.Sources, topic.SourceDependency{SourceHandle: snapshot.SourceHandle, Fingerprint: fingerprint})
	}
	page, err := f.topicStore.ListRecords(context.Background(), f.team, "", "", 1)
	require.NoError(t, err)
	_, err = f.topicStore.PublishManager(f.actor(0, "manager"), f.team, topic.Publication{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision, Reason: "Retired override", Changes: []topic.Change{{Record: record}}})
	require.ErrorIs(t, err, topic.ErrOverride)
}

func (f *recallOntologyFixture) organizer(t testing.TB, meanings map[string]string) (*organization.Service, *atomic.Int32) {
	return f.organizerWithAccounting(t, meanings, false, nil)
}

func (f *recallOntologyFixture) organizerWithAccounting(t testing.TB, meanings map[string]string, forceDistinct bool, accounting assessment.AttemptAccounting) (*organization.Service, *atomic.Int32) {
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
			if definition.Kind == topic.Topic {
				definitionRef = definition.Ref
				break
			}
		}
		if definitionRef == "" {
			definitionRef = "postgresql-topic"
		}
		items := map[string]assessment.Item{}
		for _, item := range request.Items {
			items[item.Ref] = item
			if item.LockedDefinitionRef == "" {
				response.Items = append(response.Items, assessment.Decision{Ref: item.Ref, Status: "classified", DefinitionRef: definitionRef})
			}
		}
		if definitionRef == "postgresql-topic" && len(response.Items) != 0 {
			response.Definitions = append(response.Definitions, assessment.Definition{Ref: definitionRef, Kind: topic.Topic, Key: "postgresql", Label: "PostgreSQL", Description: "Synthetic PostgreSQL source organization", Aliases: []string{"database"}})
		}
		for _, pair := range request.Pairs {
			if pair.RequiredRelation != "" {
				continue
			}
			relation := "distinct"
			left, right := meanings[items[pair.LeftRef].Text], meanings[items[pair.RightRef].Text]
			if !forceDistinct && left != "" && left == right {
				relation = "equivalent"
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
	transport := structured.NewOpenAI(cfg, server.Client())
	return organization.NewService(f.topicStore, assessment.NewProviderWithAccounting(transport, "fixture-model", limits, accounting, 300000)), calls
}

func (f *recallOntologyFixture) assignment(t testing.TB, definitionID string, handle topic.SourceHandle) topic.Record {
	t.Helper()
	snapshots, err := f.topicStore.ReadSources(context.Background(), f.team, []topic.SourceHandle{handle})
	require.NoError(t, err)
	fingerprint, err := topic.SourceFingerprint(snapshots[0])
	require.NoError(t, err)
	return topic.Record{ID: uuid.NewString(), Kind: topic.AssignmentKind,
		Assignment: &topic.Assignment{Source: handle, DefinitionID: definitionID},
		Sources:    []topic.SourceDependency{{SourceHandle: handle, Fingerprint: fingerprint}}}
}
