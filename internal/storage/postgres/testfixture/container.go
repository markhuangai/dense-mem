// Package testfixture owns disposable PostgreSQL startup for repository tests.
package testfixture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Run preserves the caller's container options and retries only published-port collisions.
func Run(ctx context.Context, image string, options ...testcontainers.ContainerCustomizer) (*postgres.PostgresContainer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	level, err := observability.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return nil, err
	}
	logger := observability.New(level).With(
		observability.String("run_id", os.Getenv("DENSE_MEM_CI_PRECHECK_RUN_ID")),
		observability.String("run_attempt", os.Getenv("DENSE_MEM_CI_PRECHECK_RUN_ATTEMPT")),
		observability.String("project", os.Getenv("DENSE_MEM_CI_PRECHECK_PROJECT")),
		observability.String("scenario", os.Getenv("DENSE_MEM_E2E_SCENARIO")),
		observability.String("source_revision", os.Getenv("DENSE_MEM_E2E_COMMIT_SHA")),
		observability.String("image_digest", os.Getenv("DENSE_MEM_CI_PRECHECK_IMAGE_DIGEST")),
		observability.String("image", image),
		observability.String("network", os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK")),
	).(observability.ContextLogProvider)
	const maxAttempts = 4
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(lastErr, err)
		}
		started := time.Now()
		logger.InfoContext(ctx, "postgres_fixture_start", observability.Int("attempt", attempt))
		container, startupErr := postgres.Run(ctx, image, options...)
		if startupErr == nil {
			logger.InfoContext(ctx, "postgres_fixture_ready", observability.Int("attempt", attempt),
				observability.String("container_id", container.GetContainerID()),
				observability.Int("elapsed_ms", int(time.Since(started).Milliseconds())))
			return container, nil
		}
		retry := publishedPortCollision(startupErr)
		lastErr = fmt.Errorf("Postgres startup attempt=%d elapsed=%s: %w", attempt, time.Since(started), startupErr)
		containerID := ""
		if container != nil {
			containerID = container.GetContainerID()
			diagnosticLevel := slog.LevelWarn
			if !retry || attempt == maxAttempts || ctx.Err() != nil {
				diagnosticLevel = slog.LevelError
			}
			lastErr = errors.Join(lastErr, startupDiagnostics(ctx, container, logger, diagnosticLevel, attempt))
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			// Leave time to remove the container after stopping stalled initialization.
			cleanupErr := container.Terminate(cleanupCtx, testcontainers.StopTimeout(5*time.Second))
			cancel()
			if cleanupErr != nil {
				lastErr = errors.Join(lastErr, fmt.Errorf("terminate failed Postgres container %s: %w", containerID, cleanupErr))
				logger.ErrorContext(ctx, "postgres_fixture_cleanup_failed", cleanupErr, observability.Int("attempt", attempt),
					observability.String("container_id", containerID), observability.String("startup_cause", startupErr.Error()))
				return nil, lastErr
			}
			logger.InfoContext(ctx, "postgres_fixture_removed", observability.Int("attempt", attempt),
				observability.String("container_id", containerID))
		}
		attrs := []observability.LogAttr{observability.Int("attempt", attempt), observability.String("container_id", containerID),
			observability.Int("elapsed_ms", int(time.Since(started).Milliseconds()))}
		if !retry || attempt == maxAttempts || ctx.Err() != nil {
			logger.ErrorContext(ctx, "postgres_fixture_failed", startupErr, attrs...)
			return nil, errors.Join(lastErr, ctx.Err())
		}
		backoff := time.Duration(attempt) * 250 * time.Millisecond
		logger.WarnContext(ctx, "postgres_fixture_retry", append(attrs,
			observability.String("cause", startupErr.Error()), observability.Int("backoff_ms", int(backoff.Milliseconds())))...)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errors.Join(lastErr, ctx.Err())
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func publishedPortCollision(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := strings.ToLower(err.Error())
	allocation := strings.Contains(message, "rootlesskit portmanager.addport()") ||
		strings.Contains(message, "failed programming external connectivity") ||
		strings.Contains(message, "failed to bind host port")
	return allocation && (strings.Contains(message, "address already in use") || strings.Contains(message, "port is already allocated"))
}

func startupDiagnostics(ctx context.Context, container *postgres.PostgresContainer, logger observability.ContextLogProvider, level slog.Level, attempt int) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	diagnostics := fmt.Errorf("Postgres startup container=%s", container.GetContainerID())
	attrs := []observability.LogAttr{observability.Int("attempt", attempt), observability.String("container_id", container.GetContainerID())}
	state, err := container.State(ctx)
	if err != nil {
		diagnostics = errors.Join(diagnostics, fmt.Errorf("collect Postgres startup state: %w", err))
		attrs = append(attrs, observability.String("state_unavailable", err.Error()))
	} else {
		diagnostics = errors.Join(diagnostics, fmt.Errorf("Postgres startup status=%s running=%t exit_code=%d oom_killed=%t", state.Status, state.Running, state.ExitCode, state.OOMKilled))
		attrs = append(attrs, observability.String("status", string(state.Status)), observability.Bool("running", state.Running),
			observability.Int("exit_code", state.ExitCode), observability.Bool("oom_killed", state.OOMKilled))
	}
	logs, err := container.Logs(ctx)
	if err != nil {
		logger.LogContext(ctx, level, "postgres_fixture_diagnostics", append(attrs, observability.String("logs_unavailable", err.Error()))...)
		return errors.Join(diagnostics, fmt.Errorf("collect Postgres startup logs: %w", err))
	}
	const maxLogBytes = 16 * 1024
	excerpt, readErr := io.ReadAll(io.LimitReader(logs, maxLogBytes+1))
	closeErr := logs.Close()
	truncated := len(excerpt) > maxLogBytes
	if truncated {
		excerpt = excerpt[:maxLogBytes]
	}
	attrs = append(attrs, observability.Bool("logs_truncated", truncated), observability.Int("log_bytes", len(excerpt)))
	if err := errors.Join(readErr, closeErr); err != nil {
		attrs = append(attrs, observability.String("logs_interrupted", err.Error()))
	}
	// Protect the complete excerpt before splitting so credentials crossing chunk boundaries stay masked.
	protected := observability.NewCredentialProtector().Snapshot(string(excerpt), 128*1024, observability.AuthenticationSecretsFromContext(ctx)...)
	protectedText := "[omitted: credential protection unavailable]"
	if protected.UnavailableReason != observability.CredentialProtectionAvailable {
		logger.LogContext(ctx, level, "postgres_fixture_diagnostics", append(attrs,
			observability.Int("diagnostic_unavailable_reason", int(protected.UnavailableReason)))...)
	} else {
		protectedText = protected.Value.(string)
		text := protectedText
		attrs = append(attrs, observability.Int("protected_bytes", len(text)))
		var chunks []string
		for len(text) > 0 {
			end := min(len(text), 2048)
			for end < len(text) && !utf8.RuneStart(text[end]) {
				end--
			}
			chunks = append(chunks, text[:end])
			text = text[end:]
		}
		logger.LogContext(ctx, level, "postgres_fixture_diagnostics", append(attrs, observability.Int("chunk_count", len(chunks)))...)
		for index, chunk := range chunks {
			logger.LogContext(ctx, level, "postgres_fixture_log_chunk", observability.Int("attempt", attempt),
				observability.String("container_id", container.GetContainerID()), observability.Int("chunk_index", index),
				observability.Int("chunk_count", len(chunks)), observability.String("content", chunk))
		}
	}
	diagnostics = errors.Join(diagnostics, fmt.Errorf("Postgres startup logs (logs_truncated=%t):\n%s", truncated, protectedText))
	if err := errors.Join(readErr, closeErr); err != nil {
		diagnostics = errors.Join(diagnostics, fmt.Errorf("collect Postgres startup log content: %w", err))
	}
	return diagnostics
}
