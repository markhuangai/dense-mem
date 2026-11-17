package evalharness

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const captureLimit = 16 << 20
const captureInlineLimit = observability.MaxOperationMetadataBytes / 2

type Capture struct {
	Logger      *slog.Logger
	Protector   *observability.CredentialProtector
	RunID       string
	mu          sync.Mutex
	sequence    int
	failed      bool
	importStart time.Time
}

type Event struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
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
	if progress.Total < 0 || progress.Completed < 0 || progress.Failed < 0 || progress.Completed > progress.Total || progress.Failed > progress.Total-progress.Completed {
		return errors.New("invalid evaluation progress counts")
	}
	progress.Remaining = progress.Total - progress.Completed - progress.Failed
	return c.Emit("progress", progress)
}

func NewRunnerCredentialProtector(configured ...string) *observability.CredentialProtector {
	values := append([]string(nil), configured...)
	for _, value := range configured {
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
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return errors.New("benchmark log capture is incomplete")
	}
	if progress, ok := payload.(captureProgress); ok && progress.Stage == "import" {
		if c.importStart.IsZero() {
			c.importStart = time.Now()
		}
		elapsed := time.Since(c.importStart)
		rate := float64(0)
		if elapsed > 0 {
			rate = float64(progress.Completed) / elapsed.Minutes()
		}
		payload = struct {
			captureProgress
			ElapsedMS          int64   `json:"elapsed_ms"`
			DocumentsPerMinute float64 `json:"documents_per_minute"`
		}{progress, elapsed.Milliseconds(), rate}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	body, err := json.Marshal(Event{Kind: kind, Payload: data})
	if err != nil {
		return err
	}
	if c.Logger == nil || !c.Logger.Enabled(context.Background(), slog.LevelDebug) {
		c.failed = true
		return errors.New("DEBUG benchmark console capture is required")
	}
	var protected []byte
	reason := observability.CredentialProtectionUnsupported
	if c.Protector != nil {
		protected, reason = c.Protector.ProtectDiagnosticBytes(body, captureLimit)
	}
	if reason != observability.CredentialProtectionAvailable {
		c.failed = true
		c.Logger.Error("eval_capture_unavailable", "run_id", c.RunID, "sequence", c.sequence+1, "reason", reason.Code())
		return errors.New("benchmark payload protection unavailable")
	}
	var event Event
	if err := json.Unmarshal(protected, &event); err != nil {
		c.failed = true
		return errors.New("protected benchmark event is invalid")
	}
	var chunks []observability.DiagnosticBodyChunk
	if len(protected) > captureInlineLimit {
		chunks = observability.ChunkProtectedDiagnosticBody(protected, captureLimit)
	}
	for _, chunk := range chunks {
		if chunk.State != "captured" {
			c.failed = true
			c.Logger.Error("eval_capture_unavailable", "run_id", c.RunID, "sequence", c.sequence+1, "reason", chunk.ReasonCode)
			return errors.New("benchmark payload protection unavailable")
		}
	}
	c.sequence++
	level, message := slog.LevelDebug, "eval_capture"
	if event.Kind == "progress" {
		level, message = slog.LevelInfo, "eval_progress"
	} else if event.Kind == "resources" {
		level, message = slog.LevelInfo, "eval_resources"
	}
	fields := []any{"run_id", c.RunID, "sequence", c.sequence, "kind", event.Kind}
	if len(chunks) == 0 {
		c.Logger.Log(context.Background(), level, message, append(fields, "payload", json.RawMessage(event.Payload))...)
		return nil
	}
	for _, chunk := range chunks {
		c.Logger.Log(context.Background(), level, message, append(fields, "payload_chunk", chunk)...)
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
