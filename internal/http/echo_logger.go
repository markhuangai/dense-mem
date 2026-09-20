package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	gommonlog "github.com/labstack/gommon/log"

	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/tools"
)

// NewEchoLogger adapts Echo's legacy logger interface to the injected root.
// Echo's logger has no request context, so request-aware middleware uses the
// transport logging helpers directly while framework messages remain bounded.
func NewEchoLogger(logger httpcontract.LogProvider) echo.Logger {
	if logger == nil {
		return nil
	}
	bound := logger.With(httpcontract.String("component", "echo"))
	if bound == nil {
		bound = logger
	}
	return &echoRootLogger{logger: bound, output: io.Discard}
}

func configureEchoLogger(e *echo.Echo, logger httpcontract.LogProvider) {
	if rootLogger := NewEchoLogger(logger); rootLogger != nil {
		e.Logger = rootLogger
		e.StdLogger = echoServerErrorLogger(logger)
	}
}

type echoRootLogger struct {
	logger httpcontract.LogProvider
	output io.Writer
	prefix string
	header string
	level  gommonlog.Lvl
}

func (l *echoRootLogger) Output() io.Writer { return l.output }
func (l *echoRootLogger) SetOutput(writer io.Writer) {
	if writer != nil {
		l.output = writer
	}
}
func (l *echoRootLogger) Prefix() string               { return l.prefix }
func (l *echoRootLogger) SetPrefix(prefix string)      { l.prefix = prefix }
func (l *echoRootLogger) Level() gommonlog.Lvl         { return l.level }
func (l *echoRootLogger) SetLevel(level gommonlog.Lvl) { l.level = level }
func (l *echoRootLogger) SetHeader(header string)      { l.header = header }
func (l *echoRootLogger) Print(args ...interface{})    { l.logger.Info(l.message(fmt.Sprint(args...))) }
func (l *echoRootLogger) Printf(format string, args ...interface{}) {
	l.logger.Info(l.message(fmt.Sprintf(format, args...)))
}
func (l *echoRootLogger) Printj(values gommonlog.JSON) {
	l.logger.Info(l.message(fmt.Sprint(values)))
}
func (l *echoRootLogger) Debug(args ...interface{}) {
	l.logger.Debug(l.message(fmt.Sprint(args...)))
}
func (l *echoRootLogger) Debugf(format string, args ...interface{}) {
	l.logger.Debug(l.message(fmt.Sprintf(format, args...)))
}
func (l *echoRootLogger) Debugj(values gommonlog.JSON) {
	l.logger.Debug(l.message(fmt.Sprint(values)))
}
func (l *echoRootLogger) Info(args ...interface{}) {
	l.logger.Info(l.message(fmt.Sprint(args...)))
}
func (l *echoRootLogger) Infof(format string, args ...interface{}) {
	l.logger.Info(l.message(fmt.Sprintf(format, args...)))
}
func (l *echoRootLogger) Infoj(values gommonlog.JSON) {
	l.logger.Info(l.message(fmt.Sprint(values)))
}
func (l *echoRootLogger) Warn(args ...interface{}) {
	l.logger.Warn(l.message(fmt.Sprint(args...)))
}
func (l *echoRootLogger) Warnf(format string, args ...interface{}) {
	l.logger.Warn(l.message(fmt.Sprintf(format, args...)))
}
func (l *echoRootLogger) Warnj(values gommonlog.JSON) {
	l.logger.Warn(l.message(fmt.Sprint(values)))
}
func (l *echoRootLogger) Error(args ...interface{}) {
	l.logger.Error(l.message(fmt.Sprint(args...)), nil)
}
func (l *echoRootLogger) Errorf(format string, args ...interface{}) {
	l.logger.Error(l.message(fmt.Sprintf(format, args...)), nil)
}
func (l *echoRootLogger) Errorj(values gommonlog.JSON) {
	l.logger.Error(l.message(fmt.Sprint(values)), nil)
}
func (l *echoRootLogger) Fatal(args ...interface{}) {
	l.logFatal(l.message(fmt.Sprint(args...)))
}
func (l *echoRootLogger) Fatalf(format string, args ...interface{}) {
	l.logFatal(l.message(fmt.Sprintf(format, args...)))
}
func (l *echoRootLogger) Fatalj(values gommonlog.JSON) {
	l.logFatal(l.message(fmt.Sprint(values)))
}
func (l *echoRootLogger) Panic(args ...interface{}) {
	message := l.message(fmt.Sprint(args...))
	l.logger.Error(message, nil)
	panic(message)
}
func (l *echoRootLogger) Panicf(format string, args ...interface{}) {
	message := l.message(fmt.Sprintf(format, args...))
	l.logger.Error(message, nil)
	panic(message)
}
func (l *echoRootLogger) Panicj(values gommonlog.JSON) {
	message := l.message(fmt.Sprint(values))
	l.logger.Error(message, nil)
	panic(message)
}

func (l *echoRootLogger) message(value string) string {
	value = strings.TrimSpace(value)
	if l.prefix == "" {
		return value
	}
	return l.prefix + ": " + value
}

func (l *echoRootLogger) logFatal(message string) {
	if fatal, ok := l.logger.(interface {
		Fatal(string, ...httpcontract.LogAttr)
	}); ok {
		fatal.Fatal(message)
		return
	}
	l.logger.Error(message, nil)
}

func rootRecover(logger httpcontract.LogProvider) echo.MiddlewareFunc {
	if logger == nil {
		return echomw.Recover()
	}
	config := echomw.DefaultRecoverConfig
	config.DisablePrintStack = true
	config.LogErrorFunc = func(c echo.Context, err error, stack []byte) error {
		attrs := []httpcontract.LogAttr{
			httpcontract.String("route", c.Path()),
			httpcontract.Int("stack_bytes", len(stack)),
		}
		httpcontract.LogErrorContext(TransportLogContext(c), logger, "http_panic_recovered", fmt.Errorf("%s", tools.SanitizeError(err)), attrs...)
		return err
	}
	return echomw.RecoverWithConfig(config)
}

// RootRecover exposes the transport recovery adapter to the private metrics
// listener without exporting its implementation details.
func RootRecover(logger httpcontract.LogProvider) echo.MiddlewareFunc {
	return rootRecover(logger)
}

func echoServerErrorLogger(logger httpcontract.LogProvider) *log.Logger {
	if logger == nil {
		return nil
	}
	return log.New(rootLogWriter{logger: logger}, "", 0)
}

// EchoServerErrorLogger returns a standard-library bridge backed by the root.
func EchoServerErrorLogger(logger httpcontract.LogProvider) *log.Logger {
	return echoServerErrorLogger(logger)
}

type rootLogWriter struct{ logger httpcontract.LogProvider }

func (w rootLogWriter) Write(value []byte) (int, error) {
	if w.logger != nil {
		httpcontract.LogErrorContext(context.Background(), w.logger, "http_server_error", errors.New(tools.SanitizeError(errors.New(string(value)))))
	}
	return len(value), nil
}

func transportStatus(status int) string {
	if status >= http.StatusInternalServerError {
		return "error"
	}
	if status >= http.StatusBadRequest {
		return "client_error"
	}
	return "success"
}

// TransportStatus classifies an HTTP completion for transport observations
// emitted by first-party handlers outside Echo middleware.
func TransportStatus(status int) string {
	return transportStatus(status)
}
