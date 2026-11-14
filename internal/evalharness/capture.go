package evalharness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
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
	previous  string
	failed    bool
}

type Event struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type captureRecord struct {
	RunID    string                            `json:"run_id"`
	Sequence int                               `json:"sequence"`
	Previous string                            `json:"previous"`
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
	chunks := observability.CaptureDiagnosticBody(context.Background(), c.Protector, body, captureLimit)
	for _, chunk := range chunks {
		if chunk.State != "captured" {
			c.failed = true
			c.Logger.Error("eval_capture_unavailable", "run_id", c.RunID, "sequence", c.sequence+1, "reason", chunk.ReasonCode)
			return errors.New("benchmark payload protection unavailable")
		}
	}
	c.sequence++
	for _, chunk := range chunks {
		record, err := json.Marshal(captureRecord{RunID: c.RunID, Sequence: c.sequence, Previous: c.previous, Chunk: chunk})
		if err != nil {
			c.failed = true
			return err
		}
		// Encode only after credential protection so Actions masking cannot change replay bytes.
		c.Logger.Debug("eval_capture", "record", base64.StdEncoding.EncodeToString(record))
	}
	c.previous = chunks[0].SHA256
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

// ReplayEvents reconstructs only the exact run's ordered, complete protected log stream.
func ReplayEvents(readers []io.Reader, runID string) ([]Event, error) {
	events := []Event{}
	previous := ""
	sequence := 1
	var current []captureRecord
	consume := func() error {
		count := current[0].Chunk.Count
		if count <= 0 || len(current) != count {
			return errors.New("replay unavailable: incomplete payload chunks")
		}
		parts := make([]string, count)
		seen := map[int]bool{}
		for _, row := range current {
			chunk := row.Chunk
			if chunk.State != "captured" || chunk.Count != count || chunk.Index < 0 || chunk.Index >= count || seen[chunk.Index] || row.Previous != previous || chunk.SHA256 != current[0].Chunk.SHA256 {
				return errors.New("replay unavailable: invalid capture chain")
			}
			seen[chunk.Index] = true
			parts[chunk.Index] = chunk.Body
		}
		body := []byte(strings.Join(parts, ""))
		if bytesHash(body) != current[0].Chunk.SHA256 {
			return errors.New("replay unavailable: payload hash mismatch")
		}
		var event Event
		if err := json.Unmarshal(body, &event); err != nil {
			return err
		}
		switch event.Kind {
		case "started", "mapping", "canonical", "observation", "finished":
			events = append(events, event)
		}
		previous = current[0].Chunk.SHA256
		sequence++
		current = nil
		return nil
	}
	for _, reader := range readers {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			start := strings.Index(line, "{")
			if start < 0 {
				continue
			}
			var envelope struct {
				Message string `json:"msg"`
				Record  string `json:"record"`
				RunID   string `json:"run_id"`
			}
			if json.Unmarshal([]byte(line[start:]), &envelope) != nil {
				if strings.Contains(line, "eval_capture") && strings.Contains(line, runID) {
					return nil, errors.New("replay unavailable: malformed log record")
				}
				continue
			}
			if envelope.Message == "eval_capture_unavailable" && envelope.RunID == runID {
				return nil, errors.New("replay unavailable: capture unavailable")
			}
			if envelope.Message != "eval_capture" {
				continue
			}
			body, err := base64.StdEncoding.DecodeString(envelope.Record)
			var row captureRecord
			if err != nil || json.Unmarshal(body, &row) != nil {
				return nil, errors.New("replay unavailable: malformed encoded capture record")
			}
			if row.RunID != runID {
				continue
			}
			if row.Sequence != sequence {
				return nil, errors.New("replay unavailable: log sequence interrupted or duplicated")
			}
			if row.Chunk.Count < 1 || row.Chunk.Count > captureLimit/1024+1 {
				return nil, errors.New("replay unavailable: invalid chunk bound")
			}
			current = append(current, row)
			if len(current) == row.Chunk.Count {
				if err := consume(); err != nil {
					return nil, err
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("replay unavailable: %w", err)
		}
	}
	if len(current) != 0 {
		return nil, errors.New("replay unavailable: interrupted payload")
	}
	if len(events) == 0 {
		return nil, errors.New("replay unavailable: retained Actions logs missing or expired")
	}
	if events[0].Kind != "started" || events[len(events)-1].Kind != "finished" {
		return nil, errors.New("replay unavailable: capture lacks terminal boundaries")
	}

	return events, nil
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

func decodePayload(event Event, out any) error {
	return jsonstrict.Decode(bytes.NewReader(event.Payload), out, captureLimit)
}
