package serverapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestRuntimeLifecycleCancelsAndJoinsWorkersInReverseOrder(t *testing.T) {
	lifecycle := newRuntimeLifecycle(context.Background())
	started := make(chan struct{})
	joined := make(chan struct{})
	lifecycle.start("test worker", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(joined)
	})
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, lifecycle.shutdown(ctx))
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("worker was not joined before shutdown returned")
	}
}

func TestRuntimeLifecycleReportsShutdownTimeout(t *testing.T) {
	lifecycle := newRuntimeLifecycle(context.Background())
	stuck := make(chan struct{})
	lifecycle.start("stuck worker", func(context.Context) { <-stuck })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := lifecycle.shutdown(ctx)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
	require.True(t, errors.Is(err, ErrRuntimeShutdownTimeout))
	close(stuck)
}

func TestRuntimeLifecycleStartsAndJoinsConfiguredWorkerCount(t *testing.T) {
	lifecycle := newRuntimeLifecycle(context.Background())
	var started atomic.Int32
	var stopped atomic.Int32
	for index := 0; index < 3; index++ {
		lifecycle.start(fmt.Sprintf("worker-%d", index), func(ctx context.Context) {
			started.Add(1)
			<-ctx.Done()
			stopped.Add(1)
		})
	}
	require.Eventually(t, func() bool { return started.Load() == 3 }, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, lifecycle.shutdown(ctx))
	require.Equal(t, int32(3), stopped.Load())
}

func TestBindEchoServerReservesAddressBeforeServing(t *testing.T) {
	first := echo.New()
	listener, err := bindEchoServer(first, "127.0.0.1:0")
	require.NoError(t, err)
	require.NotNil(t, listener)
	defer listener.Close()

	second := echo.New()
	_, err = bindEchoServer(second, listener.Addr().String())
	require.Error(t, err)
	_ = first.Shutdown(context.Background())
}

func TestBindEchoServerUsesConcreteTCPListener(t *testing.T) {
	server := echo.New()
	listener, err := bindEchoServer(server, "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	require.NotEmpty(t, listener.Addr().String())
}

func TestShutdownEchoServerDrainsActiveRequest(t *testing.T) {
	server := echo.New()
	listener, err := bindEchoServer(server, "127.0.0.1:0")
	require.NoError(t, err)
	entered := make(chan struct{})
	release := make(chan struct{})
	server.GET("/", func(c echo.Context) error {
		close(entered)
		<-release
		return c.NoContent(http.StatusNoContent)
	})
	serveErrs := serveEchoServer(server, listener.Addr().String())
	requestDone := make(chan error, 1)
	go func() {
		resp, requestErr := http.Get("http://" + listener.Addr().String() + "/")
		if resp != nil {
			_ = resp.Body.Close()
		}
		requestDone <- requestErr
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not enter handler")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- shutdownEchoServer(shutdownCtx, server) }()
	select {
	case <-shutdownDone:
		t.Fatal("server shutdown completed before active request drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-shutdownDone)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("active request did not finish")
	}
	select {
	case <-serveErrs:
	case <-time.After(time.Second):
		t.Fatal("server serve loop did not finish")
	}
}

func TestShutdownEchoServerForcesCloseAfterGracefulTimeout(t *testing.T) {
	server := echo.New()
	listener, err := bindEchoServer(server, "127.0.0.1:0")
	require.NoError(t, err)
	entered := make(chan struct{})
	release := make(chan struct{})
	server.GET("/", func(c echo.Context) error {
		close(entered)
		<-release
		return c.NoContent(http.StatusNoContent)
	})
	serveErrs := serveEchoServer(server, listener.Addr().String())
	requestDone := make(chan struct{})
	go func() {
		resp, _ := http.Get("http://" + listener.Addr().String() + "/")
		if resp != nil {
			_ = resp.Body.Close()
		}
		close(requestDone)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not enter handler")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = shutdownEchoServer(shutdownCtx, server)
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(release)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("forced-close request did not finish")
	}
	select {
	case <-serveErrs:
	case <-time.After(time.Second):
		t.Fatal("forced-close serve loop did not finish")
	}
}

func TestRuntimeLifecycleDrainsWorkerBeforeAdapterClose(t *testing.T) {
	lifecycle := newRuntimeLifecycle(context.Background())
	adapterClosed := false
	flushed := false
	lifecycle.start("buffered writer", func(ctx context.Context) { <-ctx.Done() })
	lifecycle.add(managedRuntimeWorker{name: "writer flush", shutdown: func(context.Context) error {
		if adapterClosed {
			t.Fatal("adapter closed before buffered writer drained")
		}
		flushed = true
		return nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, lifecycle.shutdown(ctx))
	adapterClosed = true
	require.True(t, flushed)
}
