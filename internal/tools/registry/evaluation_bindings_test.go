package registry

import (
	"context"
	"testing"

	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

type evaluationBindingsRepository struct{}

// evaluationCommunityRepositoryStub only needs the native repository method
// set because these tests exercise dependency precedence, not repository I/O.
type evaluationCommunityRepositoryStub struct {
	communitycontract.CommunityRepository
}

func (*evaluationBindingsRepository) ListEvaluationRefs(context.Context, dreamcontract.EvaluationListInput) (*dreamcontract.EvaluationPage, error) {
	return nil, nil
}

func (*evaluationBindingsRepository) GetEvaluationItem(context.Context, dreamcontract.EvaluationGetInput) (map[string]any, error) {
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
	communities := &evaluationCommunityRepositoryStub{}
	audit := &evaluationAuditStub{}
	deps := Dependencies{EvaluationBindings: EvaluationBindings{
		Repository:  repository,
		Communities: communities,
		Audit:       audit,
	}}
	if deps.EvaluationBindings.Repository != repository || deps.EvaluationBindings.Communities != communities || deps.EvaluationBindings.Audit != audit {
		t.Fatal("evaluation bindings did not preserve native contracts")
	}
}
