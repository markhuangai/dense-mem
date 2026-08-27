package serverapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

var (
	errSearchConvergenceQueryFailed = errors.New("search convergence query failed")
	errSearchReconciliationFailed   = errors.New("search reconciliation failed")
)

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
				logger.Warn("search_convergence_health_query_failed",
					observability.String("error", safeOperationalLogError(err, errSearchConvergenceQueryFailed).Error()),
					observability.String("error_code", "search_convergence_query_failed"),
				)
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
// interval. Successful passes repeat while they make progress toward
// convergence; each pass remains one provider batch and one fenced transaction.
func startSearchReconciliation(ctx context.Context, runner searchReconciliationRunner, logger observability.LogProvider) {
	if runner == nil {
		return
	}
	run := func() {
		for ctx.Err() == nil {
			result, err := runner.Run(ctx)
			if err != nil {
				if logger != nil {
					logger.Warn("search_reconciliation_failed",
						observability.String("error", safeOperationalLogError(err, errSearchReconciliationFailed).Error()),
						observability.String("error_code", result.ErrorCode),
					)
				}
				return
			}
			if result.Skipped {
				if logger != nil {
					logger.Debug("search_reconciliation_skipped")
				}
				return
			}
			if result.DriftedCount == 0 || result.UpdatedCount == 0 {
				return
			}
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

func safeOperationalLogError(err error, fallback error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", fallback, context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: %w", fallback, context.Canceled)
	default:
		return fallback
	}
}
