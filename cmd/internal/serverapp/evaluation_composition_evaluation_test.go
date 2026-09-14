//go:build evaluation

package serverapp

import (
	"testing"

	communitypostgres "github.com/markhuangai/dense-mem/internal/community/postgres"
	evaluationpostgres "github.com/markhuangai/dense-mem/internal/evaluation/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"gorm.io/gorm"
)

func TestEvaluationCompositionUsesDedicatedReader(t *testing.T) {
	communities := communitypostgres.NewStore(nil, nil)
	bindings, err := buildEvaluationRegistryBindings(&gorm.DB{}, storagepostgres.NewRLS(), communities, nil)
	if err != nil {
		t.Fatalf("evaluation composition: %v", err)
	}
	if _, ok := bindings.Repository.(*evaluationpostgres.EvaluationReader); !ok {
		t.Fatalf("evaluation repository = %T; want *evaluationpostgres.EvaluationReader", bindings.Repository)
	}
	if bindings.Communities != communities {
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
	if _, err := buildEvaluationRegistryBindings(nil, nil, nil, nil); err == nil {
		t.Fatal("missing semantic repository did not fail evaluation composition")
	}
}
