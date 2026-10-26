//go:build integration

package serverapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	rememberprocessor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestRememberEvaluationImporterAdmission(t *testing.T) {
	t.Run("default completes concurrent imports and replays", func(t *testing.T) {
		h := newImportAdmissionHarness(t, 5)
		corpus := admissionCorpus(8, 0)
		done := make(chan error, 1)
		var imported evalharness.KnowledgeMapping
		go func() {
			var err error
			imported, err = h.client.ImportCorpusWithConcurrency(h.ctx, corpus, evalharness.DefaultImportConcurrency)
			done <- err
		}()
		h.waitEntered(t, 4)
		h.release()
		require.NoError(t, receiveImportResult(t, h.ctx, done))
		require.Len(t, imported.BySourceDocID, len(corpus))
		require.EqualValues(t, 4, h.provider.peak.Load())
		require.Zero(t, h.busyCount.Load())
		replayed, err := h.client.ImportCorpusWithConcurrency(h.ctx, corpus, 4)
		require.NoError(t, err)
		require.Equal(t, imported, replayed)
		require.EqualValues(t, len(corpus), h.provider.calls.Load())
		h.assertCompleted(t, len(corpus))
	})

	for _, test := range []struct {
		name           string
		pool, admitted int
		workers        int
	}{
		{"former default exceeds admission", 5, 4, 10},
		{"small pool admits fewer than default", 2, 1, evalharness.DefaultImportConcurrency},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newImportAdmissionHarness(t, test.pool)
			ctx, cancel := context.WithCancel(h.ctx)
			defer cancel()
			corpus := admissionCorpus(test.workers, 0)
			done := make(chan error, 1)
			go func() {
				_, err := h.client.ImportCorpusWithConcurrency(ctx, corpus, test.workers)
				done <- err
			}()
			h.waitEntered(t, test.admitted)
			select {
			case public := <-h.busy:
				require.Equal(t, "database_failure", public["code"])
				require.Equal(t, "idempotency_lock_busy", public["reason_code"])
				require.Equal(t, true, public["retryable"])
				require.Equal(t, "retry_same_request", public["next_action"])
				require.Contains(t, public["message"], "Reduce concurrent requests")
				require.Contains(t, public["remediation"], "same idempotency_key")
			case <-h.ctx.Done():
				t.Fatal("excess imports did not report busy admission")
			}
			h.assertCompleted(t, 0)
			cancel()
			require.Error(t, receiveImportResult(t, h.ctx, done))
			h.release()
			h.waitIdle(t)
			recovered, err := h.client.ImportCorpusWithConcurrency(h.ctx, corpus, 1)
			require.NoError(t, err)
			require.Len(t, recovered.BySourceDocID, len(corpus))
			h.assertCompleted(t, len(corpus))
		})
	}

	t.Run("stateless cancellation releases the only admission slot", func(t *testing.T) {
		h := newImportAdmissionHarness(t, 2)
		corpus := admissionCorpus(1, 0)
		ctx, cancel := context.WithCancel(h.ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- h.callStatelessRemember(ctx, corpus[0]) }()
		h.waitEntered(t, 1)
		cancel()
		require.ErrorIs(t, receiveImportResult(t, h.ctx, done), context.Canceled)
		select {
		case <-h.provider.cancelled:
		case <-h.ctx.Done():
			t.Fatal("stateless Remember did not cancel held provider work")
		}
		h.waitIdle(t)
		h.assertCompleted(t, 0)
		h.release()
		recovered, err := h.client.ImportCorpus(h.ctx, corpus)
		require.NoError(t, err)
		require.Len(t, recovered.BySourceDocID, 1)
		h.assertCompleted(t, 1)
	})

	t.Run("interruption retains completed mappings and resumes", func(t *testing.T) {
		h := newImportAdmissionHarness(t, 5)
		corpus := admissionCorpus(5, 2)
		path := filepath.Join(t.TempDir(), "corpus.jsonl")
		var contents strings.Builder
		for _, item := range corpus {
			encoded, err := json.Marshal(item)
			require.NoError(t, err)
			contents.Write(encoded)
			contents.WriteByte('\n')
		}
		require.NoError(t, os.WriteFile(path, []byte(contents.String()), 0o600))
		ctx, cancel := context.WithCancel(h.ctx)
		defer cancel()
		var completed evalharness.KnowledgeMapping
		var count int
		done := make(chan error, 1)
		go func() {
			var err error
			completed, count, err = h.client.ImportCorpusFileWithConcurrency(ctx, path, 1, nil, nil)
			done <- err
		}()
		h.waitEntered(t, 1)
		cancel()
		require.Error(t, receiveImportResult(t, h.ctx, done))
		require.Equal(t, 2, count)
		require.Len(t, completed.BySourceDocID, 2)
		h.release()
		h.waitIdle(t)
		h.assertCompleted(t, 3)
		var live []struct{ SourceDocID, EvidenceID string }
		require.NoError(t, h.rls.WithSystemTx(h.ctx, h.admin, func(tx *gorm.DB) error {
			return tx.Raw(`
				SELECT fragment.metadata->>'source_doc_id' AS source_doc_id,
				       fragment.fragment_id::text AS evidence_id
				FROM evidence_fragments AS fragment
				JOIN remember_attempts AS attempt
				  ON attempt.team_id = fragment.team_id AND attempt.attempt_id = fragment.ingest_id
				WHERE fragment.team_id = ?::uuid AND attempt.outcome = 'completed'
			`, h.teamID).Scan(&live).Error
		}))
		require.Len(t, live, 3)
		skip := make(map[string]struct{})
		for _, item := range live {
			if ref, exists := completed.BySourceDocID[item.SourceDocID]; exists {
				require.Equal(t, ref.ID, item.EvidenceID)
			}
			completed.BySourceDocID[item.SourceDocID] = evalharness.Ref{Type: "fragment", ID: item.EvidenceID, SourceDocID: item.SourceDocID}
			skip[item.SourceDocID] = struct{}{}
		}
		resumed, count, err := h.client.ImportCorpusFileWithConcurrency(h.ctx, path, evalharness.DefaultImportConcurrency, nil, skip)
		require.NoError(t, err)
		require.Equal(t, len(corpus), count)
		require.Len(t, resumed.BySourceDocID, 2)
		for source, ref := range completed.BySourceDocID {
			require.NotContains(t, resumed.BySourceDocID, source)
			resumed.BySourceDocID[source] = ref
		}
		replayed, err := h.client.ImportCorpusWithConcurrency(h.ctx, corpus, 4)
		require.NoError(t, err)
		require.Equal(t, resumed.BySourceDocID, replayed.BySourceDocID)
		h.assertCompleted(t, len(corpus))
	})
}

type importAdmissionAssessor struct {
	gate      <-chan struct{}
	entered   chan struct{}
	cancelled chan struct{}
	calls     atomic.Int32
	active    atomic.Int32
	peak      atomic.Int32
}

func (p *importAdmissionAssessor) Assess(ctx context.Context, request assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	p.calls.Add(1)
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for peak := p.peak.Load(); active > peak; peak = p.peak.Load() {
		if p.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	if strings.Contains(request.Evidence[0].Content, "Admission checkpoint pending.") {
		p.entered <- struct{}{}
		select {
		case <-p.gate:
		case <-ctx.Done():
			p.cancelled <- struct{}{}
			return nil, assessor.SemanticAssessmentTurn{}, ctx.Err()
		}
	}
	return (&rememberProcessorIntegrationRegistrationAssessor{}).Assess(ctx, request)
}

func (*importAdmissionAssessor) Repair(context.Context, assessor.SemanticAssessmentSession, assessor.SemanticAssessmentRepairRequest) (assessor.SemanticAssessmentTurn, error) {
	return assessor.SemanticAssessmentTurn{}, fmt.Errorf("admission fixture unexpectedly required response repair")
}

func (*importAdmissionAssessor) ModelName() string {
	return "remember-integration-registration-assessor"
}

type importAdmissionHarness struct {
	ctx            context.Context
	client         *evalharness.HTTPClient
	provider       *importAdmissionAssessor
	admin          *gorm.DB
	app            *gorm.DB
	rls            *storagepostgres.RLS
	teamID         uuid.UUID
	busy           chan map[string]any
	busyCount      atomic.Int32
	activeRequests atomic.Int32
	release        func()
}

func newImportAdmissionHarness(t *testing.T, pool int) *importAdmissionHarness {
	t.Helper()
	admin, app, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	t.Cleanup(cleanup)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	teamID, ownerID := uuid.New(), uuid.New()
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec("INSERT INTO teams (id, name, description, metadata, config) VALUES (?::uuid, ?, '', '{}'::jsonb, '{}'::jsonb)", teamID, "import admission "+teamID.String()).Error
	}))
	credentials := accesspostgres.NewCredentialRepository(admin, rls, nil)
	require.NoError(t, credentials.CreateCredential(ctx, &domain.Credential{
		ID: ownerID, TeamID: teamID, Name: "import admission owner", Role: "member", Scopes: []string{"read", "write"},
		KeyHash: ownerID.String(), KeyPrefix: strings.ReplaceAll(ownerID.String(), "-", "")[:24], KeySuffix: "owner",
	}))
	credential, err := credentials.GetByIDForTeam(ctx, teamID, ownerID)
	require.NoError(t, err)
	model, dimensions := ensureRememberProcessorIntegrationSearchContract(t, admin, rls)
	sqlDB, err := app.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(pool)
	sqlDB.SetMaxIdleConns(pool)
	gate := make(chan struct{})
	h := &importAdmissionHarness{
		ctx: ctx, admin: admin, app: app, rls: rls, teamID: teamID, busy: make(chan map[string]any, 64),
		provider: &importAdmissionAssessor{gate: gate, entered: make(chan struct{}, 64), cancelled: make(chan struct{}, 64)},
	}
	h.release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(h.release)
	store := knowledgepostgres.NewStore(app, rls, knowledgecontract.ConflictRuntimeConfig{})
	processor := rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{
		Ledger: store, Catalog: store, Assessor: h.provider,
		Embedder: rememberProcessorIntegrationSearchEmbedder{model: model, dimensions: dimensions},
		Limits:   assessor.DefaultSemanticAssessmentLimits(), Metrics: observability.NoopDiscoverabilityMetrics(),
	})
	reg, err := registry.BuildActive(registry.Dependencies{RememberBindings: registry.RememberBindings{
		Service: rememberapp.NewService(rememberapp.Dependencies{Synchronous: processor}),
	}})
	require.NoError(t, err)
	e := echo.New()
	mcp := handler.NewMCPHandler(reg, nil)
	e.POST("/mcp", mcp.HandlePost)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.activeRequests.Add(1)
		defer h.activeRequests.Add(-1)
		actor := requestctx.Actor{TeamID: teamID, OwnerID: credential.OwnerID, IdentityID: credential.ActorIdentityID, MembershipID: credential.MembershipID, CredentialID: &credential.ID, AuthMethod: "api_key", Role: credential.Role, Grants: credential.Scopes, AllowedSpaces: credential.MemorySpaces()}
		requestCtx := requestctx.WithActor(r.Context(), actor)
		requestCtx = middleware.SetPrincipalForTest(requestCtx, &middleware.Principal{TeamID: teamID, OwnerID: actor.OwnerID, Grants: actor.Grants, AllowedSpaces: actor.AllowedSpaces})
		requestCtx = middleware.SetResolvedTeamIDForTest(requestCtx, teamID)
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, r.WithContext(requestCtx))
		var response struct {
			Result struct {
				StructuredContent struct {
					Errors []map[string]any `json:"errors"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if json.Unmarshal(recorder.Body.Bytes(), &response) == nil {
			for _, failure := range response.Result.StructuredContent.Errors {
				if failure["reason_code"] == "idempotency_lock_busy" {
					h.busyCount.Add(1)
					select {
					case h.busy <- failure:
					default:
					}
				}
			}
		}
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	t.Cleanup(func() { cancel(); h.release(); server.Close() })
	h.client = &evalharness.HTTPClient{BaseURL: server.URL, APIKey: "admission-fixture", Client: server.Client()}
	return h
}

func (h *importAdmissionHarness) waitEntered(t *testing.T, count int) {
	t.Helper()
	for range count {
		select {
		case <-h.provider.entered:
		case <-h.ctx.Done():
			t.Fatal("imports did not reach provider admission")
		}
	}
}

func (h *importAdmissionHarness) callStatelessRemember(ctx context.Context, item evalharness.CorpusItem) error {
	input := map[string]any{
		"target_space": "team_shared", "idempotency_key": "eval:" + item.SourceDocID,
		"evidence": []map[string]any{{"content": item.Content, "source": "eval-seed", "labels": item.Labels,
			"metadata": map[string]any{"source_doc_id": item.SourceDocID, "source_dataset": item.SourceDataset, "eval_seed": true}}},
		"relationships": item.Relationships,
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "remember", "arguments": input,
		"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": map[string]any{}},
	}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.client.BaseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "remember")
	response, err := h.client.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	return err
}

func (h *importAdmissionHarness) waitIdle(t *testing.T) {
	t.Helper()
	db, err := h.app.DB()
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return db.Stats().InUse == 0 && h.provider.active.Load() == 0 && h.activeRequests.Load() == 0
	}, 10*time.Second, 10*time.Millisecond)
}

func (h *importAdmissionHarness) assertCompleted(t *testing.T, want int) {
	t.Helper()
	require.NoError(t, h.rls.WithSystemTx(h.ctx, h.admin, func(tx *gorm.DB) error {
		for _, query := range []string{
			"SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND outcome = 'completed'",
			"SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid",
		} {
			var count int
			if err := tx.Raw(query, h.teamID).Scan(&count).Error; err != nil {
				return err
			}
			require.Equal(t, want, count)
		}
		return nil
	}))
}

func receiveImportResult(t *testing.T, ctx context.Context, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Fatal("import did not finish")
		return ctx.Err()
	}
}

func admissionCorpus(count, heldFrom int) []evalharness.CorpusItem {
	items := make([]evalharness.CorpusItem, count)
	for index := range items {
		name := "Admission specimen " + uuid.NewString()
		value := fmt.Sprintf("distinct import state %d", index)
		content := name + " has " + value + "."
		if index >= heldFrom {
			content += " Admission checkpoint pending."
		}
		items[index] = evalharness.CorpusItem{
			SourceDocID: fmt.Sprintf("admission-%d", index), Content: content, Labels: []string{"admission"},
			Relationships: []any{map[string]any{
				"ref": "state", "subject": map[string]any{"name": name, "entity_kind": "document"},
				"predicate": map[string]any{"proposed_key": fmt.Sprintf("admission_state_%d", index)},
				"object":    map[string]any{"value": map[string]any{"type": "string", "value": value}},
				"polarity":  "+", "evidence_indices": []any{0},
			}},
		}
	}
	return items
}
