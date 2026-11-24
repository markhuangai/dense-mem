//go:build integration

package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	assessmentfixture "github.com/markhuangai/dense-mem/internal/testfixture/assessment"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	accesspg "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/assessor"
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpg "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	densehttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	knowledgepg "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/provider/structured"
	"github.com/markhuangai/dense-mem/internal/recall"
	recallpg "github.com/markhuangai/dense-mem/internal/recall/postgres"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	searchpg "github.com/markhuangai/dense-mem/internal/search/postgres"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspg "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	organization "github.com/markhuangai/dense-mem/internal/topics"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
	ontologypg "github.com/markhuangai/dense-mem/internal/topics/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type runnerServiceFixture struct {
	admin          *gorm.DB
	native         *evalharness.Native
	manifest       evalharness.Manifest
	mcp            *httptest.Server
	control        *httptest.Server
	maintenance    *organization.MaintenanceService
	ontology       *ontologypg.Store
	provider       *runnerRememberAssessor
	reconciliation searchapp.SearchReconciliationService
}

func newRunnerServiceFixture(t *testing.T) *runnerServiceFixture {
	t.Helper()
	admin, app, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	teamID, ownerID := uuid.New(), uuid.New()
	require.NoError(t, accesspg.NewTeamRepository(admin, rls).Create(ctx, &domain.Team{ID: teamID, Name: "runner-" + uuid.NewString()}))
	credential := &domain.Credential{ID: ownerID, TeamID: teamID, Name: "runner owner", KeyHash: "synthetic", KeyPrefix: strings.ReplaceAll(ownerID.String(), "-", "")[:24], KeySuffix: "owner", Scopes: []string{"read", "write"}}
	require.NoError(t, accesspg.NewCredentialRepository(admin, rls, nil).CreateCredential(ctx, credential))
	model, dimensions := ensureRememberProcessorIntegrationSearchContract(t, admin, rls)
	store := knowledgepg.NewStore(app, rls)
	provider := &runnerRememberAssessor{failures: 1}
	embedder := rememberProcessorIntegrationSearchEmbedder{model: model, dimensions: dimensions}
	writer := remember.NewService(remember.Dependencies{Synchronous: processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store, Assessor: provider, EmbeddingExecutor: semanticwrite.NewProviderExecutor(embedder), Limits: assessor.DefaultSemanticAssessmentLimits()})})
	actorCtx := rememberProcessorActorContext(t, ctx, admin, rls, teamID, ownerID)
	actor, ok := requestctx.ActorFromContext(actorCtx)
	require.True(t, ok)
	search := searchpg.NewStore(app, rls)
	audit := auditapp.New(auditpg.NewStore(app, rls))
	configuration := settings.NewAppConfigService(settingspg.NewAppConfigRepository(app, rls), nil)
	_, err := configuration.UpdateTopicMaintenanceSettings(ctx, map[string]string{domain.AppConfigTopicMaintenanceEnabled: "true", domain.AppConfigTopicMaintenanceModel: "runner-ontology", domain.AppConfigTopicMaintenanceContextWindow: "300000", domain.AppConfigTopicMaintenanceConcurrency: "4", domain.AppConfigTopicMaintenanceTeamConcurrency: "4"}, "control", "", "")
	require.NoError(t, err)
	topicStore := ontologypg.NewStore(app, rls)
	policy, err := configuration.TopicMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	_, err = topicStore.EnsureMaintenanceWindow(ctx, policy, time.Now())
	require.NoError(t, err)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Messages []modelprovider.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil || len(envelope.Messages) < 2 {
			http.Error(w, "invalid provider request", 400)
			return
		}
		var input assessment.Request
		if err := json.Unmarshal([]byte(envelope.Messages[1].Content), &input); err != nil {
			http.Error(w, "invalid organization request", 400)
			return
		}
		body, err := json.Marshal(runnerOrganizationResponse(input))
		if err != nil {
			http.Error(w, "invalid fixture result", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(body)}}}})
	}))
	t.Cleanup(upstream.Close)
	transport := structured.NewOpenAI(&config.Config{AIVerifierAPIURL: upstream.URL, AIVerifierAPIKey: "synthetic", AIVerifierModel: "runner-ontology"}, upstream.Client())
	maintenance := organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: topicStore, Config: configuration, Audit: audit, DefaultModel: "runner-ontology", Organizer: func(model string, tokens int64, accounting assessment.AttemptAccounting) *organization.Service {
		return organization.NewService(topicStore, assessment.NewProviderWithAccounting(transport, model, assessor.DefaultSemanticAssessmentLimits(), accounting, int(tokens)))
	}})
	_, err = maintenance.Command(ctx, domain.TopicMaintenanceCommand{Action: "pause", OperationKey: uuid.NewString()}, "", "")
	require.NoError(t, err)
	recallStore := recallpg.NewStore(app, rls, search, recallpg.LoadRecallEvidenceConflictRecords).
		WithOntology(func() recallpg.OntologyReader { return topicStore.NewRecallReader() }).
		WithTopics(topicStore.NewTopicReader())
	reader := recall.NewRecallService(recall.RecallDependencies{Search: recallStore, Provider: embedder, OntologyConfig: configuration})
	reg, err := registry.BuildActive(registry.Dependencies{RememberBindings: registry.RememberBindings{Service: writer}, RecallBindings: registry.RecallBindings{Service: reader}})
	require.NoError(t, err)
	e := echo.New()
	e.POST("/mcp", handler.NewMCPHandler(reg, nil).HandlePost)
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCtx := requestctx.WithActor(r.Context(), actor)
		callCtx = middleware.SetPrincipalForTest(callCtx, &middleware.Principal{TeamID: teamID, OwnerID: actor.OwnerID, Grants: actor.Grants, AllowedSpaces: actor.AllowedSpaces})
		callCtx = middleware.SetResolvedTeamIDForTest(callCtx, teamID)
		e.ServeHTTP(w, r.WithContext(callCtx))
	}))
	t.Cleanup(mcp.Close)
	control := runnerControlServer(t, maintenance, configuration, searchapp.NewSearchConvergenceService(search, store))
	pool, err := admin.DB()
	require.NoError(t, err)
	client := &evalharness.HTTPClient{BaseURL: mcp.URL, APIKey: "fixture", Client: mcp.Client()}
	native := &evalharness.Native{DB: pool, Scope: evalharness.Scope{TeamID: teamID.String(), CredentialID: ownerID.String()}, ControlURL: control.URL, ControlToken: "runner-control", Client: client}
	require.NoError(t, native.ResolveScope(ctx))
	manifest := evalharness.Manifest{ImportWorkers: 10, Model: "runner-ontology", ContextWindowTokens: 300000, EmbeddingModel: model, EmbeddingDimensions: dimensions}
	return &runnerServiceFixture{admin: admin, native: native, manifest: manifest, mcp: mcp, control: control, maintenance: maintenance,
		ontology: topicStore, provider: provider, reconciliation: searchapp.NewSearchReconciliationService(searchapp.SearchReconciliationDependencies{Repository: search, Projection: store, Executor: semanticwrite.NewProviderExecutor(embedder)})}
}

func runnerControlServer(t *testing.T, maintenance *organization.MaintenanceService, configuration settings.AppConfigService, convergence searchapp.SearchConvergenceReader) *httptest.Server {
	t.Helper()
	bindings := densehttp.ControlPortalBindings{Telemetry: densehttp.ControlPortalTelemetry{
		Ontology: maintenance, Config: configuration, Convergence: convergence,
	}}
	portal, err := densehttp.NewControlPortalServerWithCapabilityBindings(&config.Config{ControlPortalToken: "runner-control"}, nil, nil, nil, bindings, densehttp.HealthConfig{}, nil)
	require.NoError(t, err)
	control := httptest.NewServer(portal)
	t.Cleanup(control.Close)
	return control
}

type runnerRememberAssessor struct {
	rememberProcessorIntegrationRegistrationAssessor
	failures int
}

func (p *runnerRememberAssessor) Complete(ctx context.Context, wire modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	if wire.SchemaName == assessor.PartitionSchemaName {
		return partitionFixtureComplete(wire)
	}
	if p.failures > 0 {
		p.failures--
		p.calls++
		return modelprovider.StructuredResult{}, &modelprovider.ProviderError{
			Provider: "runner-fixture", FailureClass: modelprovider.ProviderFailureClassHTTPServer, StatusCode: http.StatusServiceUnavailable,
		}
	}
	request, err := assessmentfixture.Request(wire)
	if err != nil {
		return modelprovider.StructuredResult{}, err
	}
	if !strings.HasPrefix(request.Evidence[0].Content, "PostgreSQL stores durable memory for") {
		return p.rememberProcessorIntegrationRegistrationAssessor.Complete(ctx, wire)
	}
	p.calls++
	reply := rememberProcessorIntegrationAssessmentResponse(request)
	for _, group := range request.EvidenceEquivalenceCandidates {
		for _, candidate := range group.Candidates {
			if candidate.Content != "Project Alpha stores durable memory in PostgreSQL." {
				continue
			}
			for index := range reply.EvidenceEquivalenceResults {
				result := &reply.EvidenceEquivalenceResults[index]
				if result.EvidenceID == group.EvidenceID {
					result.Action, result.CandidateEvidenceID = "reuse", &candidate.EvidenceID
					return assessmentfixture.Result(wire, reply)
				}
			}
		}
	}
	return modelprovider.StructuredResult{}, fmt.Errorf("paraphrase fixture has no supplied canonical candidate")
}

func pairArgumentString(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return value
}

func runnerOrganizationResponse(request assessment.Request) assessment.Response {
	response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{}, Items: []assessment.Decision{}, Equivalence: []assessment.Equivalence{}}
	created := map[string]string{}
	for _, item := range request.Items {
		if item.LockedDefinitionRef != "" {
			continue
		}
		kind, key := ontology.Topic, "runner-storage"
		ref := item.PreferredDefinitionRef
		if ref == "" {
			for _, definition := range request.Definitions {
				if definition.Kind == kind {
					ref = definition.Ref
					break
				}
			}
		}
		if ref == "" {
			ref = created[key]
			if ref == "" {
				ref = "new-" + key
				created[key] = ref
				response.Definitions = append(response.Definitions, assessment.Definition{Ref: ref, Kind: kind, Key: key, Label: key, Description: "Paired fixture classification", Aliases: []string{}})
			}
		}
		response.Items = append(response.Items, assessment.Decision{Ref: item.Ref, Status: "classified", DefinitionRef: ref})
	}
	for _, pair := range request.Pairs {
		if pair.RequiredRelation == "" {
			response.Equivalence = append(response.Equivalence, assessment.Equivalence{Ref: pair.Ref, Relation: "distinct"})
		}
	}
	return response
}

func (f *runnerServiceFixture) snapshot(t *testing.T) evalharness.NativeObservation {
	t.Helper()
	snapshot, err := f.native.Snapshot(context.Background())
	require.NoError(t, err)
	return snapshot
}

func (f *runnerServiceFixture) controlResponse(t *testing.T, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.control.URL+"/control/api"+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer runner-control")
	response, err := f.control.Client().Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, body
}
