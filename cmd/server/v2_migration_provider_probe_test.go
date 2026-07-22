package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestMigrationProviderProbeWrapsEmbeddingErrors(t *testing.T) {
	rawErr := errors.New("raw embedding provider failure")
	probe := migrationProviderProbe{
		embedder: probeEmbedderStub{err: rawErr},
		verifier: probeVerifierStub{response: verifier.Response{Verdict: "supported"}},
	}

	_, err := probe.Probe(context.Background())
	if !errors.Is(err, rawErr) {
		t.Fatalf("Probe err = %v, want wrapped raw error", err)
	}
	if !strings.Contains(err.Error(), "embedding request failed") {
		t.Fatalf("Probe err = %v, want stable operation context", err)
	}
}

func TestMigrationProviderProbeWrapsVerifierErrors(t *testing.T) {
	rawErr := errors.New("raw verifier provider failure")
	probe := migrationProviderProbe{
		embedder: probeEmbedderStub{vector: []float32{0.1, 0.2, 0.3}},
		verifier: probeVerifierStub{err: rawErr},
	}

	_, err := probe.Probe(context.Background())
	if !errors.Is(err, rawErr) {
		t.Fatalf("Probe err = %v, want wrapped raw error", err)
	}
	if !strings.Contains(err.Error(), "verifier request failed") {
		t.Fatalf("Probe err = %v, want stable operation context", err)
	}
}

type probeEmbedderStub struct {
	vector []float32
	err    error
}

func (s probeEmbedderStub) Embed(context.Context, string) ([]float32, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return s.vector, "embedding-test-model", nil
}

func (s probeEmbedderStub) EmbedBatch(context.Context, []string) ([][]float32, string, error) {
	return nil, "", nil
}

func (s probeEmbedderStub) ModelName() string {
	return "embedding-test-model"
}

func (s probeEmbedderStub) Dimensions() int {
	return len(s.vector)
}

func (s probeEmbedderStub) IsAvailable() bool {
	return true
}

type probeVerifierStub struct {
	response verifier.Response
	err      error
}

func (s probeVerifierStub) Verify(context.Context, verifier.Request) (verifier.Response, error) {
	if s.err != nil {
		return verifier.Response{}, s.err
	}
	return s.response, nil
}
