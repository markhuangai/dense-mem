package service

import (
	"context"
)

func rememberFailureRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), RememberFailurePersistenceBudget)
}
