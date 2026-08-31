package serverapp

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

var errSearchConvergenceQueryFailed = errors.New("search convergence query failed")

type searchConvergenceHealthReader interface {
	CheckSearchConvergence(context.Context) error
}

func searchConvergenceHealthCheck(search searchConvergenceHealthReader, logger observability.LogProvider) func(context.Context) error {
	return func(ctx context.Context) error {
		err := search.CheckSearchConvergence(ctx)
		if err != nil {
			if errors.Is(err, repository.ErrSearchConvergenceAttentionRequired) {
				return err
			}
			if logger != nil {
				logger.Warn("search_convergence_health_query_failed", observability.String("error_code", "search_convergence_query_failed"))
			}
			return errSearchConvergenceQueryFailed
		}
		return nil
	}
}

type searchReconciliationRunner interface {
	Run(context.Context) (service.SearchReconciliationResult, error)
}

const searchReconciliationInterval = time.Hour

// startSearchReconciliation performs bounded document repair on a maintenance
// interval. It never creates a queue or changes the request-scoped Remember
// path; each pass is one provider batch followed by one fenced SQL transaction.
func startSearchReconciliation(ctx context.Context, runner searchReconciliationRunner, logger observability.LogProvider) {
	if runner == nil {
		return
	}
	run := func() {
		result, err := runner.Run(ctx)
		if err != nil {
			if logger != nil {
				logger.Warn("search_reconciliation_failed", observability.String("error_code", result.ErrorCode))
			}
			return
		}
		if result.Skipped && logger != nil {
			logger.Debug("search_reconciliation_skipped")
		}
	}
	run()
	ticker := time.NewTicker(searchReconciliationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
