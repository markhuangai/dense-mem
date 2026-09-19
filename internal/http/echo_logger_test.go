package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/labstack/gommon/log"
	"github.com/stretchr/testify/require"

	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
)

type nilBoundLogProvider struct{ captureLogProvider }

func (l *nilBoundLogProvider) With(...httpcontract.LogAttr) httpcontract.LogProvider { return nil }

type fatalCaptureLogProvider struct {
	captureLogProvider
	fatalMessage string
}

func (l *fatalCaptureLogProvider) With(...httpcontract.LogAttr) httpcontract.LogProvider { return l }
func (l *fatalCaptureLogProvider) Fatal(message string, _ ...httpcontract.LogAttr) {
	l.fatalMessage = message
}

func TestEchoRootLoggerDelegatesLegacySurface(t *testing.T) {
	delegate := &captureLogProvider{}
	adapted := NewEchoLogger(delegate)
	root, ok := adapted.(*echoRootLogger)
	require.True(t, ok)

	var output bytes.Buffer
	root.SetOutput(nil)
	root.SetOutput(&output)
	root.SetPrefix("echo")
	root.SetHeader("header")
	root.SetLevel(log.DEBUG)
	require.Same(t, &output, root.Output())
	require.Equal(t, "echo", root.Prefix())
	require.Equal(t, log.DEBUG, root.Level())

	root.Print("print")
	root.Printf("printf %s", "value")
	root.Printj(log.JSON{"printj": true})
	root.Debug("debug")
	root.Debugf("debugf %s", "value")
	root.Debugj(log.JSON{"debugj": true})
	root.Info("info")
	root.Infof("infof %s", "value")
	root.Infoj(log.JSON{"infoj": true})
	root.Warn("warn")
	root.Warnf("warnf %s", "value")
	root.Warnj(log.JSON{"warnj": true})
	root.Error("error")
	root.Errorf("errorf %s", "value")
	root.Errorj(log.JSON{"errorj": true})
	root.Fatal("fatal")
	root.Fatalf("fatalf %s", "value")
	root.Fatalj(log.JSON{"fatalj": true})

	require.Panics(t, func() { root.Panic("panic") })
	require.Panics(t, func() { root.Panicf("panicf %s", "value") })
	require.Panics(t, func() { root.Panicj(log.JSON{"panicj": true}) })
	require.Contains(t, delegate.msg, "echo:")
}

func TestEchoLoggerHandlesNilBindingsAndFatalCapabilitiy(t *testing.T) {
	nilBound := &nilBoundLogProvider{}
	adapted := NewEchoLogger(nilBound)
	root := adapted.(*echoRootLogger)
	root.Info("without prefix")
	require.Equal(t, "without prefix", nilBound.msg)

	fatal := &fatalCaptureLogProvider{}
	fatalRoot := NewEchoLogger(fatal).(*echoRootLogger)
	fatalRoot.Fatal("fatal message")
	require.Equal(t, "fatal message", fatal.fatalMessage)
}

func TestRootRecoverLogsPanicAndSupportsNilLogger(t *testing.T) {
	require.NotNil(t, RootRecover(nil))

	logger := &captureLogProvider{}
	e := echo.New()
	e.Use(RootRecover(logger))
	e.GET("/panic", func(echo.Context) error {
		panic("panic-secret")
	})

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Equal(t, "http_panic_recovered", logger.msg)
	require.NotContains(t, logger.msg, "panic-secret")
	attrs := make(map[string]any, len(logger.attrs))
	for _, attr := range logger.attrs {
		attrs[attr.Key] = attr.Value
	}
	require.Equal(t, "/panic", attrs["route"])
	require.Contains(t, attrs, "stack_bytes")
	require.NotNil(t, logger.contextSeen)
}

func TestEchoServerErrorLoggerBridgesAndHandlesNilWriter(t *testing.T) {
	require.Nil(t, EchoServerErrorLogger(nil))

	logger := &captureLogProvider{}
	serverLogger := EchoServerErrorLogger(logger)
	serverLogger.Print("server error")
	require.Equal(t, "http_server_error", logger.msg)

	n, err := (rootLogWriter{}).Write([]byte("discarded"))
	require.NoError(t, err)
	require.Equal(t, len("discarded"), n)

}
