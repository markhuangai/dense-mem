package processor

import (
	"context"

	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func rememberFailureRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), rememberapp.RememberFailurePersistenceBudget)
}
