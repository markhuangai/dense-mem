package dream

import (
	"context"
	"log/slog"
	"time"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

// RunDiagnosticPurger keeps retained Dream projections bounded. It is a
// coordination worker; semantic Dream state is never removed by this loop.
func RunDiagnosticPurger(ctx context.Context, repo dreamcontract.DreamDiagnosticRepository, interval time.Duration, logger *slog.Logger) {
	if repo == nil {
		return
	}
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			const batchSize = 100
			deletedTotal := 0
			for {
				if err := ctx.Err(); err != nil {
					return
				}
				deleted, err := repo.PurgeExpiredDreamDiagnostics(ctx, batchSize)
				deletedTotal += deleted
				if err != nil {
					if ctx.Err() == nil && logger != nil {
						logger.Error("dream diagnostic purge failed", "error_code", "dream_diagnostic_purge_failed", "error", err)
					}
					break
				}
				if deleted < batchSize {
					break
				}
			}
			if deletedTotal > 0 && logger != nil {
				logger.Info("dream diagnostics purged", "count", deletedTotal)
			}
		}
	}
}
