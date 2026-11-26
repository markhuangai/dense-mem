package assessment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/service/generation"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
)

type AttemptAccounting interface {
	BeforeAttempt(context.Context, string, int, int, int) (context.Context, error)
	AfterAttempt(context.Context, string, topic.AssessmentAttempt, bool) error
}

func AccountGeneration[T any](task *generation.Task[T], accounting AttemptAccounting, id string, output int) {
	if accounting == nil {
		return
	}
	task.BeforeAttempt = func(ctx context.Context, n, input int) (context.Context, error) {
		return accounting.BeforeAttempt(ctx, id, n, input, output)
	}
	task.AfterTransport = func(ctx context.Context, attempt *generation.Attempt, err error) error {
		record := topic.AssessmentAttempt{
			Number: attempt.Number, ReportedInputTokens: attempt.Provider.PromptTokens,
			ReportedOutputTokens: attempt.Provider.CompletionTokens, ReportedTotalTokens: attempt.Provider.TotalTokens,
			ReportedUsageAvailable: attempt.Provider.UsageAvailable, Diagnostics: attempt.Provider.Diagnostics,
		}
		return errors.Join(err, reconcileAttempt(ctx, accounting, id, record, err == nil))
	}
}

func AccountDecisions(ctx context.Context, accounting AttemptAccounting, id string, maxInput int) context.Context {
	if accounting == nil {
		return ctx
	}
	return modelprovider.WithDecisionExchangeAccounting(ctx, decisionAccounting{accounting, id, maxInput})
}

func reconcileAttempt(ctx context.Context, accounting AttemptAccounting, id string, record topic.AssessmentAttempt, success bool) error {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return accounting.AfterAttempt(recordCtx, id, record, success)
}

type decisionAccounting struct {
	accounting AttemptAccounting
	id         string
	maxInput   int
}

func (d decisionAccounting) DecisionInputLimit() int { return d.maxInput }

func (d decisionAccounting) assessmentID(batch int) (string, error) {
	if batch <= 1 {
		return d.id, nil
	}
	namespace, err := uuid.Parse(d.id)
	if err != nil {
		return "", fmt.Errorf("invalid decision assessment ID: %w", err)
	}
	return uuid.NewSHA1(namespace, []byte(fmt.Sprintf("decision-batch:%d", batch))).String(), nil
}

func (d decisionAccounting) BeforeDecisionExchange(ctx context.Context, r modelprovider.DecisionReceipt) (context.Context, error) {
	id, err := d.assessmentID(r.Batch)
	if err != nil {
		return ctx, err
	}
	return d.accounting.BeforeAttempt(ctx, id, r.Attempt, r.EstimatedInputTokens, 1)
}

func (d decisionAccounting) AfterDecisionExchange(ctx context.Context, r modelprovider.DecisionReceipt, success bool) error {
	id, err := d.assessmentID(r.Batch)
	if err != nil {
		return err
	}
	record := topic.AssessmentAttempt{Number: r.Attempt, Diagnostics: r.Diagnostics, EstimatedInputTokens: r.EstimatedInputTokens, EstimatedOutputTokens: 1}
	if r.Usage != nil {
		record.ReportedUsageAvailable = true
		record.ReportedInputTokens = r.Usage.InputTokens
		record.ReportedOutputTokens = r.Usage.OutputTokens
		record.ReportedTotalTokens = r.Usage.InputTokens + r.Usage.OutputTokens
	}
	return reconcileAttempt(ctx, d.accounting, id, record, success)
}

func NewProviderWithAccounting(transport modelprovider.StructuredTransport, model string, limits assessor.SemanticAssessmentLimits, accounting AttemptAccounting, contextWindowTokens int) *Provider {
	provider := NewProvider(transport, model, limits, contextWindowTokens)
	provider.accounting = accounting
	return provider
}
