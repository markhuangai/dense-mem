package serverapp

import (
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestTransportLoggerAdaptersPreserveAndDropDelegates(t *testing.T) {
	if transportLogger(nil) != nil {
		t.Fatal("nil transport logger was not preserved")
	}
	adapter := httpLoggerAdapter{}
	adapter.Info("ignored")
	adapter.Warn("ignored")
	adapter.Debug("ignored")
	adapter.Error("ignored", errors.New("ignored"))
	if got := adapter.With(contract.String("key", "value")); got == nil {
		t.Fatal("nil delegate With returned nil")
	}
	logger := observability.New(0)
	adapted := transportLogger(logger)
	if adapted == nil {
		t.Fatal("logger adapter was nil")
	}
	adapted.Info("info", contract.String("key", "value"))
	adapted.Warn("warn")
	adapted.Debug("debug")
	adapted.Error("error", errors.New("bounded"))
	if adapted.With(contract.String("key", "value")) == nil {
		t.Fatal("adapted With returned nil")
	}
	if got := observabilityAttrs([]contract.LogAttr{contract.String("key", "value")}); len(got) != 1 || got[0].Key != "key" {
		t.Fatalf("observability attrs = %#v", got)
	}
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
