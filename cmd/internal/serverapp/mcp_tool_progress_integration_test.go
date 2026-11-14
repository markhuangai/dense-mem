//go:build integration

package serverapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/evaluation"
	evaluationpostgres "github.com/markhuangai/dense-mem/internal/evaluation/postgres"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type toolProgressConfig struct{}

func (toolProgressConfig) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return domain.RecallFeedbackRuntimeConfig{Enabled: true, RetentionDays: 30}, nil
}
func (toolProgressConfig) OntologyMaintenanceRuntimeConfig(context.Context) (domain.OntologyMaintenanceConfig, error) {
	return domain.OntologyMaintenanceConfig{Enabled: true}, nil
}

type toolProgressTransport struct {
	gate modelprovider.ConcurrencyGate
}

func (p *toolProgressTransport) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	if request.SchemaName != session.ExtractionVersion {
		return partitionFixtureComplete(request)
	}
	if p.gate != nil {
		if err := modelprovider.AcquireConcurrency(ctx, p.gate); err != nil {
			return modelprovider.StructuredResult{}, err
		}
		defer modelprovider.ReleaseConcurrency(p.gate)
	}
	var input session.ExtractionRequest
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	if len(request.Messages) == 2 && strings.Contains(request.Messages[1].Content, "[tool:regenerate]") {
		return modelprovider.StructuredResult{Content: "invalid complete extraction"}, nil
	}
	result := session.ExtractionResponse{RequestID: input.RequestID, Coverage: []string{}, Excerpts: []session.Excerpt{}, SecuritySignals: []session.ExtractionSignal{}}
	for _, segment := range input.Window.Core {
		result.Coverage = append(result.Coverage, segment.Ref)
		result.Excerpts = append(result.Excerpts, session.Excerpt{EventIndex: segment.EventIndex, Start: segment.Start, End: segment.End, Content: segment.Text})
	}
	body, err := json.Marshal(result)
	return modelprovider.StructuredResult{Content: string(body)}, err
}

type toolProgressEmbedder struct{ progressPostgresEmbedder }

func (p *toolProgressEmbedder) Embed(ctx context.Context, text string) ([]float32, string, error) {
	vectors, model, err := p.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, model, err
	}
	return vectors[0], model, nil
}

type toolProgressLog struct {
	mu   sync.Mutex
	body bytes.Buffer
}

func (l *toolProgressLog) Write(value []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.body.Write(value)
}
func (l *toolProgressLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.body.String() }

type mcpToolProgressFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Token    json.RawMessage `json:"progressToken"`
		Sequence int             `json:"progress"`
		Message  string          `json:"message"`
		Total    *int            `json:"total"`
	} `json:"params"`
	Result struct {
		IsError bool           `json:"isError"`
		Content map[string]any `json:"structuredContent"`
	} `json:"result"`
	Error json.RawMessage `json:"error"`
}

func readMCPToolProgress(body io.Reader, observe func(mcpToolProgressFrame)) ([]mcpToolProgressFrame, error) {
	var frames []mcpToolProgressFrame
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" && data.Len() > 0 {
			var frame mcpToolProgressFrame
			if err := json.Unmarshal([]byte(data.String()), &frame); err != nil {
				return frames, err
			}
			frames = append(frames, frame)
			if observe != nil {
				observe(frame)
			}
			data.Reset()
		} else if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if scanner.Err() != nil {
		return frames, scanner.Err()
	}
	if data.Len() > 0 {
		return frames, fmt.Errorf("incomplete MCP progress event")
	}
	return frames, nil
}

func TestMCPToolProgressPostgres(t *testing.T) {
	admin, app, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	t.Cleanup(cleanup)
	model, dimensions := ensureRememberProcessorIntegrationSearchContract(t, admin, rls)
	provider := &progressPostgresAssessor{}
	embedder := &toolProgressEmbedder{progressPostgresEmbedder: progressPostgresEmbedder{rememberProcessorIntegrationSearchEmbedder: rememberProcessorIntegrationSearchEmbedder{model: model, dimensions: dimensions}}}
	transport := &toolProgressTransport{}
	knowledge := knowledgepostgres.NewStore(app, rls)
	var logs toolProgressLog
	logger := observability.NewWithHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	bundle := buildApplicationBundle(applicationCompositionDependencies{
		Knowledge: knowledge, RememberPersistence: knowledge, TraceStore: buildTraceStore(app, rls),
		RecallSearch:         recallpostgres.NewStore(app, rls, searchpostgres.NewStore(app, rls), recallpostgres.LoadRecallEvidenceConflictRecords),
		RecallFeedbackEvents: recallpostgres.NewFeedbackStore(app, rls), Assessor: provider, SessionAssessor: provider,
		GeneratorTransport: transport, SessionEnabled: true, RememberModel: "tool-progress-extraction", AssessmentLimits: assessor.DefaultSemanticAssessmentLimits(),
		EmbeddingProvider: embedder, RetryEmbeddingProvider: embedding.NewRetryEmbeddingProvider(embedder, logger),
		Metrics: observability.NoopDiscoverabilityMetrics(), Logger: logger,
	})
	// The production composition accepts the full settings port; these fixed settings exercise enabled policy without changing durable configuration.
	bundle.Recall = buildRecallApplication(recallApplicationDependencies{
		Search:   recallpostgres.NewStore(app, rls, searchpostgres.NewStore(app, rls), recallpostgres.LoadRecallEvidenceConflictRecords),
		Provider: embedding.NewRetryEmbeddingProvider(embedder, logger), OntologyConfig: toolProgressConfig{}, Logger: logger,
	})
	reg, err := registry.BuildActive(registry.Dependencies{
		RememberBindings: registry.RememberBindings{Service: bundle.Remember}, SessionBindings: registry.SessionBindings{Service: bundle.Session},
		RecallBindings: registry.RecallBindings{Service: bundle.Recall}, TraceBindings: registry.TraceBindings{Service: bundle.Context},
		LifecycleBindings: registry.LifecycleBindings{Service: bundle.Lifecycle}, MemoryPackBindings: registry.MemoryPackBindings{Service: bundle.MemoryPack},
		Core: registry.CoreDependencies{RecallFeedbackConfig: toolProgressConfig{}, RecallFeedbackEvents: bundle.RecallFeedback, Metrics: observability.NoopDiscoverabilityMetrics()},
	})
	require.NoError(t, err)
	actors := map[string]requestctx.Actor{}
	newActor := func(team uuid.UUID) string {
		if team == uuid.Nil {
			team = uuid.New()
			require.NoError(t, accesspostgres.NewTeamRepository(admin, rls).Create(t.Context(), &domain.Team{ID: team, Name: "tool-progress-" + team.String()}))
		}
		id := uuid.New()
		require.NoError(t, accesspostgres.NewCredentialRepository(admin, rls, nil).CreateCredential(t.Context(), &domain.Credential{
			ID: id, TeamID: team, Name: "tool progress", Role: "member", KeyHash: id.String(), KeyPrefix: strings.ReplaceAll(id.String(), "-", "")[:24], KeySuffix: "test", Scopes: []string{"read", "write"},
		}))
		actor, ok := requestctx.ActorFromContext(rememberProcessorActorContext(t, t.Context(), admin, rls, team, id))
		require.True(t, ok)
		actors[id.String()] = actor
		return id.String()
	}
	a := newActor(uuid.Nil)
	b, c := newActor(actors[a].TeamID), newActor(uuid.Nil)
	e := echo.New()
	h := handler.NewMCPHandlerWithLifecycleAndRuntimeConfig(reg, handler.NewMCPLogger(transportLogger(logger)), nil, toolProgressConfig{})
	e.POST("/mcp", h.HandlePost)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actors[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ctx := correlation.WithID(requestctx.WithActor(r.Context(), actor), r.Header.Get("X-Correlation-ID"))
		ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{TeamID: actor.TeamID, OwnerID: actor.OwnerID, Grants: actor.Grants, AllowedSpaces: actor.AllowedSpaces})
		ctx = middleware.SetResolvedTeamIDForTest(ctx, actor.TeamID)
		e.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(server.Close)
	request := func(ctx context.Context, actor, name string, args any, token, accept string) *http.Request {
		meta := map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
		if token != "" {
			meta["progressToken"] = json.RawMessage(token)
		}
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": uuid.NewString(), "method": "tools/call", "params": map[string]any{"name": name, "arguments": args, "_meta": meta}})
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/mcp", bytes.NewReader(body))
		require.NoError(t, err)
		for key, value := range map[string]string{"Authorization": "Bearer " + actor, "Content-Type": "application/json", "Accept": accept,
			"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call", "Mcp-Name": name, "X-Correlation-ID": uuid.NewString()} {
			req.Header.Set(key, value)
		}
		return req
	}
	call := func(actor, name string, args any, token, accept string, observe func(mcpToolProgressFrame)) []mcpToolProgressFrame {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		req := request(ctx, actor, name, args, token, accept)
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		var frames []mcpToolProgressFrame
		if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
			frames, err = readMCPToolProgress(response.Body, observe)
		} else {
			var frame mcpToolProgressFrame
			err = json.NewDecoder(response.Body).Decode(&frame)
			frames = append(frames, frame)
		}
		require.NoError(t, err)
		require.NotEmpty(t, frames)
		for index, frame := range frames[:len(frames)-1] {
			require.Equal(t, "notifications/progress", frame.Method)
			require.Equal(t, token, string(frame.Params.Token))
			require.Equal(t, index+1, frame.Params.Sequence)
			require.Nil(t, frame.Params.Total)
			require.Empty(t, frame.Result.Content)
			require.NotContains(t, frame.Params.Message, "private canary")
		}
		require.NotEmpty(t, frames[len(frames)-1].ID)
		return frames
	}
	stream := func(actor, name string, args any) []mcpToolProgressFrame {
		return call(actor, name, args, "9007199254740993", "application/json, text/event-stream", nil)
	}
	content := func(frames []mcpToolProgressFrame) map[string]any {
		require.False(t, frames[len(frames)-1].Result.IsError)
		require.Empty(t, frames[len(frames)-1].Error)
		return frames[len(frames)-1].Result.Content
	}
	hasStages := func(frames []mcpToolProgressFrame, stages ...string) {
		for _, stage := range stages {
			found := false
			for _, frame := range frames {
				found = found || strings.HasPrefix(frame.Params.Message, stage)
			}
			require.True(t, found, stage)
		}
	}
	seed := content(stream(a, "remember", map[string]any{"target_space": "private", "idempotency_key": uuid.NewString(), "evidence": []any{map[string]any{"content": "private canary PostgreSQL progress", "source_type": "manual", "force_insert": true}}}))
	id := seed["evidence"].([]any)[0].(map[string]any)["evidence_id"].(string)
	sessionArgs := session.Request{Framework: "progress", AppName: "integration", UserID: "private canary", SessionID: uuid.NewString(), IdempotencyKey: uuid.NewString(), Events: []session.Event{{EventID: "one", Text: "I use PostgreSQL. [tool:regenerate]"}}}
	ingested := stream(a, "ingest_session", sessionArgs)
	require.Equal(t, "completed", content(ingested)["processing_state"])
	require.Equal(t, "stored", content(ingested)["memory_outcome"])
	hasStages(ingested, "Checking session ownership", "Preparing extraction windows", "Extracting exact session excerpts", "Regenerating session excerpts", "Saving validated session excerpts", "Committing", "Preparing result")
	replay := stream(a, "ingest_session", sessionArgs)
	require.Equal(t, content(ingested), content(replay))
	hasStages(replay, "Returning saved result")
	require.Len(t, replay, 6)
	recalled := stream(a, "recall_memory", map[string]any{"query": "PostgreSQL progress", "limit": 10})
	hasStages(recalled, "Preparing Recall policy", "Preparing query embedding", "Searching authorized Evidence", "Reading authorized Evidence", "Recording the Recall snapshot")
	recallID := content(recalled)["recall_id"]
	traced := stream(a, "trace_memory", map[string]any{"evidence_id": id})
	hasStages(traced, "Validating input", "Reading the authorized Evidence trace", "Preparing result")
	require.Equal(t, id, content(traced)["evidence"].(map[string]any)["evidence_id"])
	pack := stream(a, "export_memory_pack", map[string]any{"name": "progress pack", "evidence_ids": []string{id}})
	content(pack)
	hasStages(pack, "Reading Evidence for export", "Building the memory pack")
	feedback := stream(a, "submit_recall_session_feedback", map[string]any{"recalls": []any{map[string]any{"recall_event_id": recallID, "used": true, "answer_supported": true, "quality": "high"}}})
	require.EqualValues(t, 1, content(feedback)["recorded_count"])
	hasStages(feedback, "Recording Recall feedback", "Preparing result")
	for _, mode := range []struct{ token, accept string }{{"0", "application/json"}, {"", "application/json, text/event-stream"}} {
		frames := call(a, "trace_memory", map[string]any{"evidence_id": id}, mode.token, mode.accept, nil)
		require.Len(t, frames, 1)
		require.Equal(t, content(traced), content(frames))
	}
	t.Run("concurrent same-token actors", func(t *testing.T) {
		var workers sync.WaitGroup
		for _, actor := range []string{a, b, c} {
			workers.Add(1)
			go func() {
				defer workers.Done()
				frames := stream(actor, "trace_memory", map[string]any{"evidence_id": id})
				require.Equal(t, actor != a, frames[len(frames)-1].Result.IsError)
			}()
		}
		workers.Wait()
	})
	t.Run("actual provider capacity wait", func(t *testing.T) {
		gate := modelprovider.NewConcurrencyGate(1)
		require.NoError(t, modelprovider.AcquireConcurrency(t.Context(), gate))
		transport.gate = gate
		defer func() { transport.gate = nil }()
		args := sessionArgs
		args.SessionID, args.IdempotencyKey = uuid.NewString(), uuid.NewString()
		var once sync.Once
		release := func() { once.Do(func() { modelprovider.ReleaseConcurrency(gate) }) }
		defer release()
		frames := call(a, "ingest_session", args, "0", "application/json, text/event-stream", func(frame mcpToolProgressFrame) {
			if frame.Params.Message == "Waiting for session extraction provider capacity" {
				release()
			}
		})
		require.Equal(t, "completed", content(frames)["processing_state"])
		hasStages(frames, "Waiting for session extraction provider capacity")
	})
	t.Run("query embedding capacity and existing retry", func(t *testing.T) {
		gate := modelprovider.NewConcurrencyGate(1)
		require.NoError(t, modelprovider.AcquireConcurrency(t.Context(), gate))
		embedder.gate = gate
		embedder.retries.Store(0)
		defer func() { embedder.gate = nil }()
		var once sync.Once
		release := func() { once.Do(func() { modelprovider.ReleaseConcurrency(gate) }) }
		defer release()
		frames := call(a, "recall_memory", map[string]any{"query": "PostgreSQL progress [progress:retry]"}, "0", "application/json, text/event-stream", func(frame mcpToolProgressFrame) {
			if frame.Params.Message == "Waiting for query embedding provider capacity" {
				release()
			}
		})
		content(frames)
		hasStages(frames, "Waiting for query embedding provider capacity", "Waiting before retrying query embedding provider", "Calling query embedding provider")
	})
	t.Run("evaluation application audit listing and delegated Recall", func(t *testing.T) {
		api := evaluation.New(evaluation.Dependencies{Repository: evaluationpostgres.NewReader(app, rls), Recall: bundle.Recall, Audit: buildAuditApplication(app)})
		var stages []string
		ctx := requestctx.WithProgressObserver(requestctx.WithActor(t.Context(), actors[a]), func(_ context.Context, update requestctx.ProgressUpdate) { stages = append(stages, update.Stage) })
		page, err := api.ListKnowledgeRefs(ctx, "ignored", "evidence", 10, "", "", false)
		require.NoError(t, err)
		require.NotEmpty(t, page["items"])
		require.Equal(t, []string{"evaluation_audit", "evaluation_list", "preparing_result"}, stages)
		stages = nil
		result, err := api.RunRecallCase(ctx, "progress", recallcontract.Request{Query: "PostgreSQL progress"}, "", "", true)
		require.NoError(t, err)
		require.NotEmpty(t, result["ranked_refs"])
		require.Equal(t, "evaluation_audit", stages[0])
		require.Contains(t, stages, "recall_search")
		require.Equal(t, "preparing_result", stages[len(stages)-1])
		stages = nil
		_, err = api.ListKnowledgeRefs(ctx, "ignored", "unsupported", 10, "", "", false)
		require.Error(t, err)
		require.Equal(t, []string{"evaluation_audit", "evaluation_list"}, stages)
	})

	t.Run("native PostgreSQL retraction lock", func(t *testing.T) {
		tx := admin.Begin()
		require.NoError(t, tx.Error)
		defer tx.Rollback()
		require.NoError(t, tx.Exec(`SELECT fragment_id FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid FOR UPDATE`, actors[a].TeamID, id).Error)
		args := map[string]any{"evidence_ids": []string{id}, "reason": "progress lock", "idempotency_key": uuid.NewString()}
		var once sync.Once
		var releaseErr error
		release := func() { once.Do(func() { releaseErr = tx.Rollback().Error }) }
		defer release()
		frames := call(a, "retract_evidence", args, "0", "application/json, text/event-stream", func(frame mcpToolProgressFrame) {
			if frame.Params.Message == "Applying Evidence retraction" {
				require.Eventually(t, func() bool {
					var waiting int64
					err := admin.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%evidence_fragments%'`).Scan(&waiting).Error
					return err == nil && waiting > 0
				}, 5*time.Second, 10*time.Millisecond)
				release()
			}
		})
		require.NoError(t, releaseErr)
		require.Equal(t, "completed", content(frames)["processing_state"])
		require.Equal(t, content(frames), content(stream(a, "retract_evidence", args)))
	})
	t.Run("cancellation while waiting for extraction capacity", func(t *testing.T) {
		gate := modelprovider.NewConcurrencyGate(1)
		require.NoError(t, modelprovider.AcquireConcurrency(t.Context(), gate))
		transport.gate = gate
		defer func() { modelprovider.ReleaseConcurrency(gate); transport.gate = nil }()
		args := sessionArgs
		args.SessionID, args.IdempotencyKey = uuid.NewString(), uuid.NewString()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		response, err := server.Client().Do(request(ctx, a, "ingest_session", args, "0", "application/json, text/event-stream"))
		require.NoError(t, err)
		defer response.Body.Close()
		seenWait := false
		_, err = readMCPToolProgress(response.Body, func(frame mcpToolProgressFrame) {
			if frame.Params.Message == "Waiting for session extraction provider capacity" {
				seenWait = true
				cancel()
			}
		})
		require.Error(t, err)
		require.True(t, seenWait)
		var submission string
		require.Eventually(t, func() bool {
			var state string
			err := rls.WithSystemTx(t.Context(), admin, func(tx *gorm.DB) error {
				return tx.Raw(`SELECT submission_id::text, result->>'processing_state' FROM session_submissions WHERE team_id=?::uuid AND idempotency_key=?`, actors[a].TeamID, args.IdempotencyKey).Row().Scan(&submission, &state)
			})
			return err == nil && state == "failed"
		}, 5*time.Second, 10*time.Millisecond)
		var count int64
		require.NoError(t, rls.WithSystemTx(t.Context(), admin, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT count(*) FROM evidence_fragments WHERE team_id=?::uuid AND ingest_id=?::uuid`, actors[a].TeamID, submission).Scan(&count).Error
		}))
		require.Zero(t, count)
	})
	t.Run("unavailable progress delivery preserves actual tool outcome", func(t *testing.T) {
		req := request(t.Context(), a, "trace_memory", map[string]any{"evidence_id": id}, "0", "application/json, text/event-stream")
		recorder := httptest.NewRecorder()
		server.Config.Handler.ServeHTTP(recorder, req)
		frames, err := readMCPToolProgress(recorder.Body, nil)
		require.NoError(t, err)
		require.Len(t, frames, 1)
		require.Equal(t, content(stream(a, "trace_memory", map[string]any{"evidence_id": id})), content(frames))
		require.Contains(t, logs.String(), "mcp_progress_delivery_failed")
		require.Contains(t, logs.String(), "write_deadline_unsupported")
	})

	var failed int64
	require.NoError(t, rls.WithSystemTx(t.Context(), admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM recall_feedback_events WHERE recall_id=? AND feedback_at IS NOT NULL`, recallID).Scan(&failed).Error
	}))
	require.EqualValues(t, 1, failed)
	require.Contains(t, logs.String(), "mcp_progress_delivered")
	require.Contains(t, logs.String(), `"write_budget_ms":100`)
	for _, credential := range []string{a, b, c} {
		require.NotContains(t, logs.String(), "Bearer "+credential)
	}
}
