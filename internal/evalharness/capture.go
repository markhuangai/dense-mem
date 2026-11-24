package evalharness

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const captureLimit = 16 << 20
const captureInlineLimit = observability.MaxOperationMetadataBytes / 2

type Capture struct {
	Logger               *slog.Logger
	Protector            *observability.CredentialProtector
	RunID                string
	mu                   sync.Mutex
	sequence             int
	failed               atomic.Bool
	lost                 atomic.Bool
	firstFailure         atomic.Pointer[CaptureFailure]
	phase                atomic.Pointer[string]
	queue                *captureQueue
	importStart          time.Time
	importStartCompleted int
}

type Event struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
	phase   string
}

type CaptureFailure struct {
	Phase      string `json:"phase,omitempty"`
	Kind       string `json:"kind"`
	Sequence   int    `json:"sequence,omitempty"`
	Reason     string `json:"reason"`
	Bytes      int    `json:"bytes,omitempty"`
	Protection string `json:"protection,omitempty"`
	Source     string `json:"source,omitempty"`
	ExchangeID string `json:"exchange_id,omitempty"`
}

func (c *Capture) setPhase(phase string) {
	c.phase.Store(&phase)
}

func (c *Capture) currentPhase() string {
	if phase := c.phase.Load(); phase != nil {
		return *phase
	}
	return ""
}

func (c *Capture) recordFailure(failure CaptureFailure) {
	c.firstFailure.CompareAndSwap(nil, &failure)
}

func (c *Capture) unavailable(failure CaptureFailure) {
	c.recordFailure(failure)
	c.failed.Store(true)
	c.Logger.Error("eval_capture_unavailable", "run_id", c.RunID, "phase", failure.Phase,
		"kind", failure.Kind, "sequence", failure.Sequence, "reason", failure.Reason,
		"bytes", failure.Bytes, "protection", failure.Protection)
}

func (c *Capture) RecordRuntimeLogFailure(failure CaptureFailure) {
	if c != nil {
		failure.Phase = c.currentPhase()
		failure.Source = "server_log"
		c.recordFailure(failure)
	}
}

func (c *Capture) FirstFailure() *CaptureFailure {
	return c.firstFailure.Load()
}

type captureProgress struct {
	Stage     string `json:"stage"`
	Condition string `json:"condition,omitempty"`
	Unit      string `json:"unit"`
	Total     int    `json:"total"`
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
	Remaining int    `json:"remaining"`
}

func (c *Capture) emitProgress(progress captureProgress) error {
	return c.emitProgressContext(context.Background(), progress)
}

func (c *Capture) emitProgressContext(ctx context.Context, progress captureProgress) error {
	if progress.Total < 0 || progress.Completed < 0 || progress.Failed < 0 || progress.Completed > progress.Total || progress.Failed > progress.Total-progress.Completed {
		return errors.New("invalid evaluation progress counts")
	}
	progress.Remaining = progress.Total - progress.Completed - progress.Failed
	return c.EmitContext(ctx, "progress", progress)
}

func (c *Capture) incomplete() bool {
	if c == nil {
		return false
	}
	return c.failed.Load() || c.lost.Load()
}

func NewRunnerCredentialProtector(configured ...string) *observability.CredentialProtector {
	values := append([]string(nil), configured...)
	for _, value := range configured {
		if endpoint, err := url.Parse(value); err == nil && (endpoint.Scheme == "http" || endpoint.Scheme == "https") && endpoint.Hostname() != "" {
			// DNS and TLS errors can contain the host alone.
			values = append(values, endpoint.Hostname())
		}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			values = append(values, encoding.EncodeToString([]byte(value)))
		}
		encoded := hex.EncodeToString([]byte(value))
		values = append(values, encoded, strings.ToUpper(encoded))
	}
	return observability.NewCredentialProtector(values...)
}

func (c *Capture) Emit(kind string, payload any) error {
	return c.EmitContext(context.Background(), kind, payload)
}

func (c *Capture) EmitContext(ctx context.Context, kind string, payload any) error {
	return c.emitRecord(ctx, kind, payload, false)
}

func (c *Capture) EmitTerminal(ctx context.Context, kind string, payload any) error {
	return c.emitRecord(ctx, kind, payload, true)
}

func (c *Capture) emitRecord(ctx context.Context, kind string, payload any, terminal bool) error {
	if c == nil {
		return nil
	}
	phase := c.currentPhase()
	if c.queue != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			c.recordFailure(CaptureFailure{Phase: phase, Kind: kind, Reason: "invalid_record"})
			c.lost.Store(true)
			return err
		}
		event := Event{Kind: kind, Payload: data, phase: phase}
		err = c.queue.enqueue(ctx, event)
		if err != nil && terminal && ctx.Err() != nil && c.queue.ctx.Err() == nil {
			err = c.queue.enqueue(c.queue.preservation(ctx), event)
		}
		if err != nil {
			reason := "enqueue_interrupted"
			if len(data) > captureLimit {
				reason = "body_bytes_exceeded"
			}
			c.recordFailure(CaptureFailure{Phase: phase, Kind: kind, Reason: reason, Bytes: len(data)})
			c.lost.Store(true)
		}
		return err
	}
	return c.emit(ctx, phase, kind, payload)
}

func (c *Capture) emit(ctx context.Context, phase, kind string, payload any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed.Load() {
		return errors.New("benchmark log capture is incomplete")
	}
	if progress, ok := payload.(captureProgress); ok && progress.Stage == "import" {
		if c.importStart.IsZero() {
			c.importStart = time.Now()
			c.importStartCompleted = progress.Completed
		}
		elapsed := time.Since(c.importStart)
		rate := float64(0)
		if elapsed > 0 {
			rate = float64(max(0, progress.Completed-c.importStartCompleted)) / elapsed.Minutes()
		}
		payload = struct {
			captureProgress
			ElapsedMS          int64   `json:"elapsed_ms"`
			DocumentsPerMinute float64 `json:"documents_per_minute"`
		}{progress, elapsed.Milliseconds(), rate}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		c.recordFailure(CaptureFailure{Phase: phase, Kind: kind, Sequence: c.sequence + 1, Reason: "invalid_record"})
		return err
	}
	body, err := json.Marshal(Event{Kind: kind, Payload: data})
	if err != nil {
		return err
	}
	if c.Logger == nil || !c.Logger.Enabled(context.Background(), slog.LevelDebug) {
		c.recordFailure(CaptureFailure{Phase: phase, Kind: kind, Sequence: c.sequence + 1, Reason: "debug_capture_disabled"})
		c.failed.Store(true)
		return errors.New("DEBUG benchmark console capture is required")
	}
	var protected []byte
	reason := observability.CredentialProtectionUnsupported
	if c.Protector != nil {
		protected, reason = c.Protector.ProtectDiagnosticBytes(body, captureLimit)
	}
	if reason != observability.CredentialProtectionAvailable {
		c.unavailable(CaptureFailure{Phase: phase, Kind: kind, Sequence: c.sequence + 1, Reason: reason.Code(), Bytes: len(body), Protection: reason.CaptureReason(captureLimit)})
		return errors.New("benchmark payload protection unavailable")
	}
	var event Event
	if err := json.Unmarshal(protected, &event); err != nil {
		c.recordFailure(CaptureFailure{Phase: phase, Kind: kind, Sequence: c.sequence + 1, Reason: "invalid_protected_record"})
		c.failed.Store(true)
		return errors.New("protected benchmark event is invalid")
	}
	var chunks []observability.DiagnosticBodyChunk
	if len(protected) > captureInlineLimit {
		chunks = observability.ChunkProtectedDiagnosticBody(protected, captureLimit)
	}
	for _, chunk := range chunks {
		if chunk.State != "captured" {
			c.unavailable(CaptureFailure{Phase: phase, Kind: kind, Sequence: c.sequence + 1, Reason: chunk.ReasonCode, Bytes: len(protected)})
			return errors.New("benchmark payload protection unavailable")
		}
	}
	c.sequence++
	level, message := slog.LevelDebug, "eval_capture"
	if event.Kind == "progress" {
		level, message = slog.LevelInfo, "eval_progress"
	} else if event.Kind == "resources" {
		level, message = slog.LevelInfo, "eval_resources"
	} else if event.Kind == "phase_started" || event.Kind == "finished" {
		level = slog.LevelInfo
	} else if event.Kind == "answer_transport_retry" || event.Kind == "runtime_transport_failure" {
		level = slog.LevelWarn
	} else if event.Kind == "campaign_failed" || event.Kind == "seed_verification_failed" {
		level = slog.LevelError
	}
	fields := []any{"run_id", c.RunID, "sequence", c.sequence, "kind", event.Kind}
	write := func(attrs ...any) error {
		record := slog.NewRecord(time.Now(), level, message, 0)
		record.Add(attrs...)
		if err := c.Logger.Handler().Handle(ctx, record); err != nil {
			c.recordFailure(CaptureFailure{Phase: phase, Kind: kind, Sequence: c.sequence, Reason: "console_write_failed", Bytes: len(protected)})
			c.failed.Store(true)
			return errors.New("benchmark console write failed")
		}
		return nil
	}
	if len(chunks) == 0 {
		return write(append(fields, "payload", json.RawMessage(event.Payload))...)
	}
	for _, chunk := range chunks {
		if err := write(append(fields, "payload_chunk", chunk)...); err != nil {
			return err
		}
	}
	return nil
}

func (c *Capture) ProtectError(err error) string {
	if err == nil {
		return ""
	}
	if c == nil || c.Protector == nil {
		return "operation failed"
	}
	value, reason := observability.ProtectDiagnosticText(context.Background(), c.Protector, []byte(err.Error()), 1<<20)
	if reason != observability.CredentialProtectionAvailable {
		return "error detail unavailable: " + reason.Code()
	}
	return string(value)
}

func readJSON(path string, out any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return jsonstrict.Decode(file, out, 128<<20)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
