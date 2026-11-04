package serverapp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/operations"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
)

const providerTestSampleText = "Dense-Mem provider connectivity sample."

type providerTestDependencies struct {
	config      config.Config
	client      *http.Client
	gate        modelprovider.ConcurrencyGate
	embeddings  embeddingcontract.EmbeddingProviderInterface
	decisions   modelprovider.DecisionProvider
	maintenance interface {
		ProviderPolicies(context.Context) (domain.OntologyMaintenanceConfig, domain.OntologyMaintenanceConfig, error)
	}
	logger    observability.LogProvider
	protector observability.DiagnosticProtector
	metrics   observability.DiscoverabilityMetrics
}

func buildProviderTests(deps providerTestDependencies) *operations.ProviderTestService {
	instance, err := os.Hostname()
	if err != nil {
		instance = "unavailable"
		deps.logger.Warn("provider_test.instance_unavailable", observability.String("error", err.Error()))
	}
	catalog := func(ctx context.Context) ([]operations.ProviderTestBinding, error) {
		cfg := deps.config
		bindings := []operations.ProviderTestBinding{}
		credential := "Generative credentials"
		if cfg.AIVerifierAPIKey == "" {
			credential = "Embedding credentials"
		}
		addModel := func(model, role, revision string) {
			provider := assessorprovider.NewOpenAIAssessorWithAssessmentLimitsAndConcurrencyGateAndModel(&cfg, deps.client, assessorprovider.SemanticAssessmentLimitsForConfig(&cfg), deps.gate, model)
			provider.SetMetrics(deps.metrics)
			target := providerTestTarget(cfg.GetAIVerifierAPIURL(), "/chat/completions", model, "assess", role, credential, revision, cfg.GetAIVerifierTimeoutSeconds())
			target.Available = target.Available && cfg.GetAIVerifierAPIKey() != ""
			if !target.Available {
				target.UnavailableReason = "The generative endpoint, credential, or model is not configured."
			}
			bindings = append(bindings, operations.ProviderTestBinding{Target: target, Execute: func(ctx context.Context) (*modelprovider.CallDiagnostics, error) {
				_, turn, err := provider.Assess(ctx, assessor.SemanticAssessmentRequest{RequestID: uuid.NewString(), TeamID: "provider-test", Evidence: []assessor.SemanticReviewEvidence{{EvidenceID: "provider-test-sample", Content: providerTestSampleText}}})
				if err == nil && len(turn.ValidationErrors) > 0 {
					err = fmt.Errorf("%w: %s: %v", modelprovider.ErrVerifierMalformedResponse, turn.ValidationStage, turn.ValidationErrors)
				}
				return turn.Diagnostics, err
			}})
		}
		addModel(cfg.GetAIVerifierModel(), "Default generative model", "environment")
		addModel(cfg.GetAIRememberModel(), "Remember and session ingestion", "environment")
		addModel(cfg.GetAIConflictReviewModel(), "Conflict review", "environment")
		if deps.maintenance != nil {
			policy, pending, err := deps.maintenance.ProviderPolicies(ctx)
			if err != nil {
				return nil, err
			}
			addModel(policy.Model, "Ontology maintenance", policy.SettingsVersion)
			if pending.Model != policy.Model {
				addModel(pending.Model, "Ontology maintenance (next window)", pending.SettingsVersion)
			}
		}
		embeddingTarget := providerTestTarget(cfg.GetAIAPIURL(), "/embeddings", cfg.GetAIEmbeddingModel(), "embed", "Embeddings", "Embedding credentials", "environment", cfg.GetAIEmbeddingTimeoutSeconds())
		embeddingTarget.Available = embeddingTarget.Available && deps.embeddings != nil && deps.embeddings.IsAvailable()
		if !embeddingTarget.Available {
			embeddingTarget.UnavailableReason = "The embedding provider is not configured."
		}
		bindings = append(bindings, operations.ProviderTestBinding{Target: embeddingTarget, Execute: func(ctx context.Context) (*modelprovider.CallDiagnostics, error) {
			diagnostics := &modelprovider.CallDiagnostics{}
			ctx = modelprovider.WithQueueDiagnostics(ctx, diagnostics)
			_, _, err := deps.embeddings.Embed(ctx, providerTestSampleText)
			return diagnostics, err
		}})
		path := "/decisions"
		if cfg.Decision.Provider == "typesafe" {
			path = "/systemone"
		}
		decisionTarget := providerTestTarget(cfg.Decision.APIURL, path, cfg.Decision.Model, "decide", "Typed decisions", "Decision credentials", "environment", cfg.Decision.TimeoutSeconds)
		decisionTarget.Available = decisionTarget.Available && cfg.Decision.Configured() && deps.decisions != nil
		if !decisionTarget.Available {
			decisionTarget.UnavailableReason = "The optional decision provider is disabled or not configured."
		}
		bindings = append(bindings, operations.ProviderTestBinding{Target: decisionTarget, Execute: func(ctx context.Context) (*modelprovider.CallDiagnostics, error) {
			result, err := deps.decisions.Decide(ctx, modelprovider.DecisionRequest{Workload: "provider_test", OperationID: correlation.FromContext(ctx), SettingsVersion: "sample:" + operations.ProviderTestSampleVersion, Groups: []modelprovider.DecisionGroup{{ID: "provider-test", Reference: "sample", Input: map[string]string{"sample": providerTestSampleText}, Questions: []modelprovider.DecisionQuestion{{Name: "connectivity", Type: modelprovider.DecisionPredicate, Instructions: "Does the sample text describe a provider connectivity sample?"}}}}})
			if len(result.Receipts) > 0 {
				return result.Receipts[len(result.Receipts)-1].Diagnostics, err
			}
			return nil, err
		}})
		return bindings, nil
	}
	return operations.NewProviderTestService(catalog, rootDiagnosticLogger(deps.logger), deps.protector, instance)
}

func providerTestTarget(base, path, model, operation, role, credential, revision string, timeoutSeconds int) operations.ProviderTestTarget {
	endpoint := ""
	if base != "" {
		endpoint = strings.TrimSuffix(base, "/") + path
	}
	parsed, err := url.Parse(endpoint)
	available := err == nil && parsed != nil && parsed.Host != "" && (parsed.Scheme == "https" || parsed.Scheme == "http") && model != ""
	if err == nil && parsed != nil {
		parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
		endpoint = parsed.String()
	} else {
		endpoint = ""
	}
	return operations.ProviderTestTarget{Endpoint: endpoint, Model: model, Operation: operation, Roles: []string{role}, CredentialConfiguration: credential, ConfigurationRevision: revision, Available: available, TimeoutMS: int64(time.Duration(timeoutSeconds) * time.Second / time.Millisecond)}
}
