package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/markhuangai/dense-mem/internal/observability"
)

type gormParamsFilter interface {
	ParamsFilter(context.Context, string, ...interface{}) (string, []interface{})
}

func TestGORMLoggerOmitsBoundParameters(t *testing.T) {
	logger := newGORMLogger(&bytes.Buffer{})
	filter, ok := logger.(gormParamsFilter)
	if !ok {
		t.Fatal("GORM logger does not expose parameter filtering")
	}

	const query = "SELECT status FROM teams WHERE id = $1"
	filteredQuery, params := filter.ParamsFilter(context.Background(), query, "team-private-value")
	if filteredQuery != query {
		t.Fatalf("filtered query = %q, want %q", filteredQuery, query)
	}
	if len(params) != 0 {
		t.Fatalf("filtered parameters = %#v, want none", params)
	}
}

func TestGORMLoggerSanitizesTraceErrors(t *testing.T) {
	var output bytes.Buffer
	logger := newGORMLogger(&output)
	logger.Trace(
		context.Background(),
		time.Now(),
		func() (string, int64) {
			return "INSERT INTO teams (id) VALUES ($1)", -1
		},
		&pgconn.PgError{Code: "23505", Message: "private database detail"},
	)

	logged := output.String()
	if strings.Contains(logged, "private database detail") {
		t.Fatalf("trace log contains raw database error: %q", logged)
	}
	if !strings.Contains(logged, "SQLSTATE 23505") {
		t.Fatalf("trace log omits bounded SQLSTATE: %q", logged)
	}
}

func TestGORMLoggerSanitizesDirectErrorArguments(t *testing.T) {
	var output bytes.Buffer
	logger := newGORMLogger(&output).LogMode(gormlogger.Error)
	logger.Error(context.Background(), "query failed: %v", errors.New("private database detail"))

	logged := output.String()
	if strings.Contains(logged, "private database detail") {
		t.Fatalf("error log contains raw database error: %q", logged)
	}
	if !strings.Contains(logged, "database operation failed") {
		t.Fatalf("error log omits bounded category: %q", logged)
	}
}

func TestSanitizeGORMErrorUsesBoundedCategories(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "record not found", err: fmt.Errorf("wrapped: %w", gorm.ErrRecordNotFound), want: "database record not found"},
		{name: "deadline", err: context.DeadlineExceeded, want: "database operation timed out"},
		{name: "cancel", err: context.Canceled, want: "database operation canceled"},
		{name: "invalid sqlstate", err: &pgconn.PgError{Code: "unsafe detail"}, want: "database operation failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := sanitizeGORMError(test.err)
			if test.want == "" {
				if got != nil {
					t.Fatalf("sanitizeGORMError() = %v, want nil", got)
				}
				return
			}
			if got == nil || got.Error() != test.want {
				t.Fatalf("sanitizeGORMError() = %v, want %q", got, test.want)
			}
		})
	}
}

func TestGORMLoggerRoutesStructuredQueriesThroughRoot(t *testing.T) {
	root := observability.New(slog.LevelDebug)
	sink := &recordingPostgresLogSink{}
	if err := root.AttachSink(sink); err != nil {
		t.Fatal(err)
	}
	logger := newGORMLoggerWithRoot(root, time.Millisecond)
	logger.Trace(context.Background(), time.Now().Add(-10*time.Millisecond), func() (string, int64) {
		return "SELECT $1", 1
	}, nil)
	if len(sink.records) != 1 {
		t.Fatalf("structured query records = %d, want 1", len(sink.records))
	}
	if sink.records[0].Message != "slow postgres query" {
		t.Fatalf("message = %q, want slow postgres query", sink.records[0].Message)
	}
}

type recordingPostgresLogSink struct {
	records []observability.LogRecord
}

func (s *recordingPostgresLogSink) WriteLog(_ context.Context, record observability.LogRecord) error {
	s.records = append(s.records, record)
	return nil
}
