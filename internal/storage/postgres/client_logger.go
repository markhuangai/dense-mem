package postgres

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/markhuangai/dense-mem/internal/observability"
)

type parameterFilteringGORMLogger interface {
	ParamsFilter(context.Context, string, ...interface{}) (string, []interface{})
}

type sanitizingGORMLogger struct {
	delegate       gormlogger.Interface
	operatorLogger observability.LogProvider
	slowThreshold  time.Duration
}

func newGORMLogger(output io.Writer) gormlogger.Interface {
	return newSanitizingGORMLogger(output, nil, 200*time.Millisecond, gormlogger.Warn)
}

func newGORMLoggerWithRoot(logger observability.LogProvider, threshold time.Duration) gormlogger.Interface {
	if threshold <= 0 {
		threshold = 200 * time.Millisecond
	}
	return newSanitizingGORMLogger(io.Discard, logger, threshold, gormlogger.Silent)
}

func newSanitizingGORMLogger(output io.Writer, logger observability.LogProvider, threshold time.Duration, level gormlogger.LogLevel) gormlogger.Interface {
	delegate := gormlogger.New(
		log.New(output, "\r\n", log.LstdFlags),
		gormlogger.Config{
			SlowThreshold:             threshold,
			LogLevel:                  level,
			IgnoreRecordNotFoundError: false,
			ParameterizedQueries:      true,
			Colorful:                  false,
		},
	)
	return &sanitizingGORMLogger{delegate: delegate, operatorLogger: logger, slowThreshold: threshold}
}

func (l *sanitizingGORMLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return &sanitizingGORMLogger{delegate: l.delegate.LogMode(level), operatorLogger: l.operatorLogger, slowThreshold: l.slowThreshold}
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
	query, rows := sql()
	duration := time.Since(begin)
	if l.operatorLogger != nil && !observability.SinkSuppressed(ctx) {
		attrs := []observability.LogAttr{
			observability.String("sql", query),
			observability.Int("rows", int(rows)),
			observability.Int("duration_ms", int(duration/time.Millisecond)),
		}
		if err != nil {
			logWithContext(l.operatorLogger, ctx, slogLevelError, "postgres query", sanitizeGORMError(err), attrs...)
		} else if duration >= l.slowThreshold {
			logWithContext(l.operatorLogger, ctx, slogLevelWarn, "slow postgres query", nil, attrs...)
		} else {
			logWithContext(l.operatorLogger, ctx, slogLevelDebug, "postgres query", nil, attrs...)
		}
	}
	l.delegate.Trace(ctx, begin, sql, sanitizeGORMError(err))
}

const (
	slogLevelDebug slog.Level = slog.LevelDebug
	slogLevelWarn  slog.Level = slog.LevelWarn
	slogLevelError slog.Level = slog.LevelError
)

func logWithContext(logger observability.LogProvider, ctx context.Context, level slog.Level, message string, err error, attrs ...observability.LogAttr) {
	if contextual, ok := logger.(observability.ContextLogProvider); ok {
		contextual.LogContext(ctx, level, message, attrsWithError(err, attrs...)...)
		return
	}
	if err != nil {
		logger.Error(message, err, attrs...)
		return
	}
	switch level {
	case slogLevelWarn:
		logger.Warn(message, attrs...)
	case slogLevelDebug:
		logger.Debug(message, attrs...)
	default:
		logger.Info(message, attrs...)
	}
}

func attrsWithError(err error, attrs ...observability.LogAttr) []observability.LogAttr {
	if err == nil {
		return attrs
	}
	return append([]observability.LogAttr{{Key: "error", Value: err.Error()}}, attrs...)
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
