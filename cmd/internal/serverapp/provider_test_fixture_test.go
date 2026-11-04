package serverapp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/operations"
	decisionprovider "github.com/markhuangai/dense-mem/internal/provider/decision"
	"github.com/stretchr/testify/require"
)

const providerTestSecret = "provider-test-configured-operational-credential"

type providerTestPolicy struct {
	value  domain.OntologyMaintenanceConfig
	active domain.OntologyMaintenanceConfig
	err    error
}

func (p *providerTestPolicy) ProviderPolicies(context.Context) (domain.OntologyMaintenanceConfig, domain.OntologyMaintenanceConfig, error) {
	active := p.active
	if active.Model == "" {
		active = p.value
	}
	return active, p.value, p.err
}

type providerTestFixture struct {
	server     *httptest.Server
	config     config.Config
	status     atomic.Int32
	malformed  atomic.Bool
	disconnect atomic.Bool
	hold       atomic.Bool
	entered    chan struct{}
	calls      atomic.Int32
	logs       bytes.Buffer
	policy     providerTestPolicy
	deps       providerTestDependencies
	service    *operations.ProviderTestService
}

func newProviderTestFixture(t *testing.T, vendor string) *providerTestFixture {
	t.Helper()
	f := &providerTestFixture{entered: make(chan struct{}, 1)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.hold.Load() {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case f.entered <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		require.Equal(t, "Bearer "+providerTestSecret, r.Header.Get("Authorization"))
		if f.disconnect.Load() {
			connection, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			require.NoError(t, connection.Close())
			return
		}
		if status := f.status.Load(); status != 0 {
			w.WriteHeader(int(status))
			_, _ = io.WriteString(w, `{"error":{"message":"provider-test-configured-operational-credential","token":"admitted content"}}`)
			return
		}
		if f.malformed.Load() {
			_, _ = io.WriteString(w, `not JSON`)
			return
		}
		var request map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		var model string
		require.NoError(t, json.Unmarshal(request["model"], &model))
		var response any
		switch r.URL.Path {
		case "/chat/completions":
			var messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			require.NoError(t, json.Unmarshal(request["messages"], &messages))
			var sample struct {
				RequestID string `json:"request_id"`
				Evidence  []struct {
					EvidenceID string `json:"evidence_id"`
					Content    string `json:"content"`
				} `json:"evidence"`
			}
			require.NoError(t, json.Unmarshal([]byte(messages[1].Content), &sample))
			require.Equal(t, providerTestSampleText, sample.Evidence[0].Content)
			content, err := json.Marshal(map[string]any{"request_id": sample.RequestID, "evidence_security_results": []any{map[string]any{"evidence_id": sample.Evidence[0].EvidenceID, "decision": "pass", "signals": []any{}}}, "evidence_equivalence_results": []any{}, "evidence_conflict_results": []any{}, "entity_results": []any{}, "relationship_results": []any{}})
			require.NoError(t, err)
			response = map[string]any{"model": model, "choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}}
		case "/embeddings":
			response = map[string]any{"model": model, "data": []any{map[string]any{"index": 0, "embedding": []float32{0.25, 0.75}}}}
		case "/decisions":
			response = map[string]any{"model": model, "answers": []any{map[string]any{"name": "connectivity", "type": "predicate", "probability": 0.999}}, "usage": map[string]int{"input_tokens": 20, "output_tokens": 0}}
		case "/systemone":
			response = map[string]any{"model": model, "answers": map[string]any{"connectivity": map[string]any{"type": "noul", "noul": 0.999}}, "usage": map[string]int{"input_tokens": 20, "output_tokens": 0}}
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	t.Cleanup(f.server.Close)
	f.config = config.Config{AIAPIURL: f.server.URL, AIAPIKey: providerTestSecret, AIEmbeddingModel: "embedding-fixture", AIEmbeddingDimensions: 2, AIEmbeddingTimeoutSeconds: 2, AIEmbeddingMaxConcurrency: 1, AIVerifierModel: "generative-fixture", AIVerifierTokenizer: "o200k_base", AIVerifierTimeoutSeconds: 2, AIVerifierMaxConcurrency: 1, Decision: config.DecisionProviderConfig{Provider: vendor, APIURL: f.server.URL, APIKey: providerTestSecret, Model: "decision-fixture", MaxInputTokens: 32000, MaxQuestionInputTokens: 32000, TimeoutSeconds: 2, MaxConcurrency: 1}}
	f.policy.value = domain.OntologyMaintenanceConfig{Model: f.config.GetAIRememberModel(), SettingsVersion: "ontology-1"}
	protector := observability.NewCredentialProtector(providerTestSecret, "control-test-authentication")
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(&f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := observability.DiagnosticHTTPClient(&http.Client{Timeout: 2 * time.Second}, logger.ContextSlog(), protector)
	embedder := embedding.NewOpenAIEmbeddingProvider(&f.config, client)
	var decisions modelprovider.DecisionProvider
	if vendor != "disabled" {
		var err error
		decisions, err = decisionprovider.New(f.config.Decision, "o200k_base", observability.DiagnosticHTTPClient(&http.Client{}, nil, protector), logger, protector, nil)
		require.NoError(t, err)
	}
	f.deps = providerTestDependencies{config: f.config, client: client, gate: modelprovider.NewConcurrencyGate(1), embeddings: embedder, decisions: decisions, maintenance: &f.policy, logger: logger, protector: protector}
	f.service = buildProviderTests(f.deps)
	return f
}
