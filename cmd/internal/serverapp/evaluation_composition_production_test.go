//go:build !evaluation

package serverapp

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestProductionEvaluationCompositionIsEmpty(t *testing.T) {
	bindings, err := buildEvaluationRegistryBindings(nil, nil)
	if err != nil {
		t.Fatalf("production evaluation composition: %v", err)
	}
	if bindings.Repository != nil || bindings.Communities != nil || bindings.Audit != nil {
		t.Fatal("production composition wired offline evaluation dependencies")
	}
	reg, err := registry.BuildActive(registry.Dependencies{EvaluationBindings: bindings})
	if err != nil {
		t.Fatalf("production registry: %v", err)
	}
	if got := len(reg.List()); got != 10 {
		t.Fatalf("production catalog size = %d; want 10", got)
	}
}
