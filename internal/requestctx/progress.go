package requestctx

import (
	"context"
	"sync"
)

// ProgressUpdate describes server-owned work; callers must never populate it with admitted content.
type ProgressUpdate struct {
	Stage     string
	Operation string
	Attempt   int
	Total     int
	Batch     int
	Batches   int
}

type progressObserverKey struct{}
type progressOperationKey struct{}

func WithProgressObserver(ctx context.Context, observer func(context.Context, ProgressUpdate)) context.Context {
	return context.WithValue(ctx, progressObserverKey{}, observer)
}

func WithSynchronizedProgress(ctx context.Context) context.Context {
	observer, ok := ctx.Value(progressObserverKey{}).(func(context.Context, ProgressUpdate))
	if !ok || observer == nil {
		return ctx
	}
	var mu sync.Mutex
	return WithProgressObserver(ctx, func(ctx context.Context, update ProgressUpdate) {
		mu.Lock()
		defer mu.Unlock()
		observer(ctx, update)
	})
}

func WithProgressOperation(ctx context.Context, operation string) context.Context {
	return context.WithValue(ctx, progressOperationKey{}, operation)
}

func ReportProgress(ctx context.Context, update ProgressUpdate) {
	if ctx.Err() != nil {
		return
	}
	if update.Operation == "" {
		update.Operation, _ = ctx.Value(progressOperationKey{}).(string)
	}
	if observer, ok := ctx.Value(progressObserverKey{}).(func(context.Context, ProgressUpdate)); ok && observer != nil {
		observer(ctx, update)
	}
}
