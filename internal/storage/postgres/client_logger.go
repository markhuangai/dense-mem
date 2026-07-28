package postgres

import (
	"context"
	"errors"
	"io"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type parameterFilteringGORMLogger interface {
	ParamsFilter(context.Context, string, ...interface{}) (string, []interface{})
}

type sanitizingGORMLogger struct {
	delegate gormlogger.Interface
}

func newGORMLogger(output io.Writer) gormlogger.Interface {
	delegate := gormlogger.New(
		log.New(output, "\r\n", log.LstdFlags),
		gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: false,
			ParameterizedQueries:      true,
			Colorful:                  false,
		},
	)
	return &sanitizingGORMLogger{delegate: delegate}
}

func (l *sanitizingGORMLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return &sanitizingGORMLogger{delegate: l.delegate.LogMode(level)}
}

func (l *sanitizingGORMLogger) Info(ctx context.Context, message string, args ...interface{}) {
	l.delegate.Info(ctx, message, sanitizeGORMLogArgs(args)...)
}

func (l *sanitizingGORMLogger) Warn(ctx context.Context, message string, args ...interface{}) {
	l.delegate.Warn(ctx, message, sanitizeGORMLogArgs(args)...)
}

func (l *sanitizingGORMLogger) Error(ctx context.Context, message string, args ...interface{}) {
	l.delegate.Error(ctx, message, sanitizeGORMLogArgs(args)...)
}

func (l *sanitizingGORMLogger) Trace(
	ctx context.Context,
	begin time.Time,
	sql func() (string, int64),
	err error,
) {
	l.delegate.Trace(ctx, begin, sql, sanitizeGORMError(err))
}

func (l *sanitizingGORMLogger) ParamsFilter(
	ctx context.Context,
	query string,
	params ...interface{},
) (string, []interface{}) {
	if filter, ok := l.delegate.(parameterFilteringGORMLogger); ok {
		return filter.ParamsFilter(ctx, query, params...)
	}
	return query, nil
}

func sanitizeGORMLogArgs(args []interface{}) []interface{} {
	sanitized := make([]interface{}, len(args))
	for index, arg := range args {
		if err, ok := arg.(error); ok {
			sanitized[index] = sanitizeGORMError(err)
			continue
		}
		sanitized[index] = arg
	}
	return sanitized
}

func sanitizeGORMError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return errors.New("database record not found")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("database operation timed out")
	case errors.Is(err, context.Canceled):
		return errors.New("database operation canceled")
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && validSQLState(postgresError.Code) {
		return errors.New("database operation failed (SQLSTATE " + postgresError.Code + ")")
	}
	return errors.New("database operation failed")
}

func validSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, character := range code {
		if (character < '0' || character > '9') && (character < 'A' || character > 'Z') {
			return false
		}
	}
	return true
}
