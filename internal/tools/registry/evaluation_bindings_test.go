package registry

import (
	"context"
	"testing"

	evaluationcontract "github.com/markhuangai/dense-mem/internal/evaluation/contract"
)

type evaluationBindingsRepository struct{}

func (*evaluationBindingsRepository) ListEvaluationRefs(context.Context, evaluationcontract.EvaluationListInput) (*evaluationcontract.EvaluationPage, error) {
	return nil, nil
}

func (*evaluationBindingsRepository) GetEvaluationItem(context.Context, evaluationcontract.EvaluationGetInput) (map[string]any, error) {
	return nil, nil
}

func TestEvaluationFacetSuppliesAuditAppender(t *testing.T) {
	audit := &evaluationAuditStub{}
	deps := Dependencies{
		EvaluationBindings: EvaluationBindings{Audit: audit},
	}
	if deps.EvaluationBindings.Audit != audit {
		t.Fatal("evaluation capability facet did not supply its audit appender")
	}
}

func TestEvaluationBindingsKeepNativeContracts(t *testing.T) {
	repository := &evaluationBindingsRepository{}
	audit := &evaluationAuditStub{}
	deps := Dependencies{EvaluationBindings: EvaluationBindings{
		Repository: repository,
		Audit:      audit,
	}}
	if deps.EvaluationBindings.Repository != repository || deps.EvaluationBindings.Audit != audit {
		t.Fatal("evaluation bindings did not preserve native contracts")
	}
}
