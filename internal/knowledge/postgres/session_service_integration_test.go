//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/markhuangai/dense-mem/internal/session/extraction"
	service "github.com/markhuangai/dense-mem/internal/session/service"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
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

func TestEvidenceOnlyPostgresMigratedSessionResumesUnchangedRequest(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDBAtEvidenceOnlyBoundary(t, true)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "migrated-session")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	other := createLedgerProfile(t, admin, rls, team, "other")
	ctx, original := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpacePrivate)
	otherCtx, _ := sessionIntakeFixture(t, admin, app, rls, team, other, domain.MemorySpacePrivate)
	store := NewStore(app, rls)
	type retainedSession struct {
		intake session.Intake
		id     string
	}
	retained := map[string]retainedSession{}
	for _, mode := range []string{"unfinished", "retryable", "policy", "stale"} {
		intake := original
		intake.Request.IdempotencyKey = "migrated-" + mode
		intake.Request.SessionID = mode
		intake.ExtractionVersion = "session_factual_extraction_v1"
		var err error
		intake.RequestHash, err = service.RequestHash(intake.Request)
		require.NoError(t, err)
		staged, err := store.StageSession(ctx, intake)
		require.NoError(t, err)
		require.NoError(t, store.SaveSessionExtraction(ctx, intake.Scope, staged.ID, 0, json.RawMessage(`{"entities":[],"relationships":[],"coverage":[]}`)))
		if mode == "retryable" || mode == "policy" {
			code := "provider_unavailable"
			if mode == "policy" {
				code = "submission_policy_rejected"
			}
			require.NoError(t, store.RecordSessionFailure(ctx, intake.Scope, staged.ID, session.Result{ContractVersion: "dense-mem.v2.7.0", SubmissionID: staged.ID, ProcessingState: "failed", SearchState: "not_required", Errors: []session.Error{{Code: code, Retryable: mode == "retryable"}}}))
		}
		retained[mode] = retainedSession{intake: intake, id: staged.ID}
	}
	migrator, err := storagepostgres.NewMigrator(admin)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(ctx))
	installOwnerSearchContract(t, admin, rls, "migrated-session", 3, 1)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var envelope struct {
			Messages       []modelprovider.Message `json:"messages"`
			ResponseFormat struct {
				JSONSchema struct{ Name string } `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil || len(envelope.Messages) < 2 {
			http.Error(w, "invalid request", 400)
			return
		}
		response, err := sessionFactsProviderResponse(envelope.ResponseFormat.JSONSchema.Name, []byte(envelope.Messages[1].Content))
		if err != nil {
			http.Error(w, "unsupported fixture request", 400)
			return
		}
		body, err := json.Marshal(response)
		if err != nil {
			http.Error(w, "invalid response", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(body)}}}})
	}))
	defer server.Close()
	transport := assessorprovider.NewOpenAIAssessor(&config.Config{AIVerifierAPIURL: server.URL, AIVerifierAPIKey: "migration-provider-canary", AIVerifierModel: "fixture", AIVerifierMaxConcurrency: 4}, server.Client())
	path := filepath.Join(t.TempDir(), "session-resume.jsonl")
	output, err := os.Create(path)
	require.NoError(t, err)
	defer output.Close()
	previous := os.Stdout
	os.Stdout = output
	protector := observability.NewCredentialProtector("migration-provider-canary")
	logger := observability.NewWithProtector(slog.LevelDebug, protector)
	os.Stdout = previous
	preparer := processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store, Assessor: transport, EmbeddingExecutor: semanticwrite.NewProviderExecutor(&sessionFactsEmbedder{}), Limits: assessor.DefaultSemanticAssessmentLimits(), DiagnosticProtector: protector})
	api := service.NewService(service.Dependencies{
		Enabled: true, Repository: store, Preparer: preparer, Tokenizer: "o200k_base",
		Extractor: extraction.NewProvider(transport, "fixture", assessor.DefaultSemanticAssessmentLimits()), Logger: logger,
		DiagnosticProtector: protector,
		DiagnosticRecorder:  func() modelprovider.SnapshotRecorder { return processor.NewDiagnosticRecorder(protector) },
		DiagnosticLog: func(ctx context.Context, scope session.Scope, id string, body []byte) {
			observability.LogDiagnosticBody(ctx, logger.ContextSlog(), protector, "session_evidence_diagnostics", body, 64<<20,
				slog.String("team_id", scope.TeamID), slog.String("owner_profile_id", scope.OwnerProfileID),
				slog.String("submission_id", id), slog.String("space_id", scope.SpaceID), slog.Int64("space_generation", scope.SpaceGeneration))
		},
	})
	for _, mode := range []string{"unfinished", "retryable"} {
		item := retained[mode]
		loaded, err := store.LookupSession(ctx, item.intake.Scope, item.intake.Request.IdempotencyKey)
		require.NoError(t, err)
		require.Equal(t, item.intake.RequestHash, loaded.Intake.RequestHash)
		require.Equal(t, item.intake.Request, loaded.Intake.Request)
		require.Empty(t, loaded.Extractions)
		_, err = store.LookupSession(otherCtx, item.intake.Scope, item.intake.Request.IdempotencyKey)
		require.ErrorIs(t, err, session.ErrUnauthorized)
		changed := item.intake.Request
		changed.Events = []session.Event{{EventID: "one", Text: "Changed immutable request"}}
		_, err = api.Ingest(ctx, changed)
		require.ErrorIs(t, err, session.ErrRequestConflict)
		completed, err := api.Ingest(ctx, item.intake.Request)
		require.NoError(t, err)
		require.Equal(t, item.id, completed.SubmissionID)
		require.Equal(t, "stored", completed.MemoryOutcome)
		require.Len(t, completed.Events[0].EvidenceIDs, 1)
		var content string
		require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT content FROM evidence_occurrences WHERE team_id=?::uuid AND space_id=?::uuid AND ingest_id=?::uuid`, team, item.intake.Scope.SpaceID, item.id).Row().Scan(&content)
		}))
		require.Equal(t, item.intake.Request.Events[0].Text, content)
		before := calls.Load()
		replay, err := api.Ingest(ctx, item.intake.Request)
		require.NoError(t, err)
		require.Equal(t, completed, replay)
		require.Equal(t, before, calls.Load())
	}
	before := calls.Load()
	policy, err := api.Ingest(ctx, retained["policy"].intake.Request)
	require.NoError(t, err)
	require.True(t, policy.RetiredPayload)
	require.Equal(t, "submission_policy_rejected", policy.Errors[0].Code)
	require.Equal(t, before, calls.Load())
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE id=?::uuid`, original.Scope.SpaceID).Error
	}))
	_, err = api.Ingest(ctx, retained["stale"].intake.Request)
	require.Error(t, err)
	require.Equal(t, before, calls.Load())
	require.NoError(t, output.Close())
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(body), "migration-provider-canary")
	seen := map[string]bool{}
	correlations := map[string]string{}
	chunks := map[string][]observability.DiagnosticBodyChunk{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		if event["msg"] == "session_evidence_diagnostics" {
			require.Equal(t, "DEBUG", event["level"])
			require.Equal(t, team, event["team_id"])
			require.Equal(t, owner, event["owner_profile_id"])
			require.Equal(t, original.Scope.SpaceID, event["space_id"])
			require.Equal(t, float64(original.Scope.SpaceGeneration), event["space_generation"])
			encoded, err := json.Marshal(event["payload_chunk"])
			require.NoError(t, err)
			var chunk observability.DiagnosticBodyChunk
			require.NoError(t, json.Unmarshal(encoded, &chunk))
			id := event["submission_id"].(string)
			require.Equal(t, correlations[id], event["correlation_id"])
			chunks[id] = append(chunks[id], chunk)
		}
		if event["msg"] != "session_evidence_only_resume" {
			continue
		}
		require.Equal(t, "INFO", event["level"])
		require.Equal(t, team, event["team_id"])
		require.Equal(t, owner, event["owner_profile_id"])
		require.Contains(t, []string{"unfinished", "failed"}, event["original_outcome"])
		require.NotEmpty(t, event["request_hash"])
		require.Equal(t, "session_factual_extraction_v1", event["original_version"])
		require.Equal(t, session.ExtractionVersion, event["extraction_version"])
		require.Equal(t, float64(1), event["cleared_checkpoints"])
		require.Equal(t, "resume_authorized", event["outcome"])
		seen[event["submission_id"].(string)] = true
		correlations[event["submission_id"].(string)] = event["correlation_id"].(string)
	}
	require.Equal(t, map[string]bool{retained["unfinished"].id: true, retained["retryable"].id: true}, seen)
	require.Len(t, chunks, 2)
	for _, mode := range []string{"unfinished", "retryable"} {
		item := retained[mode]
		captured := chunks[item.id]
		require.NotEmpty(t, captured)
		parts := make([]string, len(captured))
		indices := map[int]bool{}
		for _, chunk := range captured {
			require.Equal(t, "captured", chunk.State)
			require.Equal(t, len(captured), chunk.Count)
			require.GreaterOrEqual(t, chunk.Index, 0)
			require.Less(t, chunk.Index, len(parts))
			require.False(t, indices[chunk.Index])
			require.LessOrEqual(t, len(chunk.Body), 2048)
			require.Equal(t, captured[0].SHA256, chunk.SHA256)
			require.Equal(t, captured[0].OriginalBytes, chunk.OriginalBytes)
			indices[chunk.Index] = true
			parts[chunk.Index] = chunk.Body
		}
		encoded := []byte(strings.Join(parts, ""))
		require.Equal(t, captured[0].OriginalBytes, len(encoded))
		require.Equal(t, captured[0].SHA256, fmt.Sprintf("%x", sha256.Sum256(encoded)))
		var diagnostic struct {
			CaptureState string `json:"capture_state"`
			Payload      struct {
				Request   session.Request                  `json:"request"`
				Result    session.Result                   `json:"result"`
				Exchanges []modelprovider.ProviderExchange `json:"exchanges"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(encoded, &diagnostic))
		require.Equal(t, "captured", diagnostic.CaptureState)
		require.Equal(t, item.intake.Request, diagnostic.Payload.Request)
		require.Equal(t, item.id, diagnostic.Payload.Result.SubmissionID)
		require.Equal(t, correlations[item.id], diagnostic.Payload.Result.CorrelationID)
		require.Equal(t, domain.ContractVersion, diagnostic.Payload.Result.ContractVersion)
		require.Equal(t, "completed", diagnostic.Payload.Result.ProcessingState)
		require.Equal(t, "stored", diagnostic.Payload.Result.MemoryOutcome)
		require.Equal(t, "current", diagnostic.Payload.Result.SearchState)
		require.Empty(t, diagnostic.Payload.Result.Errors)
		require.Len(t, diagnostic.Payload.Result.Events[0].EvidenceIDs, 1)
		require.GreaterOrEqual(t, len(diagnostic.Payload.Exchanges), 2)
		exactText, err := json.Marshal(item.intake.Request.Events[0].Text)
		require.NoError(t, err)
		for _, exchange := range diagnostic.Payload.Exchanges {
			require.Equal(t, "captured", exchange.CaptureState)
			require.Equal(t, http.StatusOK, exchange.StatusCode)
			require.NotEmpty(t, exchange.RequestBody)
			require.NotEmpty(t, exchange.ResponseBody)
			var request struct {
				Messages []modelprovider.Message `json:"messages"`
			}
			require.NoError(t, json.Unmarshal(exchange.RequestBody, &request))
			var userMessage string
			for _, message := range request.Messages {
				if message.Role == "user" {
					userMessage += message.Content
				}
			}
			require.Contains(t, userMessage, string(exactText))
			require.False(t, exchange.StartedAt.IsZero())
			require.False(t, exchange.CompletedAt.Before(exchange.StartedAt))
		}
	}
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
	out := session.ExtractionResponse{RequestID: input.RequestID, Coverage: []string{}, Excerpts: []session.Excerpt{}, SecuritySignals: []session.ExtractionSignal{}}
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
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpacePrivate)
	intake.Request.Events[0].Text = "Thanks. Let's continue."
	store := NewStore(app, rls)
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
	base, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpacePrivate)
	store := NewStore(app, rls)
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			req := intake.Request
			req.IdempotencyKey = mode
			req.Events = []session.Event{{EventID: mode, Text: "Thanks."}}
			outbound := &sessionOutbound{block: true, entered: make(chan struct{}, 1)}
			api := service.NewService(service.Dependencies{Enabled: true, Repository: store, Preparer: processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store}), Tokenizer: "o200k_base", Extractor: extraction.NewProvider(outbound, "fixture", assessor.DefaultSemanticAssessmentLimits()), DiagnosticProtector: observability.NewCredentialProtector("session-credential-canary")})
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
			var diagnostic []byte
			require.NoError(t, rls.WithSystemTx(base, admin, func(tx *gorm.DB) error {
				return tx.Raw(`SELECT body FROM session_submission_diagnostics WHERE submission_id=?::uuid`, retained.ID).Row().Scan(&diagnostic)
			}))
			require.JSONEq(t, fmt.Sprintf(`{"event_id":%q,"text":"Thanks."}`, mode), extractSessionDiagnosticEvent(t, diagnostic))
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

func extractSessionDiagnosticEvent(t *testing.T, body []byte) string {
	t.Helper()
	var diagnostic struct {
		Payload struct{ Request session.Request } `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(body, &diagnostic))
	require.Len(t, diagnostic.Payload.Request.Events, 1)
	event, err := json.Marshal(diagnostic.Payload.Request.Events[0])
	require.NoError(t, err)
	return string(event)
}

func TestSessionServiceBudgetRejectionHasNoIntake(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-budget")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpacePrivate)
	store := NewStore(app, rls)
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
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpacePrivate)
	intake.Request.Events[0].Text = strings.Repeat("Okay. ", 6000)
	var err error
	intake.Windows, err = service.BuildWindows(intake.Request, "o200k_base")
	require.NoError(t, err)
	require.Greater(t, len(intake.Windows), 1)
	intake.RequestHash, err = service.RequestHash(intake.Request)
	require.NoError(t, err)
	store := NewStore(app, rls)
	staged, err := store.StageSession(ctx, intake)
	require.NoError(t, err)
	window := staged.Intake.Windows[0]
	checkpoint := session.ExtractionResponse{RequestID: fmt.Sprintf("session:%s:window:%d", staged.ID, window.Index), Coverage: []string{}, Excerpts: []session.Excerpt{}, SecuritySignals: []session.ExtractionSignal{}}
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
	base, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpacePrivate)
	store := NewStore(app, rls)
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

func TestEvidenceOnlyPostgresSessionExcerptsDiagnosticsAndCheckpointRecovery(t *testing.T) {
	admin, app, rls, cleanup := setupKnowledgeOwnerDB(t)
	defer cleanup()
	team := createOwnerTeam(t, admin, rls, "session-facts")
	owner := createOwnerProfile(t, admin, rls, team, "owner")
	installOwnerSearchContract(t, admin, rls, "session-facts", 3, 1)
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpacePrivate)
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
		if envelope.ResponseFormat.JSONSchema.Name == "session_evidence_excerpts_v2" {
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
	store := NewStore(app, rls)
	embedder := &sessionFactsEmbedder{}
	protector := observability.NewCredentialProtector("fixture-provider-key")
	preparer := processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store, Assessor: transport, EmbeddingExecutor: semanticwrite.NewProviderExecutor(embedder), Limits: assessor.DefaultSemanticAssessmentLimits(), DiagnosticProtector: protector})
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
	require.Equal(t, "stored", first.MemoryOutcome)
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
	var excerpts []string
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT content FROM evidence_occurrences WHERE team_id=?::uuid AND space_id=?::uuid ORDER BY created_at,occurrence_id`, team, intake.Scope.SpaceID).Scan(&excerpts).Error
	}))
	require.Equal(t, []string{intake.Request.Events[0].Text, "Ari also uses Rust."}, excerpts)
	failedRequest := intake.Request
	failedRequest.IdempotencyKey = "late-embedding-failure"
	failedRequest.Events = []session.Event{{EventID: "three", Text: "Ari uses Redis."}}
	embedder.failOnCall.Store(embedder.calls.Load() + 1)
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
	case assessor.PartitionSchemaName:
		var input assessor.PartitionRequest
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		output := assessor.PartitionResponse{RequestID: input.RequestID, Items: make([]assessor.PartitionResult, len(input.Items))}
		for i, item := range input.Items {
			refs := regexp.MustCompile(`⟦([^⟧]+)⟧`).FindAllStringSubmatch(item.BoundaryText, -1)
			output.Items[i] = assessor.PartitionResult{ItemID: item.ItemID, EndRefs: []string{refs[len(refs)-1][1]}}
		}
		return output, nil
	case "session_evidence_excerpts_v2":
		var input session.ExtractionRequest
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		output := session.ExtractionResponse{RequestID: input.RequestID, Coverage: []string{}, Excerpts: []session.Excerpt{}, SecuritySignals: []session.ExtractionSignal{}}
		for _, segment := range input.Window.Core {
			output.Coverage = append(output.Coverage, segment.Ref)
			output.Excerpts = append(output.Excerpts, session.Excerpt{EventIndex: segment.EventIndex, Start: segment.Start, End: segment.End, Content: segment.Text})
		}
		return output, nil
	case assessor.SemanticAssessmentSchemaName:
		var input assessor.SemanticAssessmentRequest
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		output := assessor.SemanticAssessmentResponse{RequestID: input.RequestID, EvidenceSecurityResults: []assessor.SemanticAssessmentEvidenceSecurityResult{}, EvidenceEquivalenceResults: []assessor.SemanticAssessmentEvidenceEquivalenceResult{}, EvidenceConflictResults: []assessor.SemanticAssessmentEvidenceConflictResult{}}
		for _, evidence := range input.Evidence {
			output.EvidenceSecurityResults = append(output.EvidenceSecurityResults, assessor.SemanticAssessmentEvidenceSecurityResult{EvidenceID: evidence.EvidenceID, Decision: "pass", Signals: []assessor.SemanticAssessmentSecuritySignal{}})
		}
		for _, group := range input.EvidenceEquivalenceCandidates {
			output.EvidenceEquivalenceResults = append(output.EvidenceEquivalenceResults, assessor.SemanticAssessmentEvidenceEquivalenceResult{EvidenceID: group.EvidenceID, Action: "new"})
		}
		return output, nil
	default:
		return nil, fmt.Errorf("unsupported fixture schema %q", schema)
	}
}
