package assessment

import (
	"context"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

type Diagnostic struct {
	Stage               string
	TeamID              string
	OperationKey        string
	Input               *ontology.OrganizationInput
	SourceContext       *ontology.OrganizationContext
	SourceDependencies  []ontology.SourceDependency
	Request             *Request
	Response            *Response
	Result              *ontology.OrganizationResult
	Policy              *domain.OntologyMaintenanceConfig
	ProviderIdentity    string
	Model               string
	Limits              assessor.SemanticAssessmentLimits
	ContextWindowTokens int
	Attempt             int
	Validation          []assessor.SemanticValidationError
	Duration            time.Duration
	Error               error
	SnapshotError       error
}

type Observer func(context.Context, Diagnostic)

func (p *Provider) WithDiagnostics(observer Observer) *Provider {
	p.observer = observer
	return p
}

func (p *Provider) Observe(ctx context.Context, diagnostic Diagnostic) {
	if p == nil || p.observer == nil {
		return
	}
	diagnostic.ProviderIdentity, diagnostic.Model = p.Identity(), p.model
	diagnostic.Limits, diagnostic.ContextWindowTokens = p.limits, p.contextWindowTokens
	p.observer(ctx, diagnostic)
}
