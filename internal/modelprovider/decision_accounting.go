package modelprovider

import "context"

// DecisionExchangeAccounting attaches a capability's durable admission and reconciliation to one actual HTTP exchange.
type DecisionExchangeAccounting interface {
	BeforeDecisionExchange(context.Context, DecisionReceipt) (context.Context, error)
	AfterDecisionExchange(context.Context, DecisionReceipt, bool) error
}

type decisionAccountingKey struct{}

func WithDecisionExchangeAccounting(ctx context.Context, a DecisionExchangeAccounting) context.Context {
	return context.WithValue(ctx, decisionAccountingKey{}, a)
}

func DecisionAccountingFromContext(ctx context.Context) DecisionExchangeAccounting {
	a, _ := ctx.Value(decisionAccountingKey{}).(DecisionExchangeAccounting)
	return a
}
