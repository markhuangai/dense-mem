package modelprovider

import "context"

type queueDiagnosticsKey struct{}

func WithQueueDiagnostics(ctx context.Context, diagnostics *CallDiagnostics) context.Context {
	return context.WithValue(ctx, queueDiagnosticsKey{}, diagnostics)
}

func QueueDiagnosticsFromContext(ctx context.Context) *CallDiagnostics {
	diagnostics, _ := ctx.Value(queueDiagnosticsKey{}).(*CallDiagnostics)
	return diagnostics
}
