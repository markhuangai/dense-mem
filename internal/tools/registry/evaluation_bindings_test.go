package registry

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestEvaluationFacetSuppliesAuditAppender(t *testing.T) {
	audit := &evaluationAuditStub{}
	deps := (Dependencies{
		EvaluationBindings: EvaluationBindings{Audit: audit},
	}).withCapabilityBindings()
	if deps.EvaluationAudit != audit {
		t.Fatal("evaluation capability facet did not supply its audit appender")
	}
}

func TestEvaluationBindingsPreserveFlatCoreAndFacetPrecedence(t *testing.T) {
	flatEval := repository.NewSemanticRepository(nil, nil)
	facetEval := repository.NewSemanticRepository(nil, nil)
	flatCommunity := repository.NewSemanticRepository(nil, nil)
	facetCommunity := repository.NewSemanticRepository(nil, nil)
	flatAudit := &evaluationAuditStub{}
	coreAudit := &evaluationAuditStub{}
	facetAudit := &evaluationAuditStub{}

	wired := (Dependencies{
		Core: CoreDependencies{
			EvaluationAudit: coreAudit,
		},
		Evaluation:      flatEval,
		Communities:     flatCommunity,
		EvaluationAudit: flatAudit,
		EvaluationBindings: EvaluationBindings{
			Repository:  facetEval,
			Communities: facetCommunity,
			Audit:       facetAudit,
		},
	}).withCapabilityBindings()
	if wired.Evaluation != flatEval || wired.Communities != flatCommunity || wired.EvaluationAudit != flatAudit {
		t.Fatal("flat evaluation dependencies did not take precedence")
	}

	wired = (Dependencies{
		Core: CoreDependencies{
			EvaluationAudit: coreAudit,
		},
		EvaluationBindings: EvaluationBindings{
			Repository:  facetEval,
			Communities: facetCommunity,
			Audit:       facetAudit,
		},
	}).withCapabilityBindings()
	if wired.Evaluation != facetEval || wired.Communities != facetCommunity || wired.EvaluationAudit != coreAudit {
		t.Fatal("core audit dependency did not take precedence")
	}

	wired = (Dependencies{EvaluationBindings: EvaluationBindings{
		Repository:  facetEval,
		Communities: facetCommunity,
		Audit:       facetAudit,
	}}).withCapabilityBindings()
	if wired.Evaluation != facetEval || wired.Communities != facetCommunity || wired.EvaluationAudit != facetAudit {
		t.Fatal("evaluation facet dependencies were not used as fallback")
	}
}
