package topicassignment

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/service/generation"
	"github.com/markhuangai/dense-mem/internal/topicassignment/contract"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
)

type accountingKey struct{}

func WithAccounting(ctx context.Context, a assessment.AttemptAccounting) context.Context {
	if a == nil {
		return ctx
	}
	return context.WithValue(ctx, accountingKey{}, a)
}

func attachGenerationAccounting(ctx context.Context, task *generation.Task[contract.Outcome], id string, output int) {
	a, _ := ctx.Value(accountingKey{}).(assessment.AttemptAccounting)
	assessment.AccountGeneration(task, a, id, output)
}

func withDecisionAccounting(ctx context.Context, id string, output int) context.Context {
	a, _ := ctx.Value(accountingKey{}).(assessment.AttemptAccounting)
	return assessment.AccountDecisions(ctx, a, id, output)
}
