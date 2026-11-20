package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func ownedRuntimeContainer(ctx context.Context, runID string) (id string, running bool, err error) {
	container := os.Getenv("EVAL_SERVER_CONTAINER")
	if container != "densemem-eval-"+runID+"-server" {
		return "", false, errors.New("server log collection identity mismatch")
	}
	inspection := exec.CommandContext(ctx, "docker", "container", "inspect", "--format", `{{.Id}} {{.State.Running}} {{index .Config.Labels "io.dense-mem.eval.run"}}`, container)
	data, err := inspection.Output()
	fields := strings.Fields(string(data))
	if err != nil || len(fields) != 3 || fields[2] != runID || (fields[1] != "true" && fields[1] != "false") {
		return "", false, errors.New("server log ownership cannot be verified")
	}
	if id, err := hex.DecodeString(fields[0]); err != nil || len(id) != sha256.Size {
		return "", false, errors.New("server container ID is invalid")
	}
	return fields[0], fields[1] == "true", nil
}

func stopRuntime(ctx context.Context, runID string, cutoff bool) error {
	id, running, err := ownedRuntimeContainer(ctx, runID)
	if err != nil || !running {
		return err
	}
	if !cutoff {
		if err := exec.CommandContext(ctx, "docker", "container", "stop", "--signal=SIGTERM", "--timeout=60", id).Run(); err != nil {
			return errors.New("owned evaluation server stop failed")
		}
	}
	// The workflow owns the immediate stop at cutoff. Wait for the same container's terminal state.
	if err := exec.CommandContext(ctx, "docker", "container", "wait", id).Run(); err != nil {
		return errors.New("owned evaluation server exit could not be confirmed")
	}
	return nil
}

func runtimeLogs(ctx context.Context, runID string, capture *evalharness.Capture, usage map[string]evalharness.UsageTotals, requireUsage, requireExited bool) error {
	id, running, err := ownedRuntimeContainer(ctx, runID)
	if err != nil {
		return err
	}
	if requireExited && running {
		return errors.New("owned evaluation server is still running")
	}
	file, err := os.CreateTemp("", "evaluation-server-*.log")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	command := exec.CommandContext(ctx, "docker", "container", "logs", id)
	command.Stdout = file
	command.Stderr = file
	if err = command.Run(); err != nil {
		return errors.New("server log collection failed")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := logReader(ctx, file, capture, usage); err != nil {
		return err
	}
	if requireUsage && len(usage) == 0 {
		return errors.New("required runtime provider exchanges are missing")
	}
	return nil
}

func logFile(ctx context.Context, path string, capture *evalharness.Capture) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return logReader(ctx, file, capture, map[string]evalharness.UsageTotals{})
}

func logReader(ctx context.Context, reader io.Reader, capture *evalharness.Capture, usage map[string]evalharness.UsageTotals) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	type captured struct {
		Count int
		Seen  map[int]bool
		Parts []string
		Hash  string
	}
	groups := map[string]*captured{}
	type exchange struct {
		Phase    string
		Request  bool
		Response bool
		Usage    *struct {
			Input  *int64 `json:"prompt_tokens"`
			Output *int64 `json:"completion_tokens"`
			Total  *int64 `json:"total_tokens"`
		}
	}
	exchanges := map[string]*exchange{}
	finished := map[string]bool{}
	var payloadErr error
	rejectPayload := func(message string) {
		if payloadErr == nil {
			payloadErr = errors.New(message)
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		var record struct {
			Level          string                             `json:"level"`
			Message        string                             `json:"msg"`
			Exchange       string                             `json:"exchange_id"`
			Direction      string                             `json:"direction"`
			Invocation     string                             `json:"invocation_id"`
			Correlation    string                             `json:"correlation_id"`
			TransportState string                             `json:"transport_capture_state"`
			Chunk          *observability.DiagnosticBodyChunk `json:"payload_chunk"`
			CaptureState   string                             `json:"capture_state"`
		}
		if json.Unmarshal([]byte(line), &record) != nil {
			if err := capture.Emit("runtime_log", map[string]string{"line": line}); err != nil {
				return err
			}
			continue
		}
		switch strings.ToUpper(record.Level) {
		case "WARN", "ERROR", "FATAL":
			if err := capture.Emit("runtime_log", map[string]string{"line": line}); err != nil {
				return err
			}
		}
		if record.CaptureState == "unavailable" || record.CaptureState == "formatting_failed" || record.CaptureState == "interrupted" {
			rejectPayload("required runtime payload capture is incomplete")
			continue
		}
		transportFailure := record.Message == "ai_exchange_payload" && record.Exchange != "" && record.Direction == "transport_error"
		// A bad exchange invalidates usage, but later failure reasons still explain the run.
		if record.Chunk == nil || (payloadErr != nil && !transportFailure) {
			continue
		}
		chunk := record.Chunk
		if chunk.State != "captured" || record.TransportState == "interrupted" || record.TransportState == "unavailable" {
			rejectPayload("required runtime payload capture is unavailable or interrupted")
			continue
		}
		if chunk.Count < 1 || chunk.Count > (16<<20)/1024+1 {
			rejectPayload("runtime payload chunk count exceeds capture bounds")
			continue
		}
		var identity map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &identity); err != nil {
			rejectPayload("runtime payload identity is malformed")
			continue
		}
		for _, key := range []string{"time", "level", "payload_chunk"} {
			delete(identity, key)
		}
		encoded, err := json.Marshal(identity)
		if err != nil {
			return err
		}
		key := hashBytes(encoded) + ":" + chunk.SHA256
		if record.Exchange != "" && finished[key] {
			rejectPayload("runtime payload chunk duplicated")
			continue
		}
		group := groups[key]
		if group == nil {
			group = &captured{Count: chunk.Count, Seen: map[int]bool{}, Parts: make([]string, chunk.Count), Hash: chunk.SHA256}
			groups[key] = group
		}
		if chunk.Count != group.Count || chunk.Index < 0 || chunk.Index >= chunk.Count || group.Seen[chunk.Index] {
			rejectPayload("runtime payload chunks are inconsistent")
			continue
		}
		group.Seen[chunk.Index] = true
		group.Parts[chunk.Index] = chunk.Body
		if len(group.Seen) == group.Count {
			body := []byte(strings.Join(group.Parts, ""))
			if hashBytes(body) != group.Hash {
				rejectPayload("runtime payload hash mismatch")
				delete(groups, key)
				continue
			}
			if record.Exchange == "" {
				delete(groups, key)
				continue
			}
			if transportFailure {
				diagnostic := map[string]string{"link": "dense_mem_to_provider", "exchange_id": record.Exchange, "invocation_id": record.Invocation, "correlation_id": record.Correlation}
				if len(body) > observability.MaxOperationMetadataBytes/4 {
					diagnostic["reason_unavailable"] = "error_text_exceeds_log_limit"
				} else {
					diagnostic["error"] = string(body)
				}
				if err := capture.Emit("runtime_transport_failure", diagnostic); err != nil {
					return err
				}
			}
			item := exchanges[record.Exchange]
			if item == nil {
				item = &exchange{}
				exchanges[record.Exchange] = item
			}
			if record.Direction == "request" {
				item.Request = true
				var request struct {
					Input  json.RawMessage `json:"input"`
					Format struct {
						Schema struct {
							Name string `json:"name"`
						} `json:"json_schema"`
					} `json:"response_format"`
				}
				if json.Unmarshal(body, &request) == nil {
					switch request.Format.Schema.Name {
					case "dense_mem_semantic_partition_response":
						item.Phase = "intake_partition"
					case "dense_mem_evidence_assessment_response":
						item.Phase = "intake_assessment"
					case "ontology_evidence_organization_v3":
						item.Phase = "ontology"
					default:
						if request.Input != nil {
							item.Phase = "embedding"
						} else {
							item.Phase = "other"
						}
					}
				}
			}
			if record.Direction == "response" {
				item.Response = true
				var response struct {
					Usage *struct {
						Input  *int64 `json:"prompt_tokens"`
						Output *int64 `json:"completion_tokens"`
						Total  *int64 `json:"total_tokens"`
					} `json:"usage"`
				}
				if json.Unmarshal(body, &response) == nil {
					item.Usage = response.Usage
				}
			}
			finished[key] = true
			delete(groups, key)
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.Join(payloadErr, errors.New("runtime log line exceeded capture bounds or read failed"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if payloadErr != nil {
		return payloadErr
	}
	for _, group := range groups {
		if len(group.Seen) != group.Count {
			return errors.New("runtime payload chunks are incomplete")
		}
	}
	for _, item := range exchanges {
		if !item.Request || !item.Response {
			return errors.New("runtime provider exchange is incomplete")
		}
		if item.Phase == "" {
			item.Phase = "other"
		}
		total := usage[item.Phase]
		total.Attempts++
		if item.Usage == nil {
			total.Unavailable++
		} else {
			measured := item.Usage
			if measured.Input != nil {
				total.Input += *measured.Input
			}
			if measured.Output != nil {
				total.Output += *measured.Output
			}
			if measured.Total != nil {
				total.Total += *measured.Total
			}
			if measured.Input == nil || measured.Output == nil || measured.Total == nil || *measured.Input+*measured.Output != *measured.Total {
				total.Partial++
			} else {
				total.Available++
			}
		}
		usage[item.Phase] = total
	}
	return nil
}

func hashBytes(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }
