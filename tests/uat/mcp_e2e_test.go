//go:build uat

package uat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpserver "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/claimdedupe"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentdedupe"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
	neo4jstorage "github.com/markhuangai/dense-mem/internal/storage/neo4j"
	pgclient "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
)

const testEmbeddingDimensions = 1536

type fixedEmbeddingProvider struct {
	model string
	dims  int
}

func (p *fixedEmbeddingProvider) Embed(_ context.Context, _ string) ([]float32, string, error) {
	return make([]float32, p.dims), p.model, nil
}

func (p *fixedEmbeddingProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, string, error) {
	out := make([][]float32, 0, len(texts))
	for range texts {
		out = append(out, make([]float32, p.dims))
	}
	return out, p.model, nil
}

func (p *fixedEmbeddingProvider) ModelName() string { return p.model }
func (p *fixedEmbeddingProvider) Dimensions() int   { return p.dims }
func (p *fixedEmbeddingProvider) IsAvailable() bool { return true }

type mcpHTTPClient struct {
	baseURL string
	apiKey  string
	nextID  int
}

func (c *mcpHTTPClient) call(t *testing.T, method string, params any) map[string]any {
	t.Helper()

	c.nextID++
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}

	data, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/mcp", bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var out map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(payload), &out); err != nil {
		t.Fatalf("decode mcp response: %v\nbody=%s", err, string(payload))
	}
	return out
}

func toolResult(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()

	if errPayload, ok := resp["error"]; ok {
		t.Fatalf("unexpected mcp error: %#v", errPayload)
	}

	result, ok := resp["result"].(map[string]any)
	require.True(t, ok, "result should be an object")

	content, ok := result["content"].([]any)
	require.True(t, ok, "result.content should be present")
	require.NotEmpty(t, content, "result.content should contain one text payload")

	first, ok := content[0].(map[string]any)
	require.True(t, ok, "content item should be an object")

	text, ok := first["text"].(string)
	require.True(t, ok, "content[0].text should be a string")

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	return payload
}

func startWritableMemoryServer(t *testing.T, ctx context.Context, env *TestEnv) (*httptest.Server, fragmentservice.GetFragmentService) {
	t.Helper()

	cfgProvider := env.buildConfig()
	cfgProvider.aiAPIURL = "http://stub-embedding.local"
	cfgProvider.aiAPIKey = "stub-key"
	cfgProvider.aiEmbeddingModel = "test-embedding"
	cfgProvider.aiEmbeddingDimensions = testEmbeddingDimensions

	cfgConcrete := env.buildConfigConcrete()
	cfgConcrete.AIAPIURL = cfgProvider.aiAPIURL
	cfgConcrete.AIAPIKey = cfgProvider.aiAPIKey
	cfgConcrete.AIEmbeddingModel = cfgProvider.aiEmbeddingModel
	cfgConcrete.AIEmbeddingDimensions = cfgProvider.aiEmbeddingDimensions

	logger := observability.New(slog.LevelError)
	server := httpserver.NewServer(cfgConcrete, logger, httpserver.HealthConfig{})

	profileScopeEnforcer := neo4jstorage.NewProfileScopeEnforcer(env.neo4jClient)
	readerAdapter := &scopedReaderAdapter{inner: profileScopeEnforcer}
	fragmentAuditor := &fragmentAuditAdapter{inner: env.auditService}
	lookup := fragmentdedupe.NewNeo4jDedupeLookup(readerAdapter)
	claimDedupeLookup := claimdedupe.NewNeo4jDedupeLookup(readerAdapter)
	consistency := service.NewEmbeddingConsistencyService(pgclient.NewEmbeddingConfigRepository(env.db), cfgProvider)
	skillPackLedger := repository.NewSkillPackImportRepository(env.db, pgclient.NewRLS())

	embedder := &fixedEmbeddingProvider{
		model: cfgProvider.aiEmbeddingModel,
		dims:  cfgProvider.aiEmbeddingDimensions,
	}

	fragmentCreateSvc := fragmentservice.NewCreateFragmentService(
		embedder,
		profileScopeEnforcer,
		lookup,
		fragmentAuditor,
		consistency,
		slog.Default(),
		nil,
	)
	fragmentGetSvc := fragmentservice.NewGetFragmentService(readerAdapter)
	fragmentListSvc := fragmentservice.NewListFragmentsService(readerAdapter)
	claimCreateSvc := claimservice.NewCreateClaimService(
		claimDedupeLookup,
		profileScopeEnforcer,
		profileScopeEnforcer,
		nil,
		slog.Default(),
		nil,
	)
	claimGetSvc := claimservice.NewGetClaimService(profileScopeEnforcer, slog.Default())
	claimListSvc := claimservice.NewListClaimsService(profileScopeEnforcer)
	factPromoteSvc := factservice.NewPromoteClaimService(
		profileScopeEnforcer,
		pgclient.NewClaimLock(nil),
		env.db,
		nil,
		slog.Default(),
		nil,
		0,
	)
	factGetSvc := factservice.NewGetFactService(profileScopeEnforcer)
	factListSvc := factservice.NewListFactsService(profileScopeEnforcer)
	placementRepo := repository.NewMemoryPlacementRepository(env.db, pgclient.NewRLS())
	memorySvc := memoryservice.New(memoryservice.Dependencies{
		FragmentCreate: fragmentCreateSvc,
		ClaimCreate:    claimCreateSvc,
		ClaimGet:       claimGetSvc,
		ClaimList:      claimListSvc,
		FactPromote:    factPromoteSvc,
		FactList:       factListSvc,
		PlacementStore: placementRepo,
		Logger:         logger.Slog(),
	})
	workerCtx, workerCancel := context.WithCancel(ctx)
	t.Cleanup(workerCancel)
	memorySvc.StartPlacementWorker(workerCtx, time.Second)
	skillPackSvc := skillpackservice.New(skillpackservice.Dependencies{
		FragmentCreate: fragmentCreateSvc,
		ClaimCreate:    claimCreateSvc,
		ClaimGet:       claimGetSvc,
		ClaimList:      claimListSvc,
		FactPromote:    factPromoteSvc,
		FactGet:        factGetSvc,
		FactList:       factListSvc,
		Graph:          profileScopeEnforcer,
		Ledger:         skillPackLedger,
		HistoryDays:    30,
	})

	reg, err := registry.BuildDefault(registry.Dependencies{
		FragmentList: fragmentListSvc,
		ClaimGet:     claimGetSvc,
		ClaimList:    claimListSvc,
		FactGet:      factGetSvc,
		FactList:     factListSvc,
		Memory:       memorySvc,
		SkillPack:    skillPackSvc,
	})
	require.NoError(t, err)

	deps := httpserver.ProtectedDeps{
		APIKeyRepo:       env.apiKeyRepo,
		ProfileService:   env.profileSvc,
		ProfileSvc:       env.profileSvc,
		RateLimitService: env.rateLimitSvc,
		AuditService:     env.auditService,
		Config:           cfgProvider,
		Logger:           logger,
	}

	handlers := httpserver.ProtectedHandlers{
		APIKeySvc:   env.apiKeySvc,
		ToolCatalog: handler.NewToolCatalogHandler(reg).Handle,
		MCPPost:     handler.NewMCPHandler(reg, logger).HandlePost,
		MCPGet:      handler.NewMCPHandler(reg, logger).HandleGet,
	}

	httpserver.RegisterProtectedRoutesWithHandlers(server, deps, handlers)
	httptestServer := httptest.NewServer(server)
	t.Cleanup(httptestServer.Close)

	return httptestServer, fragmentGetSvc
}

func createProfileAndKey(t *testing.T, ctx context.Context, env *TestEnv) (string, string) {
	t.Helper()

	profile, err := env.profileSvc.Create(ctx, service.CreateProfileRequest{
		Name:        fmt.Sprintf("uat-mcp-%d", time.Now().UnixNano()),
		Description: "MCP runtime verification profile",
	}, nil, "operator", "", "uat-mcp-runtime")
	require.NoError(t, err)

	_, rawKey, err := env.apiKeySvc.CreateStandardKey(ctx, profile.ID, service.CreateAPIKeyRequest{
		RateLimit: 0,
	}, nil, "operator", "", "uat-mcp-runtime")
	require.NoError(t, err)

	return profile.ID.String(), rawKey
}

func TestUATMCPRuntime_RememberPersistsAndReadsBack(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	env, cleanup := SetupTestEnv(t, ctx)
	defer cleanup()

	profileID, rawAPIKey := createProfileAndKey(t, ctx, env)
	serverURL, fragmentGetSvc := startWritableMemoryServer(t, ctx, env)
	mcp := &mcpHTTPClient{baseURL: serverURL.URL, apiKey: rawAPIKey}

	initResp := mcp.call(t, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "uat-mcp-runtime",
			"version": "1.0.0",
		},
	})
	require.NotNil(t, initResp["result"], "initialize must succeed")

	rememberResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
		"name": "remember",
		"arguments": map[string]any{
			"evidence": []map[string]any{{
				"content":         "MCP persisted memory for runtime verification.",
				"idempotency_key": "uat-mcp-runtime-remember",
				"labels":          []string{"uat", "mcp"},
				"metadata": map[string]any{
					"origin": "uat",
				},
			}},
		},
	}))
	require.Equal(t, "queued", rememberResp["status"])
	ingestID, ok := rememberResp["ingest_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, ingestID)
	evidenceResp, ok := rememberResp["evidence"].([]any)
	require.True(t, ok)
	require.Len(t, evidenceResp, 1)
	fragmentResp, ok := evidenceResp[0].(map[string]any)
	require.True(t, ok)

	fragmentID, ok := fragmentResp["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, fragmentID)

	stored, err := fragmentGetSvc.GetByID(ctx, profileID, fragmentID)
	require.NoError(t, err)
	require.Equal(t, "MCP persisted memory for runtime verification.", stored.Content)
	require.Equal(t, "primary", string(stored.Authority))

	require.Eventually(t, func() bool {
		statusResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
			"name":      "get_memory_placement",
			"arguments": map[string]any{"ingest_id": ingestID},
		}))
		placement, ok := statusResp["placement"].(map[string]any)
		return ok && placement["status"] == "completed"
	}, 5*time.Second, 250*time.Millisecond)

	dupResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
		"name": "remember",
		"arguments": map[string]any{
			"evidence": []map[string]any{{
				"content":         "MCP persisted memory for runtime verification.",
				"idempotency_key": "uat-mcp-runtime-remember",
			}},
		},
	}))
	dupEvidence, ok := dupResp["evidence"].([]any)
	require.True(t, ok)
	dupFragment, ok := dupEvidence[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "duplicate", dupFragment["status"])
	require.Equal(t, fragmentID, dupFragment["duplicate_of"])
}

func TestUATMCPRuntime_MemoryPackReviewImportAndRollback(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	env, cleanup := SetupTestEnv(t, ctx)
	defer cleanup()

	profileID, rawAPIKey := createProfileAndKey(t, ctx, env)
	serverURL, _ := startWritableMemoryServer(t, ctx, env)
	mcp := &mcpHTTPClient{baseURL: serverURL.URL, apiKey: rawAPIKey}

	initResp := mcp.call(t, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "uat-memory-pack",
			"version": "1.0.0",
		},
	})
	require.NotNil(t, initResp["result"], "initialize must succeed")

	toolsResp := mcp.call(t, "tools/list", map[string]any{})
	result, ok := toolsResp["result"].(map[string]any)
	require.True(t, ok)
	tools, ok := result["tools"].([]any)
	require.True(t, ok)
	toolNames := make(map[string]struct{}, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		require.True(t, ok)
		name, ok := tool["name"].(string)
		require.True(t, ok)
		toolNames[name] = struct{}{}
	}
	for _, name := range []string{
		"export_memory_pack",
		"inspect_memory_pack",
		"import_memory_pack",
		"rollback_memory_pack_import",
	} {
		_, ok := toolNames[name]
		require.True(t, ok, "MCP tool %s should be exposed", name)
	}

	exportResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
		"name": "export_memory_pack",
		"arguments": map[string]any{
			"name": "UAT memory pack",
			"manual_items": []map[string]any{{
				"subject":     "assistant",
				"predicate":   "has_skill",
				"object":      "imports and rolls back memory packs through MCP",
				"source_kind": "manual",
			}},
		},
	}))
	artifactJSON, ok := exportResp["canonical_json"].(string)
	require.True(t, ok)
	require.NotEmpty(t, artifactJSON)
	artifactHash, ok := exportResp["sha256"].(string)
	require.True(t, ok)
	require.Len(t, artifactHash, 64)

	inspectResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
		"name": "inspect_memory_pack",
		"arguments": map[string]any{
			"artifact_json":   artifactJSON,
			"expected_sha256": artifactHash,
		},
	}))
	require.Equal(t, artifactHash, inspectResp["artifact_hash"])
	inspectItems, ok := inspectResp["items"].([]any)
	require.True(t, ok)
	require.Len(t, inspectItems, 1)
	firstInspect, ok := inspectItems[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "new", firstInspect["status"])

	importResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
		"name": "import_memory_pack",
		"arguments": map[string]any{
			"artifact_json":   artifactJSON,
			"expected_sha256": artifactHash,
			"mode":            "review",
		},
	}))
	require.Equal(t, "applied", importResp["status"])
	importID, ok := importResp["import_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, importID)
	require.Equal(t, float64(1), importResp["applied_count"])

	importItems, ok := importResp["items"].([]any)
	require.True(t, ok)
	require.Len(t, importItems, 1)
	firstImport, ok := importItems[0].(map[string]any)
	require.True(t, ok)
	claimID, ok := firstImport["claim_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, claimID)

	profileScopeEnforcer := neo4jstorage.NewProfileScopeEnforcer(env.neo4jClient)
	_, claimRows, err := profileScopeEnforcer.ScopedRead(ctx, profileID, `
		MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
		RETURN c.import_id AS import_id, c.status AS status
	`, map[string]any{"claimId": claimID})
	require.NoError(t, err)
	require.Len(t, claimRows, 1)
	require.Equal(t, importID, claimRows[0]["import_id"])
	require.Equal(t, "candidate", claimRows[0]["status"])

	ledger := repository.NewSkillPackImportRepository(env.db, pgclient.NewRLS())
	record, err := ledger.GetImport(ctx, profileID, importID)
	require.NoError(t, err)
	require.Equal(t, "applied", record.Status)
	require.Equal(t, artifactHash, record.ArtifactHash)

	rollbackResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
		"name": "rollback_memory_pack_import",
		"arguments": map[string]any{
			"import_id": importID,
		},
	}))
	require.Equalf(t, "rolled_back", rollbackResp["status"], "conflicts: %v", rollbackResp["conflicts"])
	require.GreaterOrEqual(t, int(rollbackResp["reverted_count"].(float64)), 2)

	_, claimRows, err = profileScopeEnforcer.ScopedRead(ctx, profileID, `
		MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
		RETURN c.claim_id AS claim_id
	`, map[string]any{"claimId": claimID})
	require.NoError(t, err)
	require.Empty(t, claimRows)

	_, fragmentRows, err := profileScopeEnforcer.ScopedRead(ctx, profileID, `
		MATCH (sf:SourceFragment {team_id: $profileId, import_id: $importId})
		RETURN sf.fragment_id AS fragment_id
	`, map[string]any{"importId": importID})
	require.NoError(t, err)
	require.Empty(t, fragmentRows)

	record, err = ledger.GetImport(ctx, profileID, importID)
	require.NoError(t, err)
	require.Equal(t, "rolled_back", record.Status)
}

func TestUATMCPRuntime_MemoryPackTrustedImportValidatesPromotesAndRollback(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	env, cleanup := SetupTestEnv(t, ctx)
	defer cleanup()

	profileID, rawAPIKey := createProfileAndKey(t, ctx, env)
	serverURL, _ := startWritableMemoryServer(t, ctx, env)
	mcp := &mcpHTTPClient{baseURL: serverURL.URL, apiKey: rawAPIKey}
	profileScopeEnforcer := neo4jstorage.NewProfileScopeEnforcer(env.neo4jClient)
	ledger := repository.NewSkillPackImportRepository(env.db, pgclient.NewRLS())

	initResp := mcp.call(t, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "uat-memory-pack-trusted",
			"version": "1.0.0",
		},
	})
	require.NotNil(t, initResp["result"], "initialize must succeed")

	t.Run("trusted manual item validates claim and rolls back", func(t *testing.T) {
		exportResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
			"name": "export_memory_pack",
			"arguments": map[string]any{
				"name": "Trusted validation memory pack",
				"manual_items": []map[string]any{{
					"subject":     "assistant",
					"predicate":   "has_skill",
					"object":      "trusted validation through memory packs",
					"source_kind": "manual",
				}},
			},
		}))
		artifactJSON, ok := exportResp["canonical_json"].(string)
		require.True(t, ok)
		artifactHash, ok := exportResp["sha256"].(string)
		require.True(t, ok)

		importResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
			"name": "import_memory_pack",
			"arguments": map[string]any{
				"artifact_json":   artifactJSON,
				"expected_sha256": artifactHash,
				"mode":            "trusted",
			},
		}))
		require.Equal(t, "applied", importResp["status"])
		require.Equal(t, float64(1), importResp["applied_count"])
		importID, ok := importResp["import_id"].(string)
		require.True(t, ok)

		importItems, ok := importResp["items"].([]any)
		require.True(t, ok)
		require.Len(t, importItems, 1)
		firstImport, ok := importItems[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "validated", firstImport["status"])
		claimID, ok := firstImport["claim_id"].(string)
		require.True(t, ok)
		require.NotEmpty(t, claimID)

		_, claimRows, err := profileScopeEnforcer.ScopedRead(ctx, profileID, `
			MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
			RETURN c.import_id AS import_id,
			       c.status AS status,
			       c.entailment_verdict AS entailment_verdict,
			       c.verifier_model AS verifier_model
		`, map[string]any{"claimId": claimID})
		require.NoError(t, err)
		require.Len(t, claimRows, 1)
		require.Equal(t, importID, claimRows[0]["import_id"])
		require.Equal(t, "validated", claimRows[0]["status"])
		require.Equal(t, "entailed", claimRows[0]["entailment_verdict"])
		require.Equal(t, "memory_pack.source_trust", claimRows[0]["verifier_model"])

		record, err := ledger.GetImport(ctx, profileID, importID)
		require.NoError(t, err)
		require.Equal(t, "applied", record.Status)
		require.Equal(t, "trusted", record.Mode)

		rollbackResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
			"name": "rollback_memory_pack_import",
			"arguments": map[string]any{
				"import_id": importID,
			},
		}))
		require.Equalf(t, "rolled_back", rollbackResp["status"], "conflicts: %v", rollbackResp["conflicts"])
		require.GreaterOrEqual(t, int(rollbackResp["reverted_count"].(float64)), 2)

		_, claimRows, err = profileScopeEnforcer.ScopedRead(ctx, profileID, `
			MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
			RETURN c.claim_id AS claim_id
		`, map[string]any{"claimId": claimID})
		require.NoError(t, err)
		require.Empty(t, claimRows)

		_, fragmentRows, err := profileScopeEnforcer.ScopedRead(ctx, profileID, `
			MATCH (sf:SourceFragment {team_id: $profileId, import_id: $importId})
			RETURN sf.fragment_id AS fragment_id
		`, map[string]any{"importId": importID})
		require.NoError(t, err)
		require.Empty(t, fragmentRows)

		record, err = ledger.GetImport(ctx, profileID, importID)
		require.NoError(t, err)
		require.Equal(t, "rolled_back", record.Status)
	})

	t.Run("trusted source fact promotes fact, exports candidate, and rolls back", func(t *testing.T) {
		exportResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
			"name": "export_memory_pack",
			"arguments": map[string]any{
				"name": "Trusted fact memory pack",
				"manual_items": []map[string]any{{
					"subject":     "assistant",
					"predicate":   "has_skill",
					"object":      "trusted fact promotion through memory packs",
					"source_kind": "source_fact",
				}},
			},
		}))
		artifactJSON, ok := exportResp["canonical_json"].(string)
		require.True(t, ok)
		artifactHash, ok := exportResp["sha256"].(string)
		require.True(t, ok)

		importResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
			"name": "import_memory_pack",
			"arguments": map[string]any{
				"artifact_json":   artifactJSON,
				"expected_sha256": artifactHash,
				"mode":            "trusted",
			},
		}))
		require.Equal(t, "applied", importResp["status"])
		importID, ok := importResp["import_id"].(string)
		require.True(t, ok)

		importItems, ok := importResp["items"].([]any)
		require.True(t, ok)
		require.Len(t, importItems, 1)
		firstImport, ok := importItems[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "promoted", firstImport["status"])
		claimID, ok := firstImport["claim_id"].(string)
		require.True(t, ok)
		factID, ok := firstImport["fact_id"].(string)
		require.True(t, ok)
		require.NotEmpty(t, factID)

		_, claimRows, err := profileScopeEnforcer.ScopedRead(ctx, profileID, `
			MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
			RETURN c.import_id AS import_id,
			       c.status AS status,
			       c.entailment_verdict AS entailment_verdict,
			       c.verifier_model AS verifier_model
		`, map[string]any{"claimId": claimID})
		require.NoError(t, err)
		require.Len(t, claimRows, 1)
		require.Equal(t, importID, claimRows[0]["import_id"])
		require.Equal(t, "superseded", claimRows[0]["status"])
		require.Equal(t, "entailed", claimRows[0]["entailment_verdict"])
		require.Equal(t, "memory_pack.source_trust", claimRows[0]["verifier_model"])

		_, factRows, err := profileScopeEnforcer.ScopedRead(ctx, profileID, `
			MATCH (f:Fact {team_id: $profileId, fact_id: $factId})
			RETURN f.import_id AS import_id,
			       f.status AS status,
			       f.promoted_from_claim_id AS promoted_from_claim_id,
			       f.subject AS subject,
			       f.predicate AS predicate,
			       f.object AS object
		`, map[string]any{"factId": factID})
		require.NoError(t, err)
		require.Len(t, factRows, 1)
		require.Equal(t, importID, factRows[0]["import_id"])
		require.Equal(t, "active", factRows[0]["status"])
		require.Equal(t, claimID, factRows[0]["promoted_from_claim_id"])
		require.Equal(t, "assistant", factRows[0]["subject"])
		require.Equal(t, "has_skill", factRows[0]["predicate"])
		require.Equal(t, "trusted fact promotion through memory packs", factRows[0]["object"])

		candidatesResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
			"name": "find_memory_pack_candidates",
			"arguments": map[string]any{
				"query": "trusted fact promotion",
				"limit": 5,
			},
		}))
		candidates, ok := candidatesResp["candidates"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, candidates)
		firstCandidate, ok := candidates[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, factID, firstCandidate["id"])
		require.Equal(t, "fact", firstCandidate["type"])

		factExportResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
			"name": "export_memory_pack",
			"arguments": map[string]any{
				"name":     "Export promoted trusted fact",
				"fact_ids": []string{factID},
			},
		}))
		factArtifactJSON, ok := factExportResp["canonical_json"].(string)
		require.True(t, ok)
		var factArtifact map[string]any
		require.NoError(t, json.Unmarshal([]byte(factArtifactJSON), &factArtifact))
		items, ok := factArtifact["items"].([]any)
		require.True(t, ok)
		require.Len(t, items, 1)
		firstItem, ok := items[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "source_fact", firstItem["source_kind"])
		require.Equal(t, "trusted fact promotion through memory packs", firstItem["object"])

		record, err := ledger.GetImport(ctx, profileID, importID)
		require.NoError(t, err)
		require.Equal(t, "applied", record.Status)
		require.Equal(t, "trusted", record.Mode)

		rollbackResp := toolResult(t, mcp.call(t, "tools/call", map[string]any{
			"name": "rollback_memory_pack_import",
			"arguments": map[string]any{
				"import_id": importID,
			},
		}))
		require.Equalf(t, "rolled_back", rollbackResp["status"], "conflicts: %v", rollbackResp["conflicts"])
		require.GreaterOrEqual(t, int(rollbackResp["reverted_count"].(float64)), 3)

		_, claimRows, err = profileScopeEnforcer.ScopedRead(ctx, profileID, `
			MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
			RETURN c.claim_id AS claim_id
		`, map[string]any{"claimId": claimID})
		require.NoError(t, err)
		require.Empty(t, claimRows)

		_, factRows, err = profileScopeEnforcer.ScopedRead(ctx, profileID, `
			MATCH (f:Fact {team_id: $profileId, fact_id: $factId})
			RETURN f.fact_id AS fact_id
		`, map[string]any{"factId": factID})
		require.NoError(t, err)
		require.Empty(t, factRows)

		_, fragmentRows, err := profileScopeEnforcer.ScopedRead(ctx, profileID, `
			MATCH (sf:SourceFragment {team_id: $profileId, import_id: $importId})
			RETURN sf.fragment_id AS fragment_id
		`, map[string]any{"importId": importID})
		require.NoError(t, err)
		require.Empty(t, fragmentRows)

		record, err = ledger.GetImport(ctx, profileID, importID)
		require.NoError(t, err)
		require.Equal(t, "rolled_back", record.Status)
	})
}
