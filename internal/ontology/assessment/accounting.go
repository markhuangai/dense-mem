package assessment

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

type AttemptAccounting interface {
	BeforeAttempt(context.Context, string, int, int, int) (context.Context, error)
	AfterAttempt(context.Context, string, ontology.AssessmentAttempt, bool) error
}

func NewProviderWithAccounting(transport modelprovider.StructuredTransport, model string, limits assessor.SemanticAssessmentLimits, accounting AttemptAccounting) *Provider {
	provider := NewProvider(transport, model, limits)
	provider.accounting = accounting
	return provider
}
