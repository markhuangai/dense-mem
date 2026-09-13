package serverapp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	listenerShutdownTimeout = 10 * time.Second
	writerShutdownTimeout   = 10 * time.Second
	workerJoinTimeout       = 20 * time.Second
)

var ErrRuntimeShutdownTimeout = errors.New("runtime shutdown timed out")

type managedRuntimeWorker struct {
	name            string
	done            <-chan struct{}
	shutdown        func(context.Context) error
	shutdownTimeout time.Duration
}

// runtimeLifecycle is the process root's single owner for cancellation and
// worker joining. Capability packages provide their loops and optional drain
// hooks; they do not know the process shutdown order.
type runtimeLifecycle struct {
	cancel  context.CancelFunc
	context context.Context
	workers []managedRuntimeWorker
}

func newRuntimeLifecycle(parent context.Context) *runtimeLifecycle {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &runtimeLifecycle{cancel: cancel, context: ctx}
}

func (l *runtimeLifecycle) Context() context.Context {
	if l == nil || l.context == nil {
		return context.Background()
	}
	return l.context
}

func (l *runtimeLifecycle) add(worker managedRuntimeWorker) {
	if l == nil {
		return
	}
	l.workers = append(l.workers, worker)
}

func (l *runtimeLifecycle) start(name string, run func(context.Context)) {
	if l == nil || run == nil {
		return
	}
	if err := l.Context().Err(); err != nil {
		return
	}
	done := make(chan struct{})
	l.add(managedRuntimeWorker{name: name, done: done})
	go func() {
		defer close(done)
		run(l.Context())
	}()
}

func (l *runtimeLifecycle) shutdown(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if l.cancel != nil {
		l.cancel()
	}
	var joined error
	for index := len(l.workers) - 1; index >= 0; index-- {
		worker := l.workers[index]
		if worker.shutdown != nil {
			shutdownCtx := ctx
			cancel := func() {}
			if worker.shutdownTimeout > 0 {
				shutdownCtx, cancel = context.WithTimeout(ctx, worker.shutdownTimeout)
			}
			err := worker.shutdown(shutdownCtx)
			cancel()
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					joined = errors.Join(joined, fmt.Errorf("%w: shutdown %s: %w", ErrRuntimeShutdownTimeout, worker.name, err))
				} else {
					joined = errors.Join(joined, fmt.Errorf("shutdown %s: %w", worker.name, err))
				}
			}
		}
		if worker.done == nil {
			continue
		}
		select {
		case <-worker.done:
		case <-ctx.Done():
			joined = errors.Join(joined, fmt.Errorf("%w: wait for %s: %w", ErrRuntimeShutdownTimeout, worker.name, ctx.Err()))
		}
	}
	return joined
}

func shutdownEchoServer(ctx context.Context, server *echo.Echo) error {
	if server == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := server.Shutdown(ctx); err != nil {
		return errors.Join(err, forceCloseEchoServer(server))
	}
	return nil
}

type listenerShutdown struct {
	name     string
	shutdown func(context.Context) error
}

func shutdownListeners(ctx context.Context, listeners ...listenerShutdown) error {
	if ctx == nil {
		ctx = context.Background()
	}
	results := make(chan struct {
		index int
		err   error
	}, len(listeners))
	active := 0
	for index, listener := range listeners {
		if listener.shutdown == nil {
			continue
		}
		active++
		go func(index int, listener listenerShutdown) {
			results <- struct {
				index int
				err   error
			}{index: index, err: listener.shutdown(ctx)}
		}(index, listener)
	}
	errorsByIndex := make([]error, len(listeners))
	for completed := 0; completed < active; completed++ {
		result := <-results
		errorsByIndex[result.index] = result.err
	}
	var joined error
	for index, err := range errorsByIndex {
		if err != nil {
			joined = errors.Join(joined, fmt.Errorf("%s shutdown: %w", listeners[index].name, err))
		}
	}
	return joined
}

func forceCloseEchoServer(server *echo.Echo) error {
	if server == nil {
		return nil
	}
	if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("force close echo server: %w", err)
	}
	return nil
}

// bindEchoServer reserves the listener before any worker starts. Echo's Start
// still owns HTTP server configuration and serving; the pre-bound listener
// makes address failures synchronous and allows startup to unwind safely.
func bindEchoServer(server *echo.Echo, address string) (net.Listener, error) {
	if server == nil {
		return nil, errors.New("runtime listener: server is required")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	server.Listener = listener
	return listener, nil
}

func serveEchoServer(server *echo.Echo, address string) <-chan error {
	errorsCh := make(chan error, 1)
	go func() {
		errorsCh <- server.Start(address)
	}()
	return errorsCh
}

func isExpectedServerClose(err error) bool {
	return err == nil || errors.Is(err, http.ErrServerClosed)
}
