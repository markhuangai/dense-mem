package ontology

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypostgres "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestOrganizationServiceWithPostgres(t *testing.T) {
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	container, err := postgrescontainer.Run(context.Background(), "pgvector/pgvector:0.8.2-pg18-trixie",
		postgrescontainer.WithDatabase("ontology_service_test"), postgrescontainer.WithUsername("testuser"), postgrescontainer.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(context.Background(), "sslmode=disable")
	require.NoError(t, err)
	admin, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	migrator, err := storage.NewMigrator(admin)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(context.Background()))
	require.NoError(t, admin.Exec(`CREATE ROLE ontology_service_app LOGIN PASSWORD 'ontology_test' NOSUPERUSER NOBYPASSRLS;
        GRANT USAGE ON SCHEMA public TO ontology_service_app;
        GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO ontology_service_app;
        GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO ontology_service_app;
        GRANT EXECUTE ON FUNCTION dense_mem_active_space_generation(UUID,UUID) TO ontology_service_app;
        GRANT EXECUTE ON FUNCTION dense_mem_lock_memory_space(UUID,UUID) TO ontology_service_app`).Error)
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	parsed.User = url.UserPassword("ontology_service_app", "ontology_test")
	app, err := gorm.Open(gormpostgres.Open(parsed.String()), &gorm.Config{})
	require.NoError(t, err)
	for _, db := range []*gorm.DB{admin, app} {
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	}
	rls := storage.NewRLS()
	team := &domain.Team{Name: "organization-service-" + uuid.NewString()}
	require.NoError(t, access.NewTeamRepository(admin, rls).Create(context.Background(), team))
	credential := &domain.Credential{ID: uuid.New(), TeamID: team.ID, Name: "source-owner", KeyHash: "synthetic-service-owner", KeyPrefix: uuid.NewString()[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}
	require.NoError(t, access.NewCredentialRepository(admin, rls, nil).CreateCredential(context.Background(), credential))
	var spaceID string
	var generation int64
	require.NoError(t, rls.WithSystemTx(context.Background(), admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, team.ID).Row().Scan(&spaceID, &generation)
	}))
	actor := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: team.ID, OwnerID: credential.ID, Role: "member", Grants: []string{"read", "write"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.MustParse(spaceID), Kind: domain.MemorySpaceTeamShared, Generation: generation}}})
	canonical := knowledge.NewStore(app, rls, knowledgecontract.ConflictRuntimeConfig{})
	source := func(text string) contract.SourceHandle {
		result, err := canonical.CreateIngestForTest(context.Background(), knowledgecontract.CreateIngestInput{TeamID: team.ID.String(), OwnerProfileID: credential.ID.String(), Evidence: []knowledgecontract.EvidenceInput{{FragmentID: uuid.NewString(), Content: text, ForceInsert: true}}})
		require.NoError(t, err)
		return contract.SourceHandle{Kind: contract.EvidenceSource, ID: result.Evidence[0].FragmentID, Version: 1}
	}
	repository := ontologypostgres.NewStore(app, rls)
	var calls, mode atomic.Int32
	started := make(chan struct{}, 1)
	var withdraw contract.SourceHandle
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var envelope struct{ Messages []struct{ Content string } }
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil || len(envelope.Messages) < 2 {
			t.Error("invalid provider request envelope")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		if mode.Load() == 6 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if mode.Load() == 2 {
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		content := "{}"
		if mode.Load() != 1 {
			var request assessment.Request
			if err := json.Unmarshal([]byte(envelope.Messages[1].Content), &request); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{}, Items: []assessment.Decision{}, Equivalence: []assessment.Equivalence{}}
			kind, key, baseKind := contract.Topic, "postgresql", ""
			if request.Items[0].Kind == contract.EntitySource {
				kind, key, baseKind = contract.EntityClass, request.Items[0].EntityKind, request.Items[0].EntityKind
			}
			ref := "new-topic"
			for _, definition := range request.Definitions {
				if definition.Kind == kind {
					ref = definition.Ref
					break
				}
			}
			if ref == "new-topic" {
				response.Definitions = append(response.Definitions, assessment.Definition{Ref: ref, Kind: kind, Key: key, Label: key, Aliases: []string{}, BaseEntityKind: baseKind})
			}
			for _, item := range request.Items {
				definitionRef := ref
				if item.LockedDefinitionRef != "" {
					definitionRef = item.LockedDefinitionRef
				}
				response.Items = append(response.Items, assessment.Decision{Ref: item.Ref, Status: "classified", DefinitionRef: definitionRef})
			}
			for _, pair := range request.Pairs {
				relation := pair.RequiredRelation
				if relation == "" {
					relation = "distinct"
					if mode.Load() == 7 {
						relation = "equivalent"
					}
					if mode.Load() == 4 {
						relation = "ambiguous"
					}
				}
				response.Equivalence = append(response.Equivalence, assessment.Equivalence{Ref: pair.Ref, Relation: relation})
			}
			if mode.Load() == 5 {
				response.Definitions = []assessment.Definition{}
				for i := range response.Items {
					response.Items[i].Status = "ambiguous"
					response.Items[i].DefinitionRef = ""
					response.Items[i].Reason = "classification_uncertain"
				}
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			content = string(encoded)
			if mode.Load() == 3 {
				_, err := canonical.RetractEvidence(actor, knowledgecontract.RetractEvidenceInput{TeamID: team.ID.String(), OwnerProfileID: credential.ID.String(), EvidenceIDs: []string{withdraw.ID}, Reason: "withdraw during service assessment", IdempotencyKey: "service-withdraw", RequestHash: "sha256:" + strings.Repeat("f", 64)})
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{AIVerifierAPIURL: server.URL, AIVerifierAPIKey: "synthetic-provider-key", AIVerifierModel: "service-fixture"}
	limits := assessorprovider.SemanticAssessmentLimitsForConfig(cfg)
	provider := assessment.NewProvider(assessorprovider.NewOpenAIAssessorWithAssessmentLimits(cfg, server.Client(), limits), cfg.AIVerifierModel, limits)
	service := NewService(repository, provider)
	a, b := source("Atlas uses PostgreSQL."), source("Atlas uses PostgreSQL.")
	input := contract.OrganizationInput{OperationKey: "service-success", Sources: []contract.SourceHandle{a, b}}
	mode.Store(7)
	result, err := service.Organize(context.Background(), team.ID.String(), input)
	mode.Store(0)
	require.NoError(t, err)
	require.NotNil(t, result.Publication)
	require.True(t, result.Current)
	groups, err := repository.ListRecords(context.Background(), team.ID.String(), contract.EvidenceGroup, "", 20)
	require.NoError(t, err)
	require.Len(t, groups.Records, 1)
	require.ElementsMatch(t, input.Sources, groups.Records[0].Group.Members)
	input.OperationKey = "service-replay"
	replay, err := service.Organize(context.Background(), team.ID.String(), input)
	require.NoError(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, result.AssessmentID, replay.AssessmentID)
	require.Equal(t, int32(1), calls.Load())
	reuseProvider := assessment.NewProvider(assessorprovider.NewOpenAIAssessorWithAssessmentLimits(cfg, server.Client(), limits), "service-fixture-v2", limits)
	reused, err := NewService(repository, reuseProvider).Organize(context.Background(), team.ID.String(), contract.OrganizationInput{OperationKey: "service-deterministic-reuse", Sources: input.Sources})
	require.NoError(t, err)
	require.NotEqual(t, result.AssessmentID, reused.AssessmentID)
	require.Nil(t, reused.Publication)
	require.Empty(t, reused.Attempts)
	require.Equal(t, "unchanged", reused.Outcomes[0].Status)
	require.Equal(t, int32(1), calls.Load())
	incomplete, err := service.Organize(context.Background(), team.ID.String(), contract.OrganizationInput{OperationKey: "service-incomplete-group", Sources: []contract.SourceHandle{a}})
	require.NoError(t, err)
	require.Equal(t, "ambiguous", incomplete.Outcomes[0].Status)
	require.Equal(t, "resubmit_complete_group", incomplete.Outcomes[0].Reason)
	require.Nil(t, incomplete.Publication)
	require.Empty(t, incomplete.Attempts)
	require.Equal(t, int32(1), calls.Load())
	entity, err := canonical.CreateEntity(context.Background(), knowledgecontract.CreateEntityInput{TeamID: team.ID.String(), OwnerProfileID: credential.ID.String(), EntityKind: "person", CanonicalName: "Ada"})
	require.NoError(t, err)
	classified, err := service.Organize(context.Background(), team.ID.String(), contract.OrganizationInput{OperationKey: "service-entity-class", Sources: []contract.SourceHandle{{Kind: contract.EntitySource, ID: entity.EntityID, Version: int64(entity.Version)}}})
	require.NoError(t, err)
	require.Equal(t, "organized", classified.Outcomes[0].Status)
	var entityKind string
	require.NoError(t, admin.Raw(`SELECT entity_kind FROM entity_records WHERE team_id=?::uuid AND entity_id=?::uuid`, team.ID, entity.EntityID).Row().Scan(&entityKind))
	require.Equal(t, "person", entityKind)
	mode.Store(4)
	e, f := source("Atlas stores data in PostgreSQL."), source("PostgreSQL is Atlas's data store.")
	ambiguous, err := service.Organize(context.Background(), team.ID.String(), contract.OrganizationInput{OperationKey: "service-ambiguous-comparison", Sources: []contract.SourceHandle{e, f}})
	require.NoError(t, err)
	require.Len(t, ambiguous.AmbiguousComparisons, 1)
	require.ElementsMatch(t, []contract.SourceHandle{e, f}, []contract.SourceHandle{ambiguous.AmbiguousComparisons[0].Left, ambiguous.AmbiguousComparisons[0].Right})
	groups, err = repository.ListRecords(context.Background(), team.ID.String(), contract.EvidenceGroup, "", 20)
	require.NoError(t, err)
	require.Len(t, groups.Records, 1)
	mode.Store(5)
	ambiguous, err = service.Organize(context.Background(), team.ID.String(), contract.OrganizationInput{OperationKey: "service-ambiguous-classification", Sources: []contract.SourceHandle{source("This source is hard to classify.")}})
	require.NoError(t, err)
	require.Equal(t, "ambiguous", ambiguous.Outcomes[0].Status)
	require.Equal(t, "classification_ambiguous", ambiguous.Outcomes[0].Reason)
	require.Nil(t, ambiguous.Publication)
	_, err = service.Organize(context.Background(), team.ID.String(), contract.OrganizationInput{})
	require.ErrorIs(t, err, contract.ErrInvalid)
	mode.Store(1)
	malformed, err := service.Organize(context.Background(), team.ID.String(), contract.OrganizationInput{OperationKey: "service-malformed", Sources: []contract.SourceHandle{source("Beacon uses PostgreSQL.")}})
	require.Error(t, err)
	require.Equal(t, "provider_response_invalid", malformed.FailureCode)
	require.Nil(t, malformed.Publication)
	require.Len(t, malformed.Attempts, 3)
	require.Equal(t, int32(7), calls.Load())
	mode.Store(6)
	failedInput := contract.OrganizationInput{OperationKey: "service-provider-unavailable", Sources: []contract.SourceHandle{source("Foxtrot uses PostgreSQL.")}}
	unavailableProvider, err := service.Organize(context.Background(), team.ID.String(), failedInput)
	require.ErrorContains(t, err, "provider_unavailable")
	require.Equal(t, "provider_unavailable", unavailableProvider.FailureCode)
	require.Len(t, unavailableProvider.Attempts, 1)
	require.Nil(t, unavailableProvider.Publication)
	priorCalls := calls.Load()
	replay, err = service.Organize(context.Background(), team.ID.String(), failedInput)
	require.Error(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, priorCalls, calls.Load())
	mode.Store(0)
	failedInput.OperationKey = "service-provider-recovered"
	recovered, err := service.Organize(context.Background(), team.ID.String(), failedInput)
	require.NoError(t, err)
	require.False(t, recovered.Existing)
	require.True(t, recovered.Current)
	require.Empty(t, recovered.FailureCode)
	require.NotEqual(t, unavailableProvider.AssessmentID, recovered.AssessmentID)
	require.Equal(t, priorCalls+1, calls.Load())
	failedInput.OperationKey = "service-provider-unavailable"
	replay, err = service.Organize(context.Background(), team.ID.String(), failedInput)
	require.ErrorContains(t, err, "provider_unavailable")
	require.Equal(t, unavailableProvider.AssessmentID, replay.AssessmentID)
	require.Equal(t, priorCalls+1, calls.Load())
	mode.Store(6)
	mixedSource := source("Mixed batch uses PostgreSQL.")
	mixed, err := service.Organize(context.Background(), team.ID.String(), contract.OrganizationInput{OperationKey: "service-mixed-provider-failure", Sources: []contract.SourceHandle{a, mixedSource}})
	require.ErrorContains(t, err, "provider_unavailable")
	require.Equal(t, "provider_unavailable", mixed.FailureCode)
	require.Nil(t, mixed.Publication)
	for _, outcome := range mixed.Outcomes {
		if outcome.Source.ID == a.ID {
			require.Equal(t, "ambiguous", outcome.Status)
			require.Equal(t, "resubmit_complete_group", outcome.Reason)
		} else {
			require.Equal(t, mixedSource.ID, outcome.Source.ID)
			require.Equal(t, "failed", outcome.Status)
			require.Equal(t, "provider_unavailable", outcome.Reason)
		}
	}
	priorCalls = calls.Load()
	mixedReplay, err := service.Organize(context.Background(), team.ID.String(), contract.OrganizationInput{OperationKey: "service-mixed-provider-failure", Sources: []contract.SourceHandle{a, mixedSource}})
	require.Error(t, err)
	require.True(t, mixedReplay.Existing)
	require.Equal(t, mixed.Outcomes, mixedReplay.Outcomes)
	require.Equal(t, priorCalls, calls.Load())
	mode.Store(3)
	withdraw = source("Cedar uses PostgreSQL.")
	stale, err := service.Organize(context.Background(), team.ID.String(), contract.OrganizationInput{OperationKey: "service-stale", Sources: []contract.SourceHandle{withdraw}})
	require.ErrorIs(t, err, contract.ErrSourceStale)
	require.Equal(t, "stale_input", stale.FailureCode)
	require.Nil(t, stale.Publication)
	mode.Store(2)
	cancelledInput := contract.OrganizationInput{OperationKey: "service-cancelled", Sources: []contract.SourceHandle{source("Delta uses PostgreSQL.")}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type completion struct {
		result contract.OrganizationResult
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		result, err := service.Organize(ctx, team.ID.String(), cancelledInput)
		done <- completion{result, err}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("provider request did not start")
	}
	cancel()
	var cancelled completion
	select {
	case cancelled = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled assessment did not finish")
	}
	require.ErrorIs(t, cancelled.err, context.Canceled)
	require.Equal(t, "request_cancelled", cancelled.result.FailureCode)
	require.Nil(t, cancelled.result.Publication)
	replay, err = service.Organize(context.Background(), team.ID.String(), cancelledInput)
	require.Error(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, cancelled.result.AssessmentID, replay.AssessmentID)
	priorCalls = calls.Load()
	unavailable, err := service.Organize(context.Background(), team.ID.String(), contract.OrganizationInput{OperationKey: "service-unavailable", Sources: []contract.SourceHandle{{Kind: contract.EvidenceSource, ID: uuid.NewString(), Version: 1}}})
	require.NoError(t, err)
	require.Equal(t, "unavailable", unavailable.Outcomes[0].Status)
	require.Nil(t, unavailable.Publication)
	require.Empty(t, unavailable.Attempts)
	require.Equal(t, priorCalls, calls.Load())
	organization, err := repository.ReadOrganization(context.Background(), team.ID.String(), input.Sources)
	require.NoError(t, err)
	separation := contract.Record{ID: uuid.NewString(), Kind: contract.OverrideKind, Override: &contract.Override{Action: contract.KeepSeparate, Members: input.Sources}}
	for _, snapshot := range organization.Sources {
		fingerprint, err := contract.SourceFingerprint(snapshot)
		require.NoError(t, err)
		separation.Sources = append(separation.Sources, contract.SourceDependency{SourceHandle: snapshot.SourceHandle, Fingerprint: fingerprint})
	}
	manager, ok := requestctx.ActorFromContext(actor)
	require.True(t, ok)
	manager.Role = "manager"
	_, err = repository.PublishManager(requestctx.WithActor(context.Background(), manager), team.ID.String(), contract.Publication{OperationKey: "service-manager-separation", ExpectedRevision: organization.Revision, Reason: "keep original sources separate", Changes: []contract.Change{{Record: separation}}})
	require.NoError(t, err)
	mode.Store(0)
	separatedInput := contract.OrganizationInput{OperationKey: "service-retire-separated-group", Sources: input.Sources}
	separated, err := service.Organize(context.Background(), team.ID.String(), separatedInput)
	require.NoError(t, err)
	require.NotNil(t, separated.Publication)
	require.Len(t, separated.Attempts, 1)
	require.Equal(t, priorCalls+1, calls.Load())
	priorCalls = calls.Load()
	retired, err := repository.GetRecord(context.Background(), team.ID.String(), groups.Records[0].ID, 0)
	require.NoError(t, err)
	require.True(t, retired.Retired)
	require.Equal(t, int64(2), retired.Version)
	require.ElementsMatch(t, input.Sources, retired.Group.Members)
	retainedOverride, err := repository.GetRecord(context.Background(), team.ID.String(), separation.ID, 0)
	require.NoError(t, err)
	require.False(t, retainedOverride.Retired)
	require.Equal(t, contract.KeepSeparate, retainedOverride.Override.Action)
	replay, err = service.Organize(context.Background(), team.ID.String(), separatedInput)
	require.NoError(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, separated.AssessmentID, replay.AssessmentID)
	require.Equal(t, priorCalls, calls.Load())
	preserved, err := repository.ReadSources(context.Background(), team.ID.String(), input.Sources)
	require.NoError(t, err)
	require.ElementsMatch(t, organization.Sources, preserved)
	var count int
	require.NoError(t, admin.Raw(`SELECT count(*) FROM ontology_assessments WHERE team_id=?::uuid`, team.ID).Row().Scan(&count))
	require.Equal(t, 15, count)
}
