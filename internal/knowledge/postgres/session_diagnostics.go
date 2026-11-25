package postgres

import "context"

func (r *Store) purgeExpiredSessionDiagnostics(ctx context.Context) (int, error) {
	return r.purgeExpiredPrivateDiagnostics(ctx, "session_submission_diagnostics", rememberDiagnosticPurgeBatchSize)
}

func (r *Store) drainExpiredSessionDiagnostics(ctx context.Context) (int, error) {
	total := 0
	for {
		deleted, err := r.purgeExpiredSessionDiagnostics(ctx)
		total += deleted
		if err != nil || deleted < rememberDiagnosticPurgeBatchSize {
			return total, err
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}
