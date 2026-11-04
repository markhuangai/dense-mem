package operations

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const ProviderTestSampleVersion = "1"

var (
	ErrProviderTestTargetChanged = errors.New("provider test target changed; refresh the provider list")
	ErrProviderTestUnavailable   = errors.New("provider test configuration unavailable")
)

type ProviderTestTarget struct {
	ID                      string   `json:"target_id"`
	Endpoint                string   `json:"endpoint"`
	Model                   string   `json:"model"`
	Roles                   []string `json:"roles"`
	Operation               string   `json:"operation"`
	CredentialConfiguration string   `json:"credential_configuration"`
	ConfigurationRevision   string   `json:"configuration_revision"`
	Available               bool     `json:"available"`
	UnavailableReason       string   `json:"unavailable_reason,omitempty"`
	TimeoutMS               int64    `json:"timeout_ms"`
}

type ProviderTestBinding struct {
	Target  ProviderTestTarget
	Execute func(context.Context) (*modelprovider.CallDiagnostics, error)
}

type ProviderTestResult struct {
	TestID                string                                    `json:"test_id"`
	CorrelationID         string                                    `json:"correlation_id"`
	ServerInstance        string                                    `json:"server_instance"`
	StartedAt             time.Time                                 `json:"started_at"`
	DurationMS            int64                                     `json:"duration_ms"`
	Target                ProviderTestTarget                        `json:"target"`
	SampleVersion         string                                    `json:"sample_version"`
	Outcome               string                                    `json:"outcome"`
	Reason                string                                    `json:"reason"`
	FailurePhase          string                                    `json:"failure_phase,omitempty"`
	Guidance              string                                    `json:"guidance"`
	Error                 string                                    `json:"error,omitempty"`
	HTTPStatus            *int                                      `json:"http_status"`
	NetworkState          string                                    `json:"network_state"`
	Diagnostics           *modelprovider.CallDiagnostics            `json:"diagnostics,omitempty"`
	Exchanges             []observability.HTTPDiagnosticObservation `json:"exchanges"`
	CaptureState          string                                    `json:"capture_state"`
	DetailedLogsAvailable bool                                      `json:"detailed_logs_available"`
}

type ProviderTests interface {
	List(context.Context) ([]ProviderTestTarget, error)
	Test(context.Context, string, string) (ProviderTestResult, error)
}

type ProviderTestService struct {
	catalog   func(context.Context) ([]ProviderTestBinding, error)
	logger    *slog.Logger
	protector observability.DiagnosticProtector
	instance  string
}

func NewProviderTestService(catalog func(context.Context) ([]ProviderTestBinding, error), logger *slog.Logger, protector observability.DiagnosticProtector, instance string) *ProviderTestService {
	return &ProviderTestService{catalog: catalog, logger: logger, protector: protector, instance: instance}
}

func (s *ProviderTestService) bindings(ctx context.Context) ([]ProviderTestBinding, error) {
	bindings, err := s.catalog(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.ErrorContext(ctx, "provider_test.configuration_failed", "error", err.Error())
		}
		return nil, ErrProviderTestUnavailable
	}
	groups := make([]ProviderTestBinding, 0, len(bindings))
	for _, binding := range bindings {
		index := slices.IndexFunc(groups, func(other ProviderTestBinding) bool {
			a, b := other.Target, binding.Target
			return a.Endpoint == b.Endpoint && a.Model == b.Model && a.Operation == b.Operation && a.CredentialConfiguration == b.CredentialConfiguration
		})
		if index < 0 {
			binding.Target.Roles = slices.Clone(binding.Target.Roles)
			groups = append(groups, binding)
		} else {
			groups[index].Target.Roles = append(groups[index].Target.Roles, binding.Target.Roles...)
			groups[index].Target.ConfigurationRevision += ":" + binding.Target.ConfigurationRevision
		}
	}
	for index := range groups {
		target := &groups[index].Target
		sort.Strings(target.Roles)
		target.Roles = slices.Compact(target.Roles)
		encoded, err := json.Marshal(target)
		if err != nil {
			return nil, ErrProviderTestUnavailable
		}
		target.ID = fmt.Sprintf("%x", sha256.Sum256(encoded))
	}
	slices.SortFunc(groups, func(a, b ProviderTestBinding) int {
		if a.Target.Endpoint != b.Target.Endpoint {
			return strings.Compare(a.Target.Endpoint, b.Target.Endpoint)
		}
		return strings.Compare(a.Target.Model, b.Target.Model)
	})
	return groups, nil
}

func (s *ProviderTestService) safeText(ctx context.Context, value string) string {
	protected, reason := observability.ProtectDiagnosticText(ctx, s.protector, []byte(value), observability.MaxOperationMetadataBytes)
	if reason != observability.CredentialProtectionAvailable {
		return "Diagnostic text unavailable"
	}
	return string(protected)
}

func (s *ProviderTestService) safeTarget(ctx context.Context, target ProviderTestTarget) ProviderTestTarget {
	target.Endpoint = s.safeText(ctx, target.Endpoint)
	target.Model = s.safeText(ctx, target.Model)
	return target
}

func (s *ProviderTestService) List(ctx context.Context) ([]ProviderTestTarget, error) {
	bindings, err := s.bindings(ctx)
	if err != nil {
		return nil, err
	}
	targets := make([]ProviderTestTarget, 0, len(bindings))
	for _, binding := range bindings {
		targets = append(targets, s.safeTarget(ctx, binding.Target))
	}
	return targets, nil
}

func (s *ProviderTestService) Test(ctx context.Context, targetID, actor string) (ProviderTestResult, error) {
	bindings, err := s.bindings(ctx)
	if err != nil {
		return ProviderTestResult{}, err
	}
	index := slices.IndexFunc(bindings, func(binding ProviderTestBinding) bool { return binding.Target.ID == targetID })
	if index < 0 {
		return ProviderTestResult{}, ErrProviderTestTargetChanged
	}
	binding := bindings[index]
	if !binding.Target.Available || binding.Execute == nil || binding.Target.TimeoutMS <= 0 {
		return ProviderTestResult{}, ErrProviderTestUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(binding.Target.TimeoutMS)*time.Millisecond)
	defer cancel()
	started := time.Now()
	result := ProviderTestResult{TestID: uuid.NewString(), CorrelationID: correlation.FromContext(ctx), ServerInstance: s.safeText(ctx, s.instance), StartedAt: started.UTC(), Target: s.safeTarget(ctx, binding.Target), SampleVersion: ProviderTestSampleVersion, Exchanges: []observability.HTTPDiagnosticObservation{}, CaptureState: "captured"}
	ctx = observability.WithExchangeScope(ctx, observability.ExchangeScope{RunID: result.TestID})
	ctx = observability.WithAIOperation(ctx, observability.AIOperationProviderTest, 1)
	var mu sync.Mutex
	ctx = observability.WithHTTPDiagnosticObserver(ctx, func(exchange observability.HTTPDiagnosticObservation) {
		mu.Lock()
		defer mu.Unlock()
		result.Exchanges = append(result.Exchanges, exchange)
	}, s.logger)
	attrs := []any{"test_id", result.TestID, "correlation_id", result.CorrelationID, "control_actor", actor, "server_instance", result.ServerInstance, "target", observability.DecisionLogValue(result.Target), "sample_version", result.SampleVersion}
	if s.logger != nil {
		result.DetailedLogsAvailable = s.logger.Enabled(ctx, slog.LevelDebug)
		s.logger.InfoContext(ctx, "provider_test.started", attrs...)
	}
	diagnostics, runErr := binding.Execute(ctx)
	mu.Lock()
	result.DurationMS, result.Diagnostics = time.Since(started).Milliseconds(), diagnostics
	classifyProviderTest(&result, ctx.Err(), runErr)
	if runErr != nil {
		result.Error = s.safeText(ctx, runErr.Error())
	}
	mu.Unlock()
	if s.logger != nil {
		for _, exchange := range result.Exchanges {
			s.logger.DebugContext(ctx, "provider_test.exchange", append(append([]any{}, attrs...), "exchange", observability.DecisionLogValue(exchange))...)
			if exchange.CaptureReason != "" || exchange.RequestCaptureState == "unavailable" || exchange.ResponseCaptureState == "unavailable" || exchange.ResponseCaptureState == "interrupted" {
				result.CaptureState = "incomplete"
				s.logger.WarnContext(ctx, "provider_test.capture_incomplete", append(append([]any{}, attrs...), "exchange", observability.DecisionLogValue(exchange))...)
			}
		}
		level := slog.LevelInfo
		if result.Outcome == "failed" {
			level = slog.LevelError
		}
		s.logger.Log(ctx, level, "provider_test.completed", append(attrs, "outcome", result.Outcome, "reason", result.Reason, "failure_phase", result.FailurePhase, "guidance", result.Guidance, "error", result.Error, "duration_ms", result.DurationMS, "http_status", result.HTTPStatus, "network_state", result.NetworkState, "attempts", len(result.Exchanges), "capture_state", result.CaptureState, "detailed_logs_available", result.DetailedLogsAvailable, "diagnostics", observability.DecisionLogValue(result.Diagnostics))...)
	}
	return result, nil
}
