package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/service/migrationsupervisor"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type migrationProviderProbe struct {
	embedder embedding.EmbeddingProviderInterface
	verifier verifier.Verifier
	timeout  time.Duration
}

func (p migrationProviderProbe) Probe(ctx context.Context) (*migrationsupervisor.GateEvidence, error) {
	if p.embedder == nil || p.verifier == nil {
		return nil, errors.New("migration provider probe: embedding and verifier providers are required")
	}
	timeout := p.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	vector, embeddingModel, err := p.embedder.Embed(probeCtx, "dense-mem v2 migration readiness probe")
	if err != nil {
		return nil, err
	}
	if len(vector) == 0 {
		return nil, errors.New("migration provider probe: embedding provider returned an empty vector")
	}
	response, err := p.verifier.Verify(probeCtx, verifier.Request{
		ProfileID: "00000000-0000-0000-0000-000000000000",
		Predicate: "Dense-Mem migration provider readiness probe completed.",
		Context:   "This bounded request validates provider transport and structured response parsing before empty-corpus cutover.",
	})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(response.Verdict) == "" {
		return nil, errors.New("migration provider probe: verifier returned an empty verdict")
	}
	return &migrationsupervisor.GateEvidence{
		Ref:     "dense-mem://migration/runtime/provider-probe",
		Message: "embedding and verifier providers completed a bounded empty-corpus readiness probe",
		Details: map[string]any{
			"embedding_model":      embeddingModel,
			"embedding_dimensions": len(vector),
			"verifier_verdict":     strings.TrimSpace(response.Verdict),
		},
	}, nil
}
