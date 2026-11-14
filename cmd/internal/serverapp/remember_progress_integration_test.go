//go:build integration

package serverapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/markhuangai/dense-mem/internal/embedding"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	calls         atomic.Int32
	gate          modelprovider.ConcurrencyGate
	gateOperation string
	entered       chan string
	release       chan struct{}
}

func (p *progressPostgresAssessor) Complete(ctx context.Context, r modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	if p.gate != nil && p.gateOperation == "partition" {
		if err := modelprovider.AcquireConcurrency(ctx, p.gate); err != nil {
			return modelprovider.StructuredResult{}, err
		}
		defer modelprovider.ReleaseConcurrency(p.gate)
	}
	if strings.Contains(regexp.MustCompile(`⟦[^⟧]+⟧`).ReplaceAllString(r.Messages[1].Content, ""), "[progress:block-partition]") {
		p.entered <- "partition"
		select {
		case <-p.release:
		case <-ctx.Done():
			return modelprovider.StructuredResult{}, ctx.Err()
		}
	}
	return partitionFixtureComplete(r)
}

func (p *progressPostgresAssessor) Assess(ctx context.Context, request assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	if p.gate != nil && p.gateOperation == "assessment" {
		if err := modelprovider.AcquireConcurrency(ctx, p.gate); err != nil {
			return nil, assessor.SemanticAssessmentTurn{}, err
		}
		defer modelprovider.ReleaseConcurrency(p.gate)
	}
	p.calls.Add(1)
	for _, evidence := range request.Evidence {
		if strings.Contains(evidence.Content, "[progress:block-assessment]") {
			p.entered <- "assessment"
			select {
			case <-p.release:
			case <-ctx.Done():
				return nil, assessor.SemanticAssessmentTurn{}, ctx.Err()
			}
		}
		if strings.Contains(evidence.Content, "[progress:assessment]") {
			return nil, assessor.SemanticAssessmentTurn{}, errors.New("progress fixture assessment transport unavailable")
		}
	}
	return p.rememberProcessorIntegrationAssessor.Assess(ctx, request)
}

type progressPostgresEmbedder struct {
	rememberProcessorIntegrationSearchEmbedder
	calls   atomic.Int32
	entered chan string
	release chan struct{}
	retries atomic.Int32
	gate    modelprovider.ConcurrencyGate
}

func (p *progressPostgresEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error) {
	if p.gate != nil {
		if err := modelprovider.AcquireConcurrency(ctx, p.gate); err != nil {
			return nil, "", err
		}
		defer modelprovider.ReleaseConcurrency(p.gate)
	}
	p.calls.Add(1)
	for _, text := range texts {
		if strings.Contains(text, "[progress:retry]") && p.retries.Add(1) == 1 {
			return nil, "", &embeddingcontract.RateLimitError{Provider: "controlled embedding", Message: "retry fixture", RetryAfter: 1}
		}
		if strings.Contains(text, "[progress:block-embedding]") {
			p.entered <- "embedding"
			select {
			case <-p.release:
			case <-ctx.Done():
				return nil, "", ctx.Err()
			}
		}
		if strings.Contains(text, "[progress:embedding]") {
			return nil, "", errors.New("progress fixture embedding transport unavailable")
		}
	}
	return p.rememberProcessorIntegrationSearchEmbedder.EmbedBatch(ctx, texts)
}

func TestRememberProgressPostgres(t *testing.T) {
	admin, app, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	t.Cleanup(cleanup)
	model, dimensions := ensureRememberProcessorIntegrationSearchContract(t, admin, rls)
	provider := &progressPostgresAssessor{}
	embedder := &progressPostgresEmbedder{rememberProcessorIntegrationSearchEmbedder: rememberProcessorIntegrationSearchEmbedder{model: model, dimensions: dimensions}}
	processor := rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{
		Ledger: knowledgepostgres.NewStore(app, rls), Assessor: provider,
		EmbeddingExecutor: semanticwrite.NewProviderExecutor(embedding.NewRetryEmbeddingProvider(embedder, nil)), Limits: assessor.DefaultSemanticAssessmentLimits(),
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

	call := func(t *testing.T, credential string, request rememberapp.RememberRequest, token string, jsonMode bool, observe ...func(postgresProgressFrame)) []postgresProgressFrame {
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
		frames, err = readPostgresProgressFrames(response.Body, observe...)
		require.NoError(t, err)
		return frames
	}

	for _, blocked := range []struct{ label, message string }{
		{"partition", "Partitioning submitted text"}, {"assessment", "Assessing Evidence"}, {"embedding", "Preparing search state"},
	} {
		t.Run("stream arrives during actual "+blocked.label, func(t *testing.T) {
			entered, release := make(chan string, 1), make(chan struct{})
			provider.entered, provider.release = entered, release
			embedder.entered, embedder.release = entered, release
			credential := newActor(uuid.Nil)
			request := rememberapp.RememberRequest{TargetSpace: domain.MemorySpacePrivate, IdempotencyKey: uuid.NewString(), Evidence: []rememberapp.RememberEvidenceInput{{Content: "Actual work [progress:block-" + blocked.label + "] " + uuid.NewString(), ForceInsert: true}}}
			observed := false
			frames := call(t, credential, request, `"actual-work"`, false, func(frame postgresProgressFrame) {
				if frame.Result != nil {
					require.True(t, observed, "terminal preceded blocked-stage observation")
				}
				if !strings.HasPrefix(frame.Params.Message, blocked.message) {
					return
				}
				select {
				case label := <-entered:
					require.Equal(t, blocked.label, label)
				case <-time.After(5 * time.Second):
					t.Fatal("actual dependency was not blocked")
				}
				observed = true
				close(release)
			})
			require.True(t, observed)
			require.Equal(t, "completed", frames[len(frames)-1].Result.Status.ProcessingState)
			provider.entered, provider.release = nil, nil
			embedder.entered, embedder.release = nil, nil
		})
	}
	for _, operation := range []string{"partition", "assessment", "embedding", "duplicate_embedding"} {
		t.Run("actual Remember waits for "+operation+" capacity", func(t *testing.T) {
			gate := modelprovider.NewConcurrencyGate(1)
			require.NoError(t, modelprovider.AcquireConcurrency(t.Context(), gate))
			provider.gateOperation = operation
			if operation == "partition" || operation == "assessment" {
				provider.gate = gate
			} else {
				embedder.gate = gate
			}
			defer func() { provider.gate = nil; embedder.gate = nil }()
			label := map[string]string{"partition": "partition", "assessment": "assessment", "embedding": "embedding", "duplicate_embedding": "duplicate embedding"}[operation]
			credential := newActor(uuid.Nil)
			request := rememberapp.RememberRequest{TargetSpace: domain.MemorySpacePrivate, IdempotencyKey: uuid.NewString(), Evidence: []rememberapp.RememberEvidenceInput{{Content: "Capacity waiter " + uuid.NewString(), ForceInsert: operation != "duplicate_embedding"}}}
			waited, resumed := false, false
			frames := call(t, credential, request, "9007199254740993", false, func(frame postgresProgressFrame) {
				if frame.Params.Message == "Waiting for "+label+" provider capacity" {
					require.False(t, waited)
					waited = true
					modelprovider.ReleaseConcurrency(gate)
				}
				if frame.Params.Message == "Calling "+label+" provider" {
					require.True(t, waited)
					resumed = true
				}
				if frame.Result != nil {
					require.True(t, resumed)
				}
			})
			require.True(t, waited && resumed)
			require.Equal(t, "completed", frames[len(frames)-1].Result.Status.ProcessingState)
		})
	}
	t.Run("actual embedding backoff resumes before terminal", func(t *testing.T) {
		credential := newActor(uuid.Nil)
		embedder.retries.Store(0)
		request := rememberapp.RememberRequest{TargetSpace: domain.MemorySpacePrivate, IdempotencyKey: uuid.NewString(), Evidence: []rememberapp.RememberEvidenceInput{{Content: "Retry [progress:retry] " + uuid.NewString(), ForceInsert: true}}}
		waited, resumed := false, false
		frames := call(t, credential, request, "0", false, func(frame postgresProgressFrame) {
			if strings.HasPrefix(frame.Params.Message, "Waiting before retrying embedding provider") {
				require.EqualValues(t, 1, embedder.retries.Load())
				waited = true
			}
			if strings.HasPrefix(frame.Params.Message, "Calling embedding provider") {
				require.True(t, waited)
				resumed = true
			}
			if frame.Result != nil {
				require.True(t, resumed)
			}
		})
		require.True(t, waited && resumed)
		require.Equal(t, "completed", frames[len(frames)-1].Result.Status.ProcessingState)
	})
	t.Run("actual PostgreSQL publication is blocked after progress", func(t *testing.T) {
		key := int64(100000 + time.Now().UnixNano()%1000000000)
		sqlDB, err := admin.DB()
		require.NoError(t, err)
		conn, err := sqlDB.Conn(t.Context())
		require.NoError(t, err)
		_, err = conn.ExecContext(t.Context(), "SELECT pg_advisory_lock($1)", key)
		require.NoError(t, err)
		defer conn.Close()
		defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", key)
		fn := "partition_progress_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		require.NoError(t, admin.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF position('[progress:block-commit]' IN NEW.content)>0 THEN PERFORM pg_advisory_xact_lock(%d); END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON remember_submitted_items FOR EACH ROW EXECUTE FUNCTION %s()`, fn, key, fn, fn)).Error)
		defer func() {
			require.NoError(t, admin.Exec(fmt.Sprintf("DROP TRIGGER %s ON remember_submitted_items; DROP FUNCTION %s()", fn, fn)).Error)
		}()
		credential := newActor(uuid.Nil)
		request := rememberapp.RememberRequest{TargetSpace: domain.MemorySpacePrivate, IdempotencyKey: uuid.NewString(), Evidence: []rememberapp.RememberEvidenceInput{{Content: "Actual transaction [progress:block-commit] " + uuid.NewString(), ForceInsert: true}}}
		observed := false
		frames := call(t, credential, request, "0", false, func(frame postgresProgressFrame) {
			if frame.Params.Message != "Committing" {
				return
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				var waiting int64
				require.NoError(t, admin.Raw("SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND objid=?::oid AND NOT granted", key).Scan(&waiting).Error)
				if waiting > 0 {
					observed = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			require.True(t, observed, "semantic transaction did not reach the real lock")
			_, err = conn.ExecContext(t.Context(), "SELECT pg_advisory_unlock($1)", key)
			require.NoError(t, err)
		})
		require.True(t, observed)
		require.Equal(t, "completed", frames[len(frames)-1].Result.Status.ProcessingState)
	})
	for _, outcome := range []string{"success", "failure"} {
		t.Run("contending caller returns saved "+outcome, func(t *testing.T) {
			entered, release := make(chan string, 1), make(chan struct{})
			provider.entered, provider.release = entered, release
			defer func() { provider.entered, provider.release = nil, nil }()
			credential := newActor(uuid.Nil)
			content := "Identical request [progress:block-assessment] " + uuid.NewString()
			if outcome == "failure" {
				content += " [progress:assessment]"
			}
			request := rememberapp.RememberRequest{TargetSpace: domain.MemorySpacePrivate, IdempotencyKey: uuid.NewString(), Evidence: []rememberapp.RememberEvidenceInput{{Content: content, ForceInsert: true}}}
			first := make(chan []postgresProgressFrame, 1)
			go func() { first <- call(t, credential, request, `"owner"`, false) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("owner did not block")
			}
			waited, returned := false, false
			frames := call(t, credential, request, `"waiter"`, false, func(frame postgresProgressFrame) {
				if frame.Params.Message == "Waiting for an identical request" {
					waited = true
					close(release)
				}
				if frame.Params.Message == "Returning saved result" {
					require.True(t, waited)
					returned = true
				}
				if frame.Result != nil {
					require.True(t, returned)
				}
			})
			owner := <-first
			require.Equal(t, owner[len(owner)-1].Result.Status.SubmissionID, frames[len(frames)-1].Result.Status.SubmissionID)
			require.True(t, waited && returned)
		})
	}

	for _, fault := range []string{"success", "assessment", "embedding", "commit"} {
		t.Run(fault, func(t *testing.T) {
			credential := newActor(uuid.Nil)
			actor := actors[credential]
			request := rememberapp.RememberRequest{TargetSpace: domain.MemorySpacePrivate, IdempotencyKey: uuid.NewString(),
				Evidence: []rememberapp.RememberEvidenceInput{{Content: "Exact Evidence [progress:" + fault + "] " + uuid.NewString(), ForceInsert: true, SourceType: "manual"}}}
			frames := call(t, credential, request, "9007199254740993", false)
			require.Greater(t, len(frames), 2)
			require.Equal(t, "Validating input", frames[0].Params.Message)
			require.Equal(t, "Checking previous result", frames[1].Params.Message)

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
				if jsonMode {
					require.Len(t, replay, 1)
				} else {
					require.Len(t, replay, 4)
					require.Equal(t, "Returning saved result", replay[2].Params.Message)
				}
				require.Equal(t, final.Status.SubmissionID, replay[len(replay)-1].Result.Status.SubmissionID)
			}
			require.Equal(t, providerCalls, provider.calls.Load())
			require.Equal(t, embeddingCalls, embedder.calls.Load())
			request.Evidence[0].Content = "Changed request under the retained key."
			conflict := call(t, credential, request, "0", false)
			require.Len(t, conflict, 3)
			require.Equal(t, "idempotency_conflict", conflict[len(conflict)-1].Result.Status.Errors[0].Code)
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
				wantFrames = 14
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
				require.Len(t, frames, 14)
				final := frames[len(frames)-1].Result
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
