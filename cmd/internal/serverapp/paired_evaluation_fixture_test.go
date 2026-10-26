//go:build integration

package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	accesspg "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/assessor"
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpg "github.com/markhuangai/dense-mem/internal/audit/postgres"
	communitypg "github.com/markhuangai/dense-mem/internal/community/postgres"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/evaluation"
	evaluationpg "github.com/markhuangai/dense-mem/internal/evaluation/postgres"
	densehttp "github.com/markhuangai/dense-mem/internal/http"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepg "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypg "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/markhuangai/dense-mem/internal/recall"
	recallpg "github.com/markhuangai/dense-mem/internal/recall/postgres"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	searchpg "github.com/markhuangai/dense-mem/internal/search/postgres"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspg "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type pairedServiceFixture struct {
	admin          *gorm.DB
	release        evalharness.PairRelease
	mcp            *httptest.Server
	control        *httptest.Server
	maintenance    *organization.MaintenanceService
	projections    communityapp.Service
	provider       *rememberProcessorIntegrationRegistrationAssessor
	reconciliation searchapp.SearchReconciliationService
}

func newPairedServiceFixture(t *testing.T, side string) *pairedServiceFixture {
	t.Setenv("PAIR_FIXTURE_API_KEY", "fixture")
	t.Helper()
	admin, app, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	teamID, ownerID := uuid.New(), uuid.New()
	require.NoError(t, accesspg.NewTeamRepository(admin, rls).Create(ctx, &domain.Team{ID: teamID, Name: "paired-" + uuid.NewString()}))
	credential := &domain.Credential{ID: ownerID, TeamID: teamID, Name: "paired owner", KeyHash: "synthetic", KeyPrefix: strings.ReplaceAll(ownerID.String(), "-", "")[:24], KeySuffix: "owner", Scopes: []string{"read", "write"}}
	require.NoError(t, accesspg.NewCredentialRepository(admin, rls, nil).CreateCredential(ctx, credential))
	model, dimensions := ensureRememberProcessorIntegrationSearchContract(t, admin, rls)
	store := knowledgepg.NewStore(app, rls, knowledge.ConflictRuntimeConfig{})
	provider := &rememberProcessorIntegrationRegistrationAssessor{}
	embedder := rememberProcessorIntegrationSearchEmbedder{model: model, dimensions: dimensions}
	writer := remember.NewService(remember.Dependencies{Synchronous: processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store, Catalog: store, Assessor: provider, Embedder: embedder, Limits: assessor.DefaultSemanticAssessmentLimits()})})
	actorCtx := rememberProcessorActorContext(t, ctx, admin, rls, teamID, ownerID)
	actor, ok := requestctx.ActorFromContext(actorCtx)
	require.True(t, ok)
	search := searchpg.NewStore(app, rls)
	audit := auditapp.New(auditpg.NewStore(app, rls))
	configuration := settings.NewAppConfigService(settingspg.NewAppConfigRepository(app, rls), nil)
	_, err := configuration.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "true", domain.AppConfigOntologyModel: "paired-ontology", domain.AppConfigOntologyContextWindow: "300000", domain.AppConfigOntologyConcurrency: "4", domain.AppConfigOntologyTeamConcurrency: "4"}, "control", "", "")
	require.NoError(t, err)
	ontologyStore := ontologypg.NewStore(app, rls)
	policy, err := configuration.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	_, err = ontologyStore.EnsureMaintenanceWindow(ctx, policy, time.Now())
	require.NoError(t, err)
	communityStore := communitypg.NewStore(app, rls).WithTopics(ontologyStore.NewTopicCatalogReader(), ontologyStore.NewTopicMembershipReader(), ontologyStore.NewTopicProjectionAdmission(), ontologyStore.NewTopicProjectionRelease())
	projections := communityapp.New(communityapp.Dependencies{Store: communityStore, AppConfig: configuration})
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
		body, err := json.Marshal(pairedOrganizationResponse(input))
		if err != nil {
			http.Error(w, "invalid fixture result", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(body)}}}})
	}))
	t.Cleanup(upstream.Close)
	transport := assessorprovider.NewOpenAIAssessor(&config.Config{AIVerifierAPIURL: upstream.URL, AIVerifierAPIKey: "synthetic", AIVerifierModel: "paired-ontology"}, upstream.Client())
	maintenance := organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: ontologyStore, Config: configuration, Audit: audit, Projections: projections, DefaultModel: "paired-ontology", Organizer: func(model string, tokens int64, accounting assessment.AttemptAccounting) *organization.Service {
		return organization.NewService(ontologyStore, assessment.NewProviderWithAccounting(transport, model, assessor.DefaultSemanticAssessmentLimits(), accounting, int(tokens)))
	}})
	_, err = maintenance.Command(ctx, domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: uuid.NewString()}, "", "")
	require.NoError(t, err)
	recallStore := recallpg.NewStore(app, rls, search, searchRecallConflictReader(), recallpg.LoadRecallEvidenceConflictRecords).
		WithOntology(func() recallpg.OntologyReader { return ontologyStore.NewRecallReader() })
	reader := evaluation.New(evaluation.Dependencies{Repository: evaluationpg.NewReader(app, rls), Audit: audit, Recall: recall.NewRecallService(recall.RecallDependencies{Search: recallStore, Provider: embedder, OntologyConfig: configuration, Communities: communityStore})})
	reg, err := registry.BuildActive(registry.Dependencies{RememberBindings: registry.RememberBindings{Service: writer}})
	require.NoError(t, err)
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.Method == "tools/list" {
			listed := []map[string]any{}
			for _, tool := range registry.ContractTools() {
				listed = append(listed, map[string]any{"name": tool.Name, "inputSchema": tool.InputSchema, "outputSchema": tool.OutputSchema})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": listed}})
			return
		}
		callCtx := requestctx.WithActor(r.Context(), actor)
		args := request.Params.Arguments
		var result map[string]any
		var err error
		switch request.Params.Name {
		case "eval_list_knowledge_refs":
			result, err = reader.ListKnowledgeRefs(callCtx, teamID.String(), fmt.Sprint(args["type"]), 500, pairArgumentString(args, "cursor"), "", false)
		case "eval_run_recall_case":
			encoded, _ := json.Marshal(args)
			var query recall.RecallRequest
			err = json.Unmarshal(encoded, &query)
			if err == nil {
				result, err = reader.RunRecallCase(callCtx, pairArgumentString(args, "case_id"), query, "", "", true)
			}
		default:
			tool, found := reg.Get(request.Params.Name)
			if !found {
				http.Error(w, "unknown tool", 404)
				return
			}
			result, err = tool.Invoke(callCtx, ownerID.String(), args)
		}
		if err != nil {
			if failure, ok := registry.ToolResultFromError(err); ok {
				result = failure.Result
			} else {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": registry.ToolCallerResponse(result, err != nil)})
	}))
	t.Cleanup(mcp.Close)
	portal, err := densehttp.NewControlPortalServerWithCapabilityBindings(&config.Config{ControlPortalToken: "paired-control"}, nil, nil, nil, densehttp.ControlPortalBindings{Telemetry: densehttp.ControlPortalTelemetry{Ontology: maintenance, Config: configuration, Communities: projections, Convergence: searchapp.NewSearchConvergenceService(search, store)}}, densehttp.HealthConfig{}, nil)
	require.NoError(t, err)
	control := httptest.NewServer(portal)
	t.Cleanup(control.Close)
	r := evalharness.PairRelease{Release: side, Protocol: "adr0014", Scope: evalharness.PairScope{TeamID: teamID.String(), OwnerID: ownerID.String()}, Capacity: evalharness.PairCapacity{Global: 4, Team: 4, ContextWindowTokens: 300000}, Models: evalharness.PairModels{Provider: "test", EmbeddingModel: model, EmbeddingDimensions: dimensions, EmbeddingIndexStrategy: "exact", Ontology: "paired-ontology", BackingRevision: "unknown"}, Runtime: evalharness.PairRuntime{CredentialID: ownerID.String(), BaseURL: mcp.URL, APIKeyEnv: "PAIR_FIXTURE_API_KEY"}}
	require.NoError(t, admin.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=? AND kind='team_shared'`, teamID).Row().Scan(&r.Scope.SpaceID, &r.Scope.Generation))
	return &pairedServiceFixture{admin: admin, release: r, mcp: mcp, control: control, maintenance: maintenance, projections: projections, provider: provider, reconciliation: searchapp.NewSearchReconciliationService(searchapp.SearchReconciliationDependencies{Repository: search, Projection: store, Provider: embedder})}
}

func pairArgumentString(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return value
}

func pairedOrganizationResponse(request assessment.Request) assessment.Response {
	response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{}, Items: []assessment.Decision{}, Equivalence: []assessment.Equivalence{}}
	created := map[string]string{}
	for _, item := range request.Items {
		kind, key, base := ontology.Topic, "paired-storage", ""
		if item.Kind == ontology.EntitySource {
			kind, key, base = ontology.EntityClass, item.EntityKind, item.EntityKind
		}
		if item.Kind == ontology.PredicateSource {
			kind, key = ontology.PredicateConcept, "paired-predicate"
		}
		ref := item.LockedDefinitionRef
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
				response.Definitions = append(response.Definitions, assessment.Definition{Ref: ref, Kind: kind, Key: key, Label: key, Description: "Paired fixture classification", Aliases: []string{}, BaseEntityKind: base})
			}
		}
		response.Items = append(response.Items, assessment.Decision{Ref: item.Ref, Status: "classified", DefinitionRef: ref})
	}
	for _, pair := range request.Pairs {
		relation := pair.RequiredRelation
		if relation == "" {
			relation = "distinct"
		}
		response.Equivalence = append(response.Equivalence, assessment.Equivalence{Ref: pair.Ref, Relation: relation})
	}
	return response
}

func (f *pairedServiceFixture) snapshot(t *testing.T) evalharness.PairNativeSnapshot {
	t.Helper()
	pool, err := f.admin.DB()
	require.NoError(t, err)
	args := []any{f.release.Scope.TeamID, f.release.Scope.OwnerID, f.release.Scope.SpaceID, f.release.Scope.Generation, f.release.Runtime.CredentialID}
	db := map[string]json.RawMessage{}
	for _, query := range []string{evalharness.PairImportSnapshotSQL, evalharness.PairOntologySnapshotSQL} {
		var raw []byte
		require.NoError(t, pool.QueryRowContext(context.Background(), query, args...).Scan(&raw))
		var part map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &part))
		for k, v := range part {
			db[k] = v
		}
	}
	encoded, err := json.Marshal(db)
	require.NoError(t, err)

	get := func(path string) json.RawMessage {
		code, raw := f.controlResponse(t, path)
		require.Equal(t, http.StatusOK, code)
		var body struct {
			Data json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(raw, &body))
		return body.Data
	}

	return evalharness.PairNativeSnapshot{Database: encoded, Ontology: get("/ontology/status"), Search: get("/search/convergence"), Community: get("/teams/" + f.release.Scope.TeamID + "/community/status")}
}

func (f *pairedServiceFixture) controlResponse(t *testing.T, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.control.URL+"/control/api"+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer paired-control")
	response, err := f.control.Client().Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, body
}
