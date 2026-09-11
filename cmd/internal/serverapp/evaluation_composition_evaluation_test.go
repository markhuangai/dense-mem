//go:build evaluation

package serverapp

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestEvaluationCompositionUsesDedicatedReader(t *testing.T) {
	semantic := repository.NewSemanticRepository(nil, nil)
	bindings, err := buildEvaluationRegistryBindings(semantic, nil)
	if err != nil {
		t.Fatalf("evaluation composition: %v", err)
	}
	if _, ok := bindings.Repository.(*repository.EvaluationReader); !ok {
		t.Fatalf("evaluation repository = %T; want *repository.EvaluationReader", bindings.Repository)
	}
	if bindings.Communities != semantic {
		t.Fatal("evaluation composition did not preserve the community reader")
	}
	reg, err := registry.BuildActive(registry.Dependencies{EvaluationBindings: bindings})
	if err != nil {
		t.Fatalf("evaluation registry: %v", err)
	}
	if got := len(reg.List()); got != 13 {
		t.Fatalf("evaluation catalog size = %d; want 13", got)
	}
}

func TestEvaluationCompositionRejectsMissingSemanticRepository(t *testing.T) {
	if _, err := buildEvaluationRegistryBindings(nil, nil); err == nil {
		t.Fatal("missing semantic repository did not fail evaluation composition")
	}
}
