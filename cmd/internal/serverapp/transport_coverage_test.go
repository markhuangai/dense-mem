package serverapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type contextTransportLogger struct {
	observability.LogProvider
	contextSeen context.Context
}

type transportContextKey struct{}

func (l *contextTransportLogger) InfoContext(ctx context.Context, _ string, _ ...observability.LogAttr) {
	l.contextSeen = ctx
}

func (l *contextTransportLogger) ErrorContext(ctx context.Context, _ string, _ error, _ ...observability.LogAttr) {
	l.contextSeen = ctx
}

func (l *contextTransportLogger) WarnContext(ctx context.Context, _ string, _ ...observability.LogAttr) {
	l.contextSeen = ctx
}

func (l *contextTransportLogger) DebugContext(ctx context.Context, _ string, _ ...observability.LogAttr) {
	l.contextSeen = ctx
}

type legacyTransportLogger struct {
	observability.LogProvider
}

func TestTransportLoggerForwardsRequestContext(t *testing.T) {
	logger := &contextTransportLogger{LogProvider: observability.New(0)}
	ctx := context.WithValue(context.Background(), transportContextKey{}, "request")

	contract.LogInfoContext(ctx, logger, "http_request")
	contract.LogErrorContext(ctx, logger, "http_request", errors.New("error"))
	contract.LogWarnContext(ctx, logger, "http_request")
	contract.LogDebugContext(ctx, logger, "http_request")

	if logger.contextSeen != ctx {
		t.Fatalf("adapter context = %v, want %v", logger.contextSeen, ctx)
	}
}

func TestTransportLoggerHandlesLegacyDelegate(t *testing.T) {
	logger := &legacyTransportLogger{LogProvider: observability.New(0)}
	ctx := context.Background()

	contract.LogInfoContext(ctx, logger, "http_request")
	contract.LogErrorContext(ctx, logger, "http_request", errors.New("error"))
	contract.LogWarnContext(ctx, logger, "http_request")
	contract.LogDebugContext(ctx, logger, "http_request")
}

func TestTransportCompositionRejectsMissingBoundaryDependencies(t *testing.T) {
	if _, err := buildTransportComposition(transportCompositionInputs{}); err == nil || !errors.Is(err, errors.New("transport: backend is required")) && err.Error() != "transport: backend is required" {
		t.Fatalf("missing backend error = %v", err)
	}
	backend, err := buildInMemoryBackend(config.Config{SSEMaxConcurrentStreams: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildTransportComposition(transportCompositionInputs{backend: backend}); err == nil || !strings.Contains(err.Error(), "credential verifier") {
		t.Fatalf("missing credential boundary error = %v", err)
	}
}
