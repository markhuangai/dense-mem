package assessment

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/service/generation"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
)

type AttemptAccounting interface {
	BeforeAttempt(context.Context, string, int, int, int) (context.Context, error)
	AfterAttempt(context.Context, string, ontology.AssessmentAttempt, bool) error
}

func AccountGeneration[T any](task *generation.Task[T], accounting AttemptAccounting, id string, output int) {
	if accounting == nil {
		return
	}
	task.BeforeAttempt = func(ctx context.Context, n, input int) (context.Context, error) {
		return accounting.BeforeAttempt(ctx, id, n, input, output)
	}
	task.AfterTransport = func(ctx context.Context, attempt *generation.Attempt, err error) error {
		record := ontology.AssessmentAttempt{
			Number: attempt.Number, ReportedInputTokens: attempt.Provider.PromptTokens,
			ReportedOutputTokens: attempt.Provider.CompletionTokens, ReportedTotalTokens: attempt.Provider.TotalTokens,
			ReportedUsageAvailable: attempt.Provider.UsageAvailable, Diagnostics: attempt.Provider.Diagnostics,
		}
		return errors.Join(err, reconcileAttempt(ctx, accounting, id, record, err == nil))
	}
}

func AccountDecisions(ctx context.Context, accounting AttemptAccounting, id string, output int) context.Context {
	if accounting == nil {
		return ctx
	}
	return modelprovider.WithDecisionExchangeAccounting(ctx, decisionAccounting{accounting, id, output})
}

func reconcileAttempt(ctx context.Context, accounting AttemptAccounting, id string, record ontology.AssessmentAttempt, success bool) error {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return accounting.AfterAttempt(recordCtx, id, record, success)
}

type decisionAccounting struct {
	accounting AttemptAccounting
	id         string
	output     int
}

func (d decisionAccounting) BeforeDecisionExchange(ctx context.Context, r modelprovider.DecisionReceipt) (context.Context, error) {
	return d.accounting.BeforeAttempt(ctx, d.id, r.Attempt, r.EstimatedInputTokens, d.output)
}

func (d decisionAccounting) AfterDecisionExchange(ctx context.Context, r modelprovider.DecisionReceipt, success bool) error {
	record := ontology.AssessmentAttempt{Number: r.Attempt, Diagnostics: r.Diagnostics, EstimatedInputTokens: r.EstimatedInputTokens, EstimatedOutputTokens: d.output}
	if r.Usage != nil {
		record.ReportedUsageAvailable = true
		record.ReportedInputTokens = r.Usage.InputTokens
		record.ReportedOutputTokens = r.Usage.OutputTokens
		record.ReportedTotalTokens = r.Usage.InputTokens + r.Usage.OutputTokens
	}
	return reconcileAttempt(ctx, d.accounting, d.id, record, success)
}

func NewProviderWithAccounting(transport modelprovider.StructuredTransport, model string, limits assessor.SemanticAssessmentLimits, accounting AttemptAccounting, contextWindowTokens int) *Provider {
	provider := NewProvider(transport, model, limits, contextWindowTokens)
	provider.accounting = accounting
	return provider
}
