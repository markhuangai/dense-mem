//go:build evaluation

package serverapp

import (
	"testing"

	evaluationpostgres "github.com/markhuangai/dense-mem/internal/evaluation/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"gorm.io/gorm"
)

func TestEvidenceOnlyCatalogEvaluationCompositionUsesDedicatedReader(t *testing.T) {
	bindings, err := buildEvaluationRegistryBindings(&gorm.DB{}, storagepostgres.NewRLS(), nil)
	if err != nil {
		t.Fatalf("evaluation composition: %v", err)
	}
	if _, ok := bindings.Repository.(*evaluationpostgres.EvaluationReader); !ok {
		t.Fatalf("evaluation repository = %T; want *evaluationpostgres.EvaluationReader", bindings.Repository)
	}

	reg, err := registry.BuildActive(registry.Dependencies{EvaluationBindings: bindings})
	if err != nil {
		t.Fatalf("evaluation registry: %v", err)
	}
	if got := len(reg.List()); got != len(registry.ContractToolNames())+2 {
		t.Fatalf("evaluation catalog size = %d; want current memory tools and two evaluation tools", got)
	}
}

func TestEvaluationCompositionRejectsMissingSemanticRepository(t *testing.T) {
	if _, err := buildEvaluationRegistryBindings(nil, nil, nil); err == nil {
		t.Fatal("missing semantic repository did not fail evaluation composition")
	}
}
