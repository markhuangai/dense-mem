//go:build integration

package serverapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	rememberprocessor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type progressPostgresAssessor struct {
	rememberProcessorIntegrationAssessor
	calls atomic.Int32
}

func (p *progressPostgresAssessor) Assess(ctx context.Context, request assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	p.calls.Add(1)
	for _, evidence := range request.Evidence {
		if strings.Contains(evidence.Content, "[progress:assessment]") {
			return nil, assessor.SemanticAssessmentTurn{}, errors.New("progress fixture assessment transport unavailable")
		}
	}
	return p.rememberProcessorIntegrationAssessor.Assess(ctx, request)
}

type progressPostgresEmbedder struct {
	rememberProcessorIntegrationSearchEmbedder
	calls atomic.Int32
}

func (p *progressPostgresEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error) {
	p.calls.Add(1)
	for _, text := range texts {
		if strings.Contains(text, "[progress:embedding]") {
			return nil, "", errors.New("progress fixture embedding transport unavailable")
		}
	}
	return p.rememberProcessorIntegrationSearchEmbedder.EmbedBatch(ctx, texts)
}

type postgresProgressFrame struct {
	Method string `json:"method"`
	Params struct {
		Token    json.RawMessage `json:"progressToken"`
		Progress int             `json:"progress"`
		Message  string          `json:"message"`
	} `json:"params"`
	Result *struct {
		IsError bool                               `json:"isError"`
		Status  rememberapp.SubmissionStatusResult `json:"structuredContent"`
	} `json:"result"`
}

func TestRememberProgressPostgres(t *testing.T) {
	admin, app, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	t.Cleanup(cleanup)
	model, dimensions := ensureRememberProcessorIntegrationSearchContract(t, admin, rls)
	provider := &progressPostgresAssessor{}
	embedder := &progressPostgresEmbedder{rememberProcessorIntegrationSearchEmbedder: rememberProcessorIntegrationSearchEmbedder{model: model, dimensions: dimensions}}
	processor := rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{
		Ledger: knowledgepostgres.NewStore(app, rls), Assessor: provider,
		EmbeddingExecutor: semanticwrite.NewProviderExecutor(embedder), Limits: assessor.DefaultSemanticAssessmentLimits(),
		CommitFailureStage: knowledgepostgres.RememberCommitFailureStage,
	})
	service := rememberapp.NewService(rememberapp.Dependencies{Synchronous: processor})
	reg, err := registry.BuildActive(registry.Dependencies{RememberBindings: registry.RememberBindings{Service: service}})
	require.NoError(t, err)
	actors := map[string]requestctx.Actor{}
	newActor := func(teamID uuid.UUID) string {
		if teamID == uuid.Nil {
			teamID = uuid.New()
			require.NoError(t, accesspostgres.NewTeamRepository(admin, rls).Create(t.Context(), &domain.Team{ID: teamID, Name: "progress-" + teamID.String()}))
		}
		id := uuid.New()
		require.NoError(t, accesspostgres.NewCredentialRepository(admin, rls, nil).CreateCredential(t.Context(), &domain.Credential{
			ID: id, TeamID: teamID, Name: "progress actor " + id.String(), Role: "member", KeyHash: id.String(),
			KeyPrefix: strings.ReplaceAll(id.String(), "-", "")[:24], KeySuffix: "test", Scopes: []string{"read", "write"},
		}))
		ctx := rememberProcessorActorContext(t, t.Context(), admin, rls, teamID, id)
		actor, ok := requestctx.ActorFromContext(ctx)
		require.True(t, ok)
		actors[id.String()] = actor
		return id.String()
	}
	e := echo.New()
	mcpHandler := handler.NewMCPHandler(reg, nil)
	e.POST("/mcp", mcpHandler.HandlePost)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actors[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ctx := requestctx.WithActor(r.Context(), actor)
		ctx = requestctx.WithRememberInvocationIDSink(ctx)
		ctx = correlation.WithID(ctx, r.Header.Get("X-Correlation-ID"))
		ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{TeamID: actor.TeamID, OwnerID: actor.OwnerID,
			Grants: actor.Grants, AllowedSpaces: actor.AllowedSpaces})
		ctx = middleware.SetResolvedTeamIDForTest(ctx, actor.TeamID)
		e.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(server.Close)

	function := "remember_progress_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec(fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF position('[progress:commit]' IN NEW.content) > 0 THEN
				RAISE EXCEPTION 'progress fixture semantic commit failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE INSERT ON evidence_fragments FOR EACH ROW EXECUTE FUNCTION %s();
	`, function, function, function)).Error)
	t.Cleanup(func() {
		require.NoError(t, admin.Exec(fmt.Sprintf("DROP TRIGGER %s ON evidence_fragments; DROP FUNCTION %s()", function, function)).Error)
	})

	call := func(t *testing.T, credential string, request rememberapp.RememberRequest, token string, jsonMode bool) []postgresProgressFrame {
		t.Helper()
		meta := map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
		if token != "" {
			meta["progressToken"] = json.RawMessage(token)
		}
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": uuid.NewString(), "method": "tools/call",
			"params": map[string]any{"name": "remember", "arguments": request, "_meta": meta}})
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/mcp", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if jsonMode {
			req.Header.Set("Accept", "application/json")
		}
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", "remember")
		req.Header.Set("X-Correlation-ID", uuid.NewString())
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		var frames []postgresProgressFrame
		if jsonMode {
			var frame postgresProgressFrame
			require.NoError(t, json.NewDecoder(response.Body).Decode(&frame))
			return append(frames, frame)
		}
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				var frame postgresProgressFrame
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame))
				frames = append(frames, frame)
			}
		}
		require.NoError(t, scanner.Err())
		return frames
	}

	for _, fault := range []string{"success", "assessment", "embedding", "commit"} {
		t.Run(fault, func(t *testing.T) {
			credential := newActor(uuid.Nil)
			actor := actors[credential]
			request := rememberapp.RememberRequest{TargetSpace: domain.MemorySpacePrivate, IdempotencyKey: uuid.NewString(),
				Evidence: []rememberapp.RememberEvidenceInput{{Content: "Exact Evidence [progress:" + fault + "] " + uuid.NewString(), ForceInsert: true, SourceType: "manual"}}}
			frames := call(t, credential, request, "9007199254740993", false)
			want := 6
			if fault == "assessment" {
				want = 4
			}
			if fault == "embedding" {
				want = 5
			}
			require.Len(t, frames, want)
			for index, frame := range frames[:len(frames)-1] {
				require.Equal(t, "notifications/progress", frame.Method)
				require.Equal(t, "9007199254740993", string(frame.Params.Token))
				require.Equal(t, index+1, frame.Params.Progress)
				require.NotContains(t, frame.Params.Message, "Exact Evidence")
			}
			final := frames[len(frames)-1].Result
			require.NotNil(t, final)
			require.NotEmpty(t, final.Status.SubmissionID)
			if fault != "success" {
				require.True(t, final.IsError)
				require.Equal(t, "failed", final.Status.ProcessingState)
				code := map[string]string{"assessment": "provider_unavailable", "embedding": "embedding_unavailable", "commit": "database_failure"}[fault]
				require.Equal(t, code, final.Status.Errors[0].Code)
				require.NotEmpty(t, final.Status.Errors[0].Remediation)
				assertRememberProcessorIntegrationCanonicalCounts(t, admin, rls, actor.TeamID, 0)
				return
			}
			require.Equal(t, "completed", final.Status.ProcessingState)
			assertRememberProcessorIntegrationCanonicalCounts(t, admin, rls, actor.TeamID, 1)
			providerCalls, embeddingCalls := provider.calls.Load(), embedder.calls.Load()
			for _, jsonMode := range []bool{false, true} {
				replay := call(t, credential, request, "0", jsonMode)
				require.Len(t, replay, 1)
				require.Equal(t, final.Status.SubmissionID, replay[0].Result.Status.SubmissionID)
			}
			require.Equal(t, providerCalls, provider.calls.Load())
			require.Equal(t, embeddingCalls, embedder.calls.Load())
			request.Evidence[0].Content = "Changed request under the retained key."
			conflict := call(t, credential, request, "0", false)
			require.Len(t, conflict, 1)
			require.Equal(t, "idempotency_conflict", conflict[0].Result.Status.Errors[0].Code)
			assertRememberProcessorIntegrationCanonicalCounts(t, admin, rls, actor.TeamID, 1)
		})
	}

	t.Run("fresh modes preserve outcomes and provider calls", func(t *testing.T) {
		credential := newActor(uuid.Nil)
		var baseline rememberapp.SubmissionStatusResult
		var baselineCalls [2]int32
		for index, mode := range []struct {
			token string
			json  bool
		}{{"0", false}, {"", false}, {"0", true}} {
			request := rememberapp.RememberRequest{TargetSpace: domain.MemorySpacePrivate, IdempotencyKey: uuid.NewString(),
				Evidence: []rememberapp.RememberEvidenceInput{{Content: "Fresh equivalent progress input " + uuid.NewString(), ForceInsert: true, SourceType: "manual"}}}
			before := [2]int32{provider.calls.Load(), embedder.calls.Load()}
			frames := call(t, credential, request, mode.token, mode.json)
			wantFrames := 1
			if index == 0 {
				wantFrames = 6
			}
			require.Len(t, frames, wantFrames)
			final := frames[len(frames)-1].Result
			require.NotNil(t, final)
			require.False(t, final.IsError)
			require.Equal(t, "completed", final.Status.ProcessingState)
			require.Empty(t, final.Status.Errors)
			require.Len(t, final.Status.Evidence, 1)
			calls := [2]int32{provider.calls.Load() - before[0], embedder.calls.Load() - before[1]}
			if index == 0 {
				baseline, baselineCalls = final.Status, calls
				require.Positive(t, calls[0])
				require.Positive(t, calls[1])
			} else {
				require.Equal(t, baselineCalls, calls, "fresh modes must make identical assessment and embedding calls")
				require.Equal(t, baseline.ContractVersion, final.Status.ContractVersion)
				require.Equal(t, baseline.SearchState, final.Status.SearchState)
				require.Equal(t, baseline.Evidence[0].Disposition, final.Status.Evidence[0].Disposition)
				require.Equal(t, baseline.Evidence[0].SearchState, final.Status.Evidence[0].SearchState)
			}
		}
	})

	t.Run("concurrent private A B C with equal token and key", func(t *testing.T) {
		a := newActor(uuid.Nil)
		b := newActor(actors[a].TeamID)
		c := newActor(uuid.Nil)
		key := uuid.NewString()
		var workers sync.WaitGroup
		for _, credential := range []string{a, b, c} {
			workers.Add(1)
			go func() {
				defer workers.Done()
				request := rememberapp.RememberRequest{TargetSpace: domain.MemorySpacePrivate, IdempotencyKey: key,
					Evidence: []rememberapp.RememberEvidenceInput{{Content: "Actor " + credential + " keeps exact private Evidence.", ForceInsert: true, SourceType: "manual"}}}
				frames := call(t, credential, request, "0", false)
				require.Len(t, frames, 6)
				final := frames[5].Result
				require.NotNil(t, final)
				require.Equal(t, "completed", final.Status.ProcessingState)
				var rows int64
				require.NoError(t, rls.WithSystemTx(t.Context(), admin, func(tx *gorm.DB) error {
					return tx.Table("evidence_fragments").Where("team_id = ? AND owner_profile_id = ? AND ingest_id = ?",
						actors[credential].TeamID, actors[credential].OwnerID, final.Status.SubmissionID).Count(&rows).Error
				}))
				require.EqualValues(t, 1, rows)
			}()
		}
		workers.Wait()
	})
}
