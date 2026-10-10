//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	processor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/markhuangai/dense-mem/internal/session/extraction"
	service "github.com/markhuangai/dense-mem/internal/session/service"
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
