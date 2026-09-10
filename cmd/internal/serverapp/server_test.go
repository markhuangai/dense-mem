package serverapp

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type searchConvergenceHealthStub struct {
	err error
}

func (s searchConvergenceHealthStub) CheckSearchConvergence(context.Context) error {
	return s.err
}

type searchConvergenceHealthLogger struct {
	warnings []string
	attrs    []observability.LogAttr
}

func (*searchConvergenceHealthLogger) Info(string, ...observability.LogAttr)         {}
func (*searchConvergenceHealthLogger) Error(string, error, ...observability.LogAttr) {}
func (l *searchConvergenceHealthLogger) Warn(message string, attrs ...observability.LogAttr) {
	l.warnings = append(l.warnings, message)
	l.attrs = append(l.attrs, attrs...)
}
func (*searchConvergenceHealthLogger) Debug(string, ...observability.LogAttr) {}
func (l *searchConvergenceHealthLogger) With(...observability.LogAttr) observability.LogProvider {
	return l
}

func TestSearchConvergenceHealthCheckBoundsRepositoryErrors(t *testing.T) {
	raw := errors.New("pq: secret relation does not exist")
	logger := &searchConvergenceHealthLogger{}
	check := searchConvergenceHealthCheck(searchConvergenceHealthStub{err: raw}, logger)

	err := check(context.Background())

	if !errors.Is(err, errSearchConvergenceQueryFailed) || errors.Is(err, raw) {
		t.Fatalf("health error = %v", err)
	}
	if len(logger.warnings) != 1 || logger.warnings[0] != "search_convergence_health_query_failed" {
		t.Fatalf("warnings = %#v", logger.warnings)
	}
	if len(logger.attrs) != 1 || logger.attrs[0].Key != "error_code" || logger.attrs[0].Value != "search_convergence_query_failed" {
		t.Fatalf("warning attrs = %#v", logger.attrs)
	}
}

func TestSearchConvergenceHealthCheckDoesNotLogExpectedDegradation(t *testing.T) {
	logger := &searchConvergenceHealthLogger{}
	check := searchConvergenceHealthCheck(searchConvergenceHealthStub{err: repository.ErrSearchConvergenceAttentionRequired}, logger)

	err := check(context.Background())

	if !errors.Is(err, repository.ErrSearchConvergenceAttentionRequired) {
		t.Fatalf("health error = %v", err)
	}
	if len(logger.warnings) != 0 {
		t.Fatalf("warnings = %#v", logger.warnings)
	}
}

func TestPrivateMemoryPrepareBootErrorBoundsRawFailure(t *testing.T) {
	raw := errors.New("pq: password secret relation does not exist")
	err := privateMemoryPrepareBootError(raw)

	if !errors.Is(err, errPrivateMemoryPrepareFailed) {
		t.Fatalf("prepare error = %v", err)
	}
	if errors.Is(err, raw) || err.Error() != "private-memory erasure preparation failed" {
		t.Fatalf("prepare error exposed raw failure: %v", err)
	}
	if err := privateMemoryPrepareBootError(nil); err != nil {
		t.Fatalf("nil prepare error = %v", err)
	}
}
