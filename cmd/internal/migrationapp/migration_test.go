package migrationapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

type logEntry struct {
	message string
	attrs   map[string]any
}

type recordingLogger struct {
	mu      sync.Mutex
	entries []logEntry
	notify  chan struct{}
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{notify: make(chan struct{}, 32)}
}

func (l *recordingLogger) Info(message string, args ...any) {
	attrs := make(map[string]any, len(args)/2)
	for index := 0; index+1 < len(args); index += 2 {
		key, ok := args[index].(string)
		if ok {
			attrs[key] = args[index+1]
		}
	}

	l.mu.Lock()
	l.entries = append(l.entries, logEntry{message: message, attrs: attrs})
	l.mu.Unlock()
	select {
	case l.notify <- struct{}{}:
	default:
	}
}

func (l *recordingLogger) entriesSnapshot() []logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]logEntry(nil), l.entries...)
}

func TestRunReportsHeartbeatAndStopsBeforeReturn(t *testing.T) {
	logger := newRecordingLogger()
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)

	go func() {
		result <- runWithInterval(context.Background(), nil, 5*time.Second, "up", logger, 5*time.Millisecond, func(context.Context, *gorm.DB) error {
			close(started)
			<-release
			return nil
		})
	}()

	<-started
	waitForMessage(t, logger, "postgres migrations still running")
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("runWithInterval() error = %v", err)
	}

	before := len(logger.entriesSnapshot())
	time.Sleep(20 * time.Millisecond)
	after := len(logger.entriesSnapshot())
	if after != before {
		t.Fatalf("heartbeat logged after return: before=%d after=%d", before, after)
	}

	entries := logger.entriesSnapshot()
	if entries[0].message != "running postgres migrations" {
		t.Fatalf("first message = %q, want start message", entries[0].message)
	}
	if entries[len(entries)-1].message != "postgres migrations completed" {
		t.Fatalf("last message = %q, want completion message", entries[len(entries)-1].message)
	}
	for _, entry := range entries {
		if entry.message != "postgres migrations still running" {
			continue
		}
		if entry.attrs["direction"] != "up" {
			t.Fatalf("heartbeat direction = %v, want up", entry.attrs["direction"])
		}
		if entry.attrs["timeout_seconds"] != int64(5) {
			t.Fatalf("heartbeat timeout_seconds = %v, want 5", entry.attrs["timeout_seconds"])
		}
		break
	}
}

func TestRunCompletesBeforeHeartbeat(t *testing.T) {
	logger := newRecordingLogger()

	err := runWithInterval(context.Background(), nil, time.Second, "down", logger, 25*time.Millisecond, func(context.Context, *gorm.DB) error {
		return nil
	})
	if err != nil {
		t.Fatalf("runWithInterval() error = %v", err)
	}

	for _, entry := range logger.entriesSnapshot() {
		if entry.message == "postgres migrations still running" {
			t.Fatal("unexpected heartbeat for immediate completion")
		}
	}
}

func TestRunPropagatesMigrationError(t *testing.T) {
	logger := newRecordingLogger()
	want := context.DeadlineExceeded

	err := runWithInterval(context.Background(), nil, time.Second, "up", logger, 25*time.Millisecond, func(context.Context, *gorm.DB) error {
		return want
	})
	if err != want {
		t.Fatalf("runWithInterval() error = %v, want %v", err, want)
	}
	for _, entry := range logger.entriesSnapshot() {
		if entry.message == "postgres migrations completed" {
			t.Fatal("unexpected completion message after migration error")
		}
	}
}

func TestRunRejectsInvalidTimeout(t *testing.T) {
	err := runWithInterval(context.Background(), nil, 0, "up", newRecordingLogger(), time.Millisecond, func(context.Context, *gorm.DB) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "migration timeout must be greater than zero") {
		t.Fatalf("runWithInterval() error = %v, want invalid timeout error", err)
	}
}

func TestRunEnforcesMigrationTimeout(t *testing.T) {
	logger := newRecordingLogger()
	err := runWithInterval(context.Background(), nil, 5*time.Millisecond, "up", logger, time.Millisecond, func(ctx context.Context, _ *gorm.DB) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runWithInterval() error = %v, want context deadline exceeded", err)
	}
}

func waitForMessage(t *testing.T, logger *recordingLogger, message string) {
	t.Helper()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		for _, entry := range logger.entriesSnapshot() {
			if entry.message == message {
				return
			}
		}
		select {
		case <-logger.notify:
		case <-timeout.C:
			t.Fatalf("timed out waiting for log message %q", message)
		}
	}
}
