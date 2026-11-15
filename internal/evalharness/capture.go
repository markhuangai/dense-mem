package evalharness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"

	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const captureLimit = 16 << 20

type Capture struct {
	Logger    *slog.Logger
	Protector *observability.CredentialProtector
	RunID     string
	mu        sync.Mutex
	sequence  int
	failed    bool
}

type Event struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type captureRecord struct {
	RunID    string                            `json:"run_id"`
	Sequence int                               `json:"sequence"`
	Chunk    observability.DiagnosticBodyChunk `json:"payload_chunk"`
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
	chunks := observability.ChunkProtectedDiagnosticBody(protected, captureLimit)
	for _, chunk := range chunks {
		if chunk.State != "captured" {
			c.failed = true
			c.Logger.Error("eval_capture_unavailable", "run_id", c.RunID, "sequence", c.sequence+1, "reason", chunk.ReasonCode)
			return errors.New("benchmark payload protection unavailable")
		}
	}
	c.sequence++
	for _, chunk := range chunks {
		record, err := json.Marshal(captureRecord{RunID: c.RunID, Sequence: c.sequence, Chunk: chunk})
		if err != nil {
			c.failed = true
			return err
		}
		// Encode only after credential protection so Actions masking preserves the protected payload.
		c.Logger.Debug("eval_capture", "record", base64.StdEncoding.EncodeToString(record))
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
