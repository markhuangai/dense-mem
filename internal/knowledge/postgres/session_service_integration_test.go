//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	processor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/markhuangai/dense-mem/internal/session/extraction"
	service "github.com/markhuangai/dense-mem/internal/session/service"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type sessionOutbound struct {
	mu      sync.Mutex
	calls   int
	fail    bool
	block   bool
	entered chan struct{}
}

func (f *sessionOutbound) Complete(ctx context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	f.mu.Lock()
	f.calls++
	fail, block := f.fail, f.block
	f.mu.Unlock()
	if block {
		if f.entered != nil {
			select {
			case f.entered <- struct{}{}:
			default:
			}
		}
		<-ctx.Done()
		return modelprovider.StructuredResult{}, ctx.Err()
	}
	if fail {
		return modelprovider.StructuredResult{}, &modelprovider.ProviderError{Message: "fixture transport unavailable"}
	}
	var input session.ExtractionRequest
	if err := json.Unmarshal([]byte(req.Messages[1].Content), &input); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	out := session.ExtractionResponse{RequestID: input.RequestID, Coverage: []string{}, Entities: []session.EntityProposal{}, Relationships: []session.RelationshipProposal{}, SecuritySignals: []session.ExtractionSignal{}}
	for _, segment := range input.Window.Core {
		out.Coverage = append(out.Coverage, segment.Ref)
	}
	body, err := json.Marshal(out)
	return modelprovider.StructuredResult{Content: string(body)}, err
}
func (f *sessionOutbound) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func TestSessionServiceDurableFailureRecoveryNoFactsAndReplay(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-service")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	intake.Request.Events[0].Text = "Thanks. Let's continue."
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	outbound := &sessionOutbound{fail: true}
	preparer := processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store})
	deps := service.Dependencies{Enabled: true, Repository: store, Preparer: preparer, Tokenizer: "o200k_base", Extractor: extraction.NewProvider(outbound, "fixture", assessor.DefaultSemanticAssessmentLimits())}
	api := service.NewService(deps)
	failed, err := api.Ingest(ctx, intake.Request)
	require.Error(t, err)
	require.NotNil(t, failed)
	require.Equal(t, "failed", failed.ProcessingState)
	require.Equal(t, "provider_unavailable", failed.Errors[0].Code)
	retained, err := store.LookupSession(ctx, intake.Scope, intake.Request.IdempotencyKey)
	require.NoError(t, err)
	require.Equal(t, failed.SubmissionID, retained.ID)
	overlapping := intake.Request
	overlapping.IdempotencyKey = "replacement-key"
	_, err = api.Ingest(ctx, overlapping)
	require.ErrorIs(t, err, session.ErrOriginalRequestRequired)
	outbound.mu.Lock()
	outbound.fail = false
	outbound.mu.Unlock()
	completed, err := api.Ingest(ctx, intake.Request)
	require.NoError(t, err)
	require.Equal(t, failed.SubmissionID, completed.SubmissionID)
	require.Equal(t, "completed", completed.ProcessingState)
	require.Equal(t, "not_required", completed.SearchState)
	require.Empty(t, completed.Events[0].EvidenceIDs)
	calls := outbound.count()
	deps.Tokenizer = "missing-tokenizer"
	replay, err := service.NewService(deps).Ingest(ctx, intake.Request)
	require.NoError(t, err)
	expectedReceipt, err := json.Marshal(completed)
	require.NoError(t, err)
	replayedReceipt, err := json.Marshal(replay)
	require.NoError(t, err)
	require.Equal(t, string(expectedReceipt), string(replayedReceipt))
	require.Equal(t, calls, outbound.count())
	overlapping.Events = append(overlapping.Events, session.Event{EventID: "two", Text: "Okay, thank you."})
	incremental, err := api.Ingest(ctx, overlapping)
	require.NoError(t, err)
	require.Equal(t, 1, incremental.AcceptedEventCount)
	require.Equal(t, 1, incremental.DuplicateEventCount)
	var count int64
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid`, team).Row().Scan(&count)
	}))
	require.Zero(t, count)
}

func TestSessionServiceCancellationAndTimeoutRetainIntakeWithoutKnowledge(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-cancel")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	base, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			req := intake.Request
			req.IdempotencyKey = mode
			req.Events = []session.Event{{EventID: mode, Text: "Thanks."}}
			outbound := &sessionOutbound{block: true, entered: make(chan struct{}, 1)}
			api := service.NewService(service.Dependencies{Enabled: true, Repository: store, Preparer: processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store}), Tokenizer: "o200k_base", Extractor: extraction.NewProvider(outbound, "fixture", assessor.DefaultSemanticAssessmentLimits())})
			ctx, cancel := context.WithCancel(base)
			if mode == "timeout" {
				ctx, cancel = context.WithTimeout(base, 150*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := api.Ingest(ctx, req); done <- err }()
			<-outbound.entered
			if mode == "cancel" {
				cancel()
			}
			require.Error(t, <-done)
			retained, err := store.LookupSession(base, intake.Scope, mode)
			require.NoError(t, err)
			require.Equal(t, "failed", retained.Result.ProcessingState)
			if mode == "timeout" {
				require.Equal(t, "request_timeout", retained.Result.Errors[0].Code)
			} else {
				require.Equal(t, "request_cancelled", retained.Result.Errors[0].Code)
			}
		})
	}
	var count int64
	require.NoError(t, rls.WithSystemTx(base, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid`, team).Row().Scan(&count)
	}))
	require.Zero(t, count)
}

func TestSessionServiceBudgetRejectionHasNoIntake(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-budget")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	outbound := &sessionOutbound{}
	api := service.NewService(service.Dependencies{Enabled: true, Repository: store, Preparer: processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store}), Tokenizer: "o200k_base", Extractor: extraction.NewProvider(outbound, "fixture", assessor.DefaultSemanticAssessmentLimits())})
	intake.Request.Events[0].Text = strings.Repeat("🧭", 100000)
	_, err := api.Ingest(ctx, intake.Request)
	require.ErrorIs(t, err, session.ErrBudget)
	_, err = store.LookupSession(ctx, intake.Scope, intake.Request.IdempotencyKey)
	require.ErrorIs(t, err, session.ErrNotFound)
	require.Zero(t, outbound.count())
}

func TestSessionServiceResumesValidatedExtractionCheckpoint(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-checkpoint-recovery")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	intake.Request.Events[0].Text = strings.Repeat("Okay. ", 6000)
	var err error
	intake.Windows, err = service.BuildWindows(intake.Request, "o200k_base")
	require.NoError(t, err)
	require.Greater(t, len(intake.Windows), 1)
	intake.RequestHash, err = service.RequestHash(intake.Request)
	require.NoError(t, err)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	staged, err := store.StageSession(ctx, intake)
	require.NoError(t, err)
	window := staged.Intake.Windows[0]
	checkpoint := session.ExtractionResponse{RequestID: fmt.Sprintf("session:%s:window:%d", staged.ID, window.Index), Coverage: []string{}, Entities: []session.EntityProposal{}, Relationships: []session.RelationshipProposal{}, SecuritySignals: []session.ExtractionSignal{}}
	for _, segment := range window.Core {
		checkpoint.Coverage = append(checkpoint.Coverage, segment.Ref)
	}
	body, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	require.NoError(t, store.SaveSessionExtraction(ctx, intake.Scope, staged.ID, window.Index, body))
	outbound := &sessionOutbound{}
	api := service.NewService(service.Dependencies{Enabled: true, Repository: store, Preparer: processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store}), Tokenizer: "o200k_base", Extractor: extraction.NewProvider(outbound, "fixture", assessor.DefaultSemanticAssessmentLimits())})
	result, err := api.Ingest(ctx, intake.Request)
	require.NoError(t, err)
	require.Equal(t, "completed", result.ProcessingState)
	require.Equal(t, len(intake.Windows)-1, outbound.count())
}

func TestSessionHTTPProviderFailureReceiptsPreserveActualClassifications(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-http-failures")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	base, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	for _, mode := range []string{"rate-limit", "provider-timeout", "caller-timeout", "caller-cancel"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case entered <- struct{}{}:
				default:
				}
				if mode == "rate-limit" {
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
					return
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			client := server.Client()
			if mode == "provider-timeout" {
				client.Timeout = 150 * time.Millisecond
			}
			transport := assessorprovider.NewOpenAIAssessor(&config.Config{AIVerifierAPIURL: server.URL, AIVerifierAPIKey: "fixture-key", AIVerifierModel: "fixture", AIVerifierMaxConcurrency: 1}, client)
			api := service.NewService(service.Dependencies{Enabled: true, Repository: store, Preparer: processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store}), Tokenizer: "o200k_base", Extractor: extraction.NewProvider(transport, "fixture", assessor.DefaultSemanticAssessmentLimits())})
			req := intake.Request
			req.IdempotencyKey = mode
			req.Events = []session.Event{{EventID: mode, Text: "Thanks."}}
			ctx, cancel := context.WithCancel(base)
			if mode == "caller-timeout" {
				ctx, cancel = context.WithTimeout(base, 250*time.Millisecond)
			}
			defer cancel()
			type outcome struct {
				result *session.Result
				err    error
			}
			done := make(chan outcome, 1)
			go func() { result, err := api.Ingest(ctx, req); done <- outcome{result, err} }()
			<-entered
			if mode == "caller-cancel" {
				cancel()
			}
			result := <-done
			require.Error(t, result.err)
			require.NotNil(t, result.result)
			expected := "provider_unavailable"
			if mode == "caller-cancel" {
				expected = "request_cancelled"
			}
			if mode == "caller-timeout" {
				expected = "request_timeout"
			}
			require.Equal(t, expected, result.result.Errors[0].Code)
			retained, err := store.LookupSession(base, intake.Scope, mode)
			require.NoError(t, err)
			require.Equal(t, expected, retained.Result.Errors[0].Code)
			require.Empty(t, retained.Result.Events[0].EvidenceIDs)
		})
	}
}

func TestSessionServiceFactsDiagnosticsAndCheckpointRecoveryThroughPostgres(t *testing.T) {
	admin, app, rls, cleanup := setupKnowledgeOwnerDB(t)
	defer cleanup()
	team := createOwnerTeam(t, admin, rls, "session-facts")
	owner := createOwnerProfile(t, admin, rls, team, "owner")
	installOwnerSearchContract(t, admin, rls, "session-facts", 3, 1)
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	var extractionCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Messages       []modelprovider.Message `json:"messages"`
			ResponseFormat struct {
				JSONSchema struct{ Name string } `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil || len(envelope.Messages) < 2 {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		if envelope.ResponseFormat.JSONSchema.Name == "dense_mem_session_extraction_v1" {
			extractionCalls.Add(1)
		}
		response, err := sessionFactsProviderResponse(envelope.ResponseFormat.JSONSchema.Name, []byte(envelope.Messages[1].Content))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		body, err := json.Marshal(response)
		if err != nil {
			http.Error(w, "invalid fixture response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture-provider-key", "choices": []any{map[string]any{"message": map[string]any{"content": string(body)}}}})
	}))
	defer server.Close()
	transport := assessorprovider.NewOpenAIAssessor(&config.Config{AIVerifierAPIURL: server.URL, AIVerifierAPIKey: "fixture-provider-key", AIVerifierModel: "fixture", AIVerifierMaxConcurrency: 4}, server.Client())
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	embedder := &sessionFactsEmbedder{}
	protector := observability.NewCredentialProtector("fixture-provider-key")
	preparer := processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store, Catalog: store, Assessor: transport, Embedder: embedder, Limits: assessor.DefaultSemanticAssessmentLimits(), DiagnosticProtector: protector})
	api := service.NewService(service.Dependencies{
		Enabled: true, Repository: store, Preparer: preparer, Tokenizer: "o200k_base",
		Extractor: extraction.NewProvider(transport, "fixture", assessor.DefaultSemanticAssessmentLimits()), DiagnosticProtector: protector,
		DiagnosticRecorder: func() modelprovider.SnapshotRecorder { return processor.NewDiagnosticRecorder(protector) },
	})
	reg, err := registry.BuildActive(registry.Dependencies{SessionBindings: registry.SessionBindings{Service: api}})
	require.NoError(t, err)
	tool, ok := reg.Get(registry.ToolIngestSession)
	require.True(t, ok)
	invoke := func(request session.Request) (map[string]any, error) {
		encoded, err := json.Marshal(request)
		require.NoError(t, err)
		var arguments map[string]any
		require.NoError(t, json.Unmarshal(encoded, &arguments))
		return tool.Invoke(ctx, owner, arguments)
	}
	ingest := func(request session.Request) (*session.Result, error) {
		output, invokeErr := invoke(request)
		if invokeErr != nil {
			failure, ok := registry.ToolResultFromError(invokeErr)
			require.True(t, ok)
			output = failure.Result
		}
		encoded, err := json.Marshal(output)
		require.NoError(t, err)
		var result session.Result
		require.NoError(t, json.Unmarshal(encoded, &result))
		return &result, invokeErr
	}
	first, err := ingest(intake.Request)
	require.NoError(t, err)
	require.Equal(t, "current", first.SearchState)
	require.Len(t, first.RelationshipResults, 1)
	require.Len(t, first.Events[0].EvidenceIDs, 1)
	secondRequest := intake.Request
	secondRequest.IdempotencyKey = "incremental-facts"
	secondRequest.Events = append([]session.Event{}, intake.Request.Events...)
	secondRequest.Events = append(secondRequest.Events, session.Event{EventID: "two", Text: "Ari also uses Rust."})
	second, err := ingest(secondRequest)
	require.NoError(t, err)
	require.Equal(t, 1, second.AcceptedEventCount)
	require.Equal(t, 1, second.DuplicateEventCount)
	require.Equal(t, first.Events[0].EvidenceIDs, second.Events[0].EvidenceIDs)
	require.Equal(t, "current", second.SearchState)
	var factsJSON []byte
	var subjects int64
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT jsonb_agg(v.canonical_value ORDER BY v.canonical_value),count(DISTINCT r.subject_entity_id)
          FROM relationship_records r JOIN value_records v ON v.team_id=r.team_id AND v.value_id=r.object_value_id
          WHERE r.team_id=?::uuid AND r.space_id=?::uuid`, team, intake.Scope.SpaceID).Row().Scan(&factsJSON, &subjects)
	}))
	var facts []string
	require.NoError(t, json.Unmarshal(factsJSON, &facts))
	require.Equal(t, []string{"Go", "Rust"}, facts)
	require.EqualValues(t, 1, subjects)
	failedRequest := intake.Request
	failedRequest.IdempotencyKey = "late-embedding-failure"
	failedRequest.Events = []session.Event{{EventID: "three", Text: "Ari uses Redis."}}
	embedder.failOnCall.Store(embedder.calls.Load() + 2)
	failed, err := ingest(failedRequest)
	require.Error(t, err)
	require.Equal(t, "failed", failed.ProcessingState)
	require.Empty(t, failed.Events[0].EvidenceIDs)
	var ingests int64
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM knowledge_ingests WHERE team_id=?::uuid`, team).Row().Scan(&ingests)
	}))
	require.EqualValues(t, 2, ingests)
	retained, err := store.LookupSession(ctx, intake.Scope, failedRequest.IdempotencyKey)
	require.NoError(t, err)
	require.NotEmpty(t, retained.Extractions)
	require.NotEmpty(t, retained.Linked)
	overlap := failedRequest
	overlap.IdempotencyKey = "unfinished-event-new-key"
	_, err = invoke(overlap)
	failure, ok := registry.ToolResultFromError(err)
	require.True(t, ok)
	require.Equal(t, "session_event_retry_required", failure.Result["reason_code"])
	require.Contains(t, failure.Result["remediation"], "original complete request")
	extracted := extractionCalls.Load()
	embedder.failOnCall.Store(0)
	recovered, err := ingest(failedRequest)
	require.NoError(t, err)
	require.Equal(t, failed.SubmissionID, recovered.SubmissionID)
	require.Equal(t, "current", recovered.SearchState)
	require.Equal(t, extracted, extractionCalls.Load())
	var diagnosticJSON []byte
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT body FROM session_submission_diagnostics WHERE submission_id=?::uuid ORDER BY created_at LIMIT 1`, first.SubmissionID).Row().Scan(&diagnosticJSON)
	}))
	var diagnostic struct {
		Payload struct {
			Exchanges []modelprovider.ProviderExchange
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(diagnosticJSON, &diagnostic))
	require.NotEmpty(t, diagnostic.Payload.Exchanges)
	for _, exchange := range diagnostic.Payload.Exchanges {
		require.NotContains(t, string(exchange.RequestBody), "fixture-provider-key")
		require.NotContains(t, string(exchange.ResponseBody), "fixture-provider-key")
	}
	for _, test := range []struct{ key, event, text, reason string }{
		{intake.Request.IdempotencyKey, "one", "Ari uses Rust.", "idempotency_conflict"},
		{"changed-event", "one", "Ari uses Rust.", "session_event_conflict"},
		{"empty-event", "four", "   ", "session_input_invalid"},
		{"budget", "four", strings.Repeat("🧭", 100000), "input_budget_exceeded"},
	} {
		request := intake.Request
		request.IdempotencyKey = test.key
		request.Events = []session.Event{{EventID: test.event, Text: test.text}}
		_, err := invoke(request)
		failure, ok := registry.ToolResultFromError(err)
		require.True(t, ok)
		require.Equal(t, test.reason, failure.Result["reason_code"])
	}
	_, err = tool.Invoke(ctx, owner, map[string]any{"unknown": "field"})
	require.Error(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET lifecycle_state='sealed',generation=generation+1,sealed_at=now() WHERE id=?::uuid`, intake.Scope.SpaceID).Error
	}))
	_, err = invoke(intake.Request)
	failure, ok = registry.ToolResultFromError(err)
	require.True(t, ok)
	require.Equal(t, "unauthorized_scope", failure.Result["code"])
}

type sessionFactsEmbedder struct {
	calls      atomic.Int64
	failOnCall atomic.Int64
}

func (e *sessionFactsEmbedder) Embed(ctx context.Context, text string) ([]float32, string, error) {
	vectors, model, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, model, err
	}
	return vectors[0], model, nil
}
func (e *sessionFactsEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, string, error) {
	if e.calls.Add(1) == e.failOnCall.Load() {
		return nil, e.ModelName(), context.DeadlineExceeded
	}
	vectors := make([][]float32, len(texts))
	for index := range vectors {
		vectors[index] = []float32{1, 0, 0}
	}
	return vectors, e.ModelName(), nil
}
func (*sessionFactsEmbedder) ModelName() string { return "owner-e2e-model" }
func (*sessionFactsEmbedder) Dimensions() int   { return 3 }
func (*sessionFactsEmbedder) IsAvailable() bool { return true }

func sessionFactsProviderResponse(schema string, raw []byte) (any, error) {
	switch schema {
	case "dense_mem_session_extraction_v1":
		var input session.ExtractionRequest
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		value := "Go"
		if strings.Contains(input.Window.Core[0].Text, "Rust") {
			value = "Rust"
		}
		if strings.Contains(input.Window.Core[0].Text, "Redis") {
			value = "Redis"
		}
		encoded, _ := json.Marshal(value)
		output := session.ExtractionResponse{RequestID: input.RequestID, Coverage: []string{}, Entities: []session.EntityProposal{{Ref: "ari", Name: "Ari", Kind: "person"}}, SecuritySignals: []session.ExtractionSignal{}}
		for _, segment := range input.Window.Core {
			output.Coverage = append(output.Coverage, segment.Ref)
		}
		ref := input.Window.Core[0].Ref
		output.Relationships = []session.RelationshipProposal{{Ref: "uses", SubjectRef: "ari", Predicate: "session_uses", ObjectValue: &session.ValueProposal{Type: "string", Value: encoded}, Polarity: "+", Citations: []session.Citation{{StartRef: ref, EndRef: ref}}, KnownEvidenceIDs: []string{}}}
		return output, nil
	case "dense_mem_session_linking_v1":
		var input session.LinkingRequest
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		output := session.LinkingResponse{RequestID: input.RequestID, Groups: []session.EntityGroup{}}
		for _, entity := range input.Entities {
			output.Groups = append(output.Groups, session.EntityGroup{Ref: entity.Ref, CanonicalRef: entity.Ref, Members: []string{entity.Ref}})
		}
		return output, nil
	case assessor.SemanticAssessmentSchemaName:
		var input assessor.SemanticAssessmentRequest
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		output := assessor.SemanticAssessmentResponse{RequestID: input.RequestID, EvidenceSecurityResults: []assessor.SemanticAssessmentEvidenceSecurityResult{}, EvidenceEquivalenceResults: []assessor.SemanticAssessmentEvidenceEquivalenceResult{}, EvidenceConflictResults: []assessor.SemanticAssessmentEvidenceConflictResult{}, EntityResults: []assessor.SemanticAssessmentEntityResult{}, RelationshipResults: []assessor.SemanticAssessmentRelationshipResult{}}
		for _, evidence := range input.Evidence {
			output.EvidenceSecurityResults = append(output.EvidenceSecurityResults, assessor.SemanticAssessmentEvidenceSecurityResult{EvidenceID: evidence.EvidenceID, Decision: "pass", Signals: []assessor.SemanticAssessmentSecuritySignal{}})
		}
		for _, group := range input.EvidenceEquivalenceCandidates {
			output.EvidenceEquivalenceResults = append(output.EvidenceEquivalenceResults, assessor.SemanticAssessmentEvidenceEquivalenceResult{EvidenceID: group.EvidenceID, Action: "new"})
		}
		for _, entity := range input.SubmittedEntities {
			grounding := entity.Groundings[0].GroundingRef
			result := assessor.SemanticAssessmentEntityResult{Ref: entity.Ref, GroundingRef: &grounding, Action: "create"}
			for _, group := range input.EntityCandidateGroups {
				if group.GroundingRef == grounding && len(group.Candidates) == 1 && group.Candidates[0].Kind == entity.Kind {
					result.Action = "reuse"
					id := group.Candidates[0].EntityID
					result.CandidateEntityID = &id
				}
			}
			output.EntityResults = append(output.EntityResults, result)
		}
		for _, relationship := range input.SubmittedRelationships {
			evidence := input.Evidence[0]
			refs := regexp.MustCompile(`⟦([^⟧]+)⟧`).FindAllStringSubmatch(evidence.BoundaryText, -1)
			span := func(start, end int) assessor.SemanticAssessmentGroundedRange {
				return assessor.SemanticAssessmentGroundedRange{EvidenceID: evidence.EvidenceID, StartRef: refs[start][1], EndRef: refs[end][1]}
			}
			predicateStart := len([]rune(evidence.Content[:strings.Index(evidence.Content, "uses")]))
			valueStart := len([]rune(evidence.Content[:strings.Index(evidence.Content, relationship.ObjectValue.CanonicalValue)]))
			valueRange := span(valueStart, valueStart+len([]rune(relationship.ObjectValue.CanonicalValue)))
			split := assessor.SemanticAssessmentRelationshipSplit{SubjectRef: relationship.SubjectRef, PredicateStatus: "registration_required", PredicateRegistration: &assessor.SemanticAssessmentPredicateRegistration{PredicateKey: relationship.PredicateHint, RelationshipKind: "state", CurrentCardinality: "many"}, PredicateRange: span(predicateStart, predicateStart+4), ObjectValue: relationship.ObjectValue, ValueRange: &valueRange, Polarity: relationship.Polarity, SupportRanges: []assessor.SemanticAssessmentGroundedRange{span(0, len([]rune(evidence.Content)))}}
			for _, option := range input.PredicateOptions {
				if option.PredicateKey == relationship.PredicateHint {
					key, version := option.PredicateKey, option.Version
					split.PredicateStatus = "resolved"
					split.PredicateRegistration = nil
					split.PredicateKey = &key
					split.PredicateVersion = &version
				}
			}
			output.RelationshipResults = append(output.RelationshipResults, assessor.SemanticAssessmentRelationshipResult{Ref: relationship.Ref, Disposition: "stored", Splits: []assessor.SemanticAssessmentRelationshipSplit{split}})
		}
		return output, nil
	default:
		return nil, fmt.Errorf("unsupported fixture schema %q", schema)
	}
}
